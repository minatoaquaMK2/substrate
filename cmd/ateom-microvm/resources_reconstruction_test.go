//go:build linux

// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"github.com/agent-substrate/substrate/cmd/ateom-microvm/internal/ch"
	"github.com/agent-substrate/substrate/cmd/ateom-microvm/internal/kata"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/internal/ateomcgroup"
	"github.com/agent-substrate/substrate/internal/cpuplacement"
	"github.com/agent-substrate/substrate/internal/resources"
	"github.com/agent-substrate/substrate/internal/sizing"
)

func TestReconstructionRejectsOverlappingExplicitReservation(t *testing.T) {
	for _, enforcement := range []string{"required", "best-effort"} {
		c := defaultResourceConfig()
		c.Policy, c.Enforcement, c.Explicit, c.Reserved = "shared", enforcement, "1-2", "2"
		if err := c.validate(); err == nil {
			t.Errorf("%s accepted overlapping explicit and reserved CPUs", enforcement)
		}
	}
}

func TestReconstructionRestartProvesExitBeforeReplacement(t *testing.T) {
	r := resourceFixture(t)
	r.killLeaf = func(context.Context, string) error { return nil }
	r.emptyLeaf = func(string) (bool, error) { return false, nil }
	s := NewService("worker", "ch", false, 128, 1, nil, nil)
	s.runtimeResources = r
	if err := s.endPreviousActivation(context.Background(), "actor", nil); err == nil {
		t.Fatal("restart without runningActor bypassed live cgroup proof")
	}
}

func TestReconstructionUnhostRetainsCoWConsumer(t *testing.T) {
	old := *memoryRestorePolicy
	*memoryRestorePolicy = "cow-preferred"
	t.Cleanup(func() { *memoryRestorePolicy = old })
	r := resourceFixture(t)
	r.allocator = nil
	r.emptyLeaf = func(string) (bool, error) { return false, nil }
	r.killLeaf = func(context.Context, string) error { return errors.New("still alive") }
	s := NewService("worker", "ch", false, 128, 1, nil, nil)
	s.actorCgroups, s.runtimeResources = true, r
	if _, _, err := s.admitActor(resources.ActorAttribution{UID: "actor"}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.unhostActor(context.Background(), "actor"); err == nil {
		t.Fatal("unhost released live CoW consumer")
	}
	if s.lookupActor("actor") == nil {
		t.Fatal("unhost lost actor ownership")
	}
}

type testRuntimeHierarchy struct {
	verify func() error
	empty  func(string) (bool, error)
}

func (h testRuntimeHierarchy) Verify() error {
	if h.verify != nil {
		return h.verify()
	}
	return nil
}
func (h testRuntimeHierarchy) Empty(owner string) (bool, error) {
	if err := h.Verify(); err != nil {
		return false, err
	}
	if h.empty != nil {
		return h.empty(owner)
	}
	return true, nil
}

func testResourceSystem(t *testing.T) runtimeResourceSystem {
	t.Helper()
	return runtimeResourceSystem{
		requireControllers: func() error { return nil },
		readBoundary: func() (ateomcgroup.CPUBoundary, error) {
			return ateomcgroup.CPUBoundary{CPUs: cpuSet(t, "0-1"), Mems: cpuSet(t, "0-1")}, nil
		},
		setManagementWeight: func(int) error { return nil },
		discoverTopology: func(cpuplacement.Set) (cpuplacement.Topology, error) {
			return cpuplacement.Topology{Online: cpuSet(t, "0-1"), CPUs: map[int]cpuplacement.CPU{0: {ID: 0, NUMA: 0, LLC: "llc0"}, 1: {ID: 1, NUMA: 1, LLC: "llc1"}}}, nil
		},
		actorLeafStates:      func() ([]ateomcgroup.ActorLeafState, error) { return nil, nil },
		populatedActorOwners: func() ([]string, error) { return nil, nil },
		captureHierarchy:     func() (runtimeHierarchy, error) { return testRuntimeHierarchy{}, nil },
	}
}

func TestRuntimeWeightAppliedOnlyAfterPreflight(t *testing.T) {
	c := defaultResourceConfig()
	c.Policy = "shared"
	c.Enforcement = "best-effort"
	c.ManagementWeight = 100
	for _, failure := range []string{"bad-reservation", "bad-explicit", "unknown-topology", "live-recovery", "hierarchy"} {
		t.Run(failure, func(t *testing.T) {
			c := c
			system := testResourceSystem(t)
			writes := 0
			system.setManagementWeight = func(int) error { writes++; return nil }
			switch failure {
			case "bad-reservation":
				c.Reserved = "5"
			case "bad-explicit":
				c.Explicit = "5"
			case "unknown-topology":
				c.Placement = "require-llc"
				system.discoverTopology = func(cpuplacement.Set) (cpuplacement.Topology, error) { return cpuplacement.Topology{}, nil }
			case "live-recovery":
				system.actorLeafStates = func() ([]ateomcgroup.ActorLeafState, error) { return nil, errors.New("unreadable live owners") }
			case "hierarchy":
				system.captureHierarchy = func() (runtimeHierarchy, error) { return nil, errors.New("missing delegation") }
			}
			_, _ = newRuntimeResourcesWithSystem(context.Background(), c, true, system)
			if writes != 0 {
				t.Fatalf("%s mutated weight before successful preflight", failure)
			}
		})
	}
	system := testResourceSystem(t)
	writeErr := errors.New("partial write")
	system.setManagementWeight = func(int) error { return writeErr }
	if _, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system); !errors.Is(err, writeErr) {
		t.Fatalf("uncertain weight silently fell back: %v", err)
	}
}

func TestRuntimeInvalidEffectiveConfigCannotHideBehindTopologyFallback(t *testing.T) {
	for _, field := range []string{"explicit", "reserved"} {
		c := defaultResourceConfig()
		c.Policy = "shared"
		c.Enforcement = "best-effort"
		c.Placement = "require-numa"
		if field == "explicit" {
			c.Explicit = "5"
		} else {
			c.Reserved = "5"
		}
		system := testResourceSystem(t)
		system.discoverTopology = func(cpuplacement.Set) (cpuplacement.Topology, error) {
			t.Fatal("topology ran before effective config validation")
			return cpuplacement.Topology{}, nil
		}
		if _, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system); err == nil {
			t.Fatal("bad effective config accepted")
		}
	}
}

func TestRuntimeRequiredUnknownTopologyCannotPublishReady(t *testing.T) {
	for _, placement := range []string{"require-llc", "require-numa", "local"} {
		for _, enforcement := range []string{"required", "best-effort"} {
			c := defaultResourceConfig()
			c.Policy = "shared"
			c.Enforcement = enforcement
			if placement == "local" {
				c.MemoryPlacement = "local"
			} else {
				c.Placement = placement
			}
			system := testResourceSystem(t)
			system.discoverTopology = func(cpuplacement.Set) (cpuplacement.Topology, error) { return cpuplacement.Topology{}, nil }
			r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
			if enforcement == "required" && err == nil {
				t.Fatal("required unknown topology accepted")
			}
			if enforcement == "best-effort" && (err != nil || r.allocator != nil || r.fallback == "") {
				t.Fatalf("unreported fallback: %+v %v", r, err)
			}
		}
	}
}

func TestCoWOnlyRecoversAdmissionWithoutCPUControllers(t *testing.T) {
	c := defaultResourceConfig()
	c.TrackExecution = true
	system := testResourceSystem(t)
	system.requireControllers = func() error { t.Fatal("CoW-only recovery touched CPU controllers"); return nil }
	system.readBoundary = func() (ateomcgroup.CPUBoundary, error) {
		t.Fatal("CoW-only recovery touched CPU boundary")
		return ateomcgroup.CPUBoundary{}, nil
	}
	system.populatedActorOwners = func() ([]string, error) { return []string{"old"}, nil }
	r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.recoveredActors()) != 1 || r.allocator != nil {
		t.Fatalf("recovery state=%+v", r)
	}
	s := NewService("worker", "ch", false, 128, 1, nil, nil)
	s.runtimeResources = r
	if _, _, err := s.admitActor(resources.ActorAttribution{UID: "new"}, false); err == nil {
		t.Fatal("lost restarted Actor capacity")
	}
	if _, _, err := s.admitActor(resources.ActorAttribution{UID: "old"}, false); err != nil {
		t.Fatal("same recovered owner cannot reuse slot", err)
	}
}

func TestCoWRecoverySurvivesCPUFallback(t *testing.T) {
	c := defaultResourceConfig()
	c.Policy = "shared"
	c.Enforcement = "best-effort"
	c.TrackExecution = true
	system := testResourceSystem(t)
	system.populatedActorOwners = func() ([]string, error) { return []string{"old"}, nil }
	system.requireControllers = func() error { return errors.New("no cpuset") }
	r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
	if err != nil || r.allocator != nil || len(r.recoveredActors()) != 1 || r.fallback == "" {
		t.Fatalf("fallback lost ownership: %+v %v", r, err)
	}
}

func TestRecoveredAdmissionReleasedOnlyAfterProof(t *testing.T) {
	r := resourceFixture(t)
	r.allocator = nil
	r.recovered = map[string]struct{}{"old": {}}
	r.emptyLeaf = func(string) (bool, error) { return false, nil }
	if err := r.release("old"); err == nil {
		t.Fatal("released populated owner")
	}
	r.emptyLeaf = func(string) (bool, error) { return true, nil }
	r.removeLeaf = func(string) error { return errors.New("busy") }
	if err := r.release("old"); err == nil {
		t.Fatal("released unremovable owner")
	}
	if len(r.recoveredActors()) != 1 {
		t.Fatal("lost owner before proof")
	}
	r.removeLeaf = func(string) error { return nil }
	if err := r.release("old"); err != nil {
		t.Fatal(err)
	}
	if len(r.recoveredActors()) != 0 {
		t.Fatal("proven owner leaked")
	}
}

func TestHierarchyChangeStopsBeforeSignalOrPlacement(t *testing.T) {
	r := resourceFixture(t)
	sentinel := errors.New("replaced root")
	r.verifyHierarchy = func() error { return sentinel }
	r.killLeaf = func(context.Context, string) error { t.Fatal("signaled changed hierarchy"); return nil }
	r.openLeaf = func(string, int64, cpuplacement.Set, cpuplacement.Set) (*ateomcgroup.ActorLeaf, error) {
		t.Fatal("wrote changed hierarchy")
		return nil, nil
	}
	if _, err := r.acquire("actor", sizing.FromLimits(1000, 0), 1); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	s := &AteomService{runtimeResources: r}
	if err := s.ensureActorStopped(context.Background(), "actor"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestBundleCleanupRequiresExitProof(t *testing.T) {
	r := resourceFixture(t)
	r.emptyLeaf = func(string) (bool, error) { return false, nil }
	r.killLeaf = func(context.Context, string) error { return nil }
	s := &AteomService{runtimeResources: r}
	if err := s.cleanupActorBundle(context.Background(), "actor", t.TempDir()); err == nil {
		t.Fatal("bundle cleanup bypassed live consumers")
	}
}

func TestSnapshotLeaseUsesCapturedHierarchy(t *testing.T) {
	r := resourceFixture(t)
	r.emptyLeaf = func(string) (bool, error) { return false, errors.New("replaced hierarchy") }
	s := &AteomService{runtimeResources: r, actorCgroups: true}
	lease := &countedSnapshotLease{}
	if err := s.releaseSnapshotLease("actor", lease); err == nil || lease.releases != 0 {
		t.Fatal("lease bypassed captured hierarchy")
	}
	r.emptyLeaf = func(string) (bool, error) { return true, nil }
	if err := s.releaseSnapshotLease("actor", lease); err != nil || lease.releases != 1 {
		t.Fatal("proved lease not released", err)
	}
}

func TestDefaultLifecycleDoesNotRequireCgroupProof(t *testing.T) {
	old := *memoryRestorePolicy
	*memoryRestorePolicy = "legacy"
	t.Cleanup(func() { *memoryRestorePolicy = old })
	s := NewService("worker", "ch", false, 128, 1, nil, nil)
	if s.requiresActorExitProof() {
		t.Fatal("default changed")
	}
	if err := s.endPreviousActivation(context.Background(), "actor", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureActorStopped(context.Background(), "actor"); err != nil {
		t.Fatal(err)
	}
	if s.failedActorProcessCleanup("actor") != nil {
		t.Fatal("default child cleanup changed")
	}
}

func TestHostProcessWaitRequiresKernelExitProof(t *testing.T) {
	for _, empty := range []bool{false, true} {
		cmd := exec.Command("/bin/sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
		r := resourceFixture(t)
		r.killLeaf = func(context.Context, string) error { return nil }
		r.emptyLeaf = func(string) (bool, error) { return empty, nil }
		s := &AteomService{runtimeResources: r}
		err := s.stopActorHostProcess(context.Background(), "actor", cmd)
		if (err == nil) != empty {
			t.Fatalf("proof=%v err=%v", empty, err)
		}
		reaped := errors.Is(cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone)
		if reaped != empty {
			t.Fatalf("proof=%v waited=%v", empty, reaped)
		}
	}
}

func TestFailedLaunchCleanupUsesDetachedFiniteContext(t *testing.T) {
	r := resourceFixture(t)
	r.killLeaf = func(ctx context.Context, _ string) error {
		if ctx.Err() != nil {
			t.Fatal("cleanup inherited canceled launch context")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("cleanup unbounded")
		}
		return nil
	}
	r.emptyLeaf = func(string) (bool, error) { return true, nil }
	s := &AteomService{runtimeResources: r}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	if err := s.failedActorProcessCleanup("actor")(ctx, cmd); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchFailureCallsOwnershipCleanup(t *testing.T) {
	for _, kind := range []string{"ch", "virtiofsd"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			sentinel := errors.New("cleanup uncertain")
			calls := 0
			cleanup := func(_ context.Context, cmd *exec.Cmd) error {
				calls++
				if cmd == nil || cmd.Process == nil {
					t.Fatal("no started child")
				}
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
				return sentinel
			}
			var err error
			if kind == "ch" {
				_, _, err = ch.LaunchVMM(ctx, ch.LaunchVMMOptions{Binary: "/bin/true", APISocket: t.TempDir() + "/missing.sock", CleanupOnFailure: cleanup})
			} else {
				_, err = kata.StartVirtiofsd(ctx, kata.VirtiofsdOptions{Binary: "/bin/true", SocketPath: t.TempDir() + "/missing.sock", CleanupOnFailure: cleanup})
			}
			if calls != 1 || !errors.Is(err, sentinel) || !errors.Is(err, context.Canceled) {
				t.Fatalf("calls=%d errors=%v", calls, err)
			}
		})
	}

}

func TestStrictTeardownFailureKeepsRetryableLeaseHandle(t *testing.T) {
	for _, strict := range []bool{false, true} {
		s := NewService("worker", "ch", false, 128, 1, nil, nil)
		if strict {
			s.runtimeResources = resourceFixture(t)
			s.runtimeResources.killLeaf = func(context.Context, string) error { return nil }
		}
		h, _, err := s.admitActor(resources.ActorAttribution{UID: "actor"}, false)
		if err != nil {
			t.Fatal(err)
		}
		lease := &countedSnapshotLease{}
		h.vm = &runningActor{snapshotLease: lease}
		sentinel := errors.New("filesystem detach failed after execution stopped")
		if err := s.finishActorTermination(context.Background(), "actor", sentinel); !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		if retained := s.lookupActor("actor") != nil; retained != strict {
			t.Fatalf("strict=%v retained=%v", strict, retained)
		}
		if strict && s.runningVM("actor").snapshotLease != lease {
			t.Fatal("lost retryable lease handle")
		}
	}
}
