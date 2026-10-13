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
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/internal/ateomcgroup"
	"github.com/agent-substrate/substrate/internal/cpuplacement"
	"github.com/agent-substrate/substrate/internal/sizing"
)

func defaultResourceConfig() runtimeResourceConfig {
	return runtimeResourceConfig{Policy: "off", Enforcement: "required", Placement: "any", MemoryPlacement: "inherit"}
}
func TestRuntimeResourcesDisabled(t *testing.T) {
	r, err := newRuntimeResources(context.Background(), defaultResourceConfig(), false)
	if err != nil || r.allocator != nil || r.startup != nil || r.fallback != "" {
		t.Fatalf("disabled resources=%+v err=%v", r, err)
	}
	s := NewService("worker", "ch", false, 128, 10, nil, nil)
	s.runtimeResources = r
	leaf, err := s.actorLeaf("actor", sizing.FromLimits(500, 0))
	if err != nil || leaf != nil {
		t.Fatalf("disabled leaf=%v err=%v", leaf, err)
	}
	if err := s.releaseRuntimeResources("actor"); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimeResourceConfigValidation(t *testing.T) {
	for _, mutate := range []func(*runtimeResourceConfig){
		func(c *runtimeResourceConfig) { c.Policy = "exclusive" },
		func(c *runtimeResourceConfig) { c.Enforcement = "silent" },
		func(c *runtimeResourceConfig) { c.Placement = "random" },
		func(c *runtimeResourceConfig) { c.MemoryPlacement = "migrate" },
		func(c *runtimeResourceConfig) { c.ManagementWeight = 10001 },
		func(c *runtimeResourceConfig) { c.StartupConcurrency = -1 },
		func(c *runtimeResourceConfig) { c.Reserved = "-2" },
		func(c *runtimeResourceConfig) { c.Explicit = "0" },
		func(c *runtimeResourceConfig) { c.Placement = "require-llc" },
	} {
		c := defaultResourceConfig()
		mutate(&c)
		if err := c.validate(); err == nil {
			t.Errorf("accepted invalid config %+v", c)
		}
	}
}
func TestRuntimeResourcesRequiredAndBestEffort(t *testing.T) {
	c := defaultResourceConfig()
	c.Policy = "nonoverlapping"
	if _, err := newRuntimeResources(context.Background(), c, false); err == nil {
		t.Fatal("required placement without delegation succeeded")
	}
	c.Enforcement = "best-effort"
	r, err := newRuntimeResources(context.Background(), c, false)
	if err != nil || r.allocator != nil || r.fallback == "" {
		t.Fatalf("fallback not reported: %+v %v", r, err)
	}
}
func TestRuntimeStartupConcurrency(t *testing.T) {
	c := defaultResourceConfig()
	c.StartupConcurrency = 2
	r, err := newRuntimeResources(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &AteomService{runtimeResources: r}
	done1, err := s.waitRuntimeStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done2, err := s.waitRuntimeStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.waitRuntimeStartup(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked startup err=%v", err)
	}
	done1()
	done2()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			done, err := s.waitRuntimeStartup(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
			done()
		})
	}
	wg.Wait()
	if len(r.startup) != 0 {
		t.Fatal("startup tokens leaked")
	}
}
func cpuSet(t *testing.T, s string) cpuplacement.Set {
	t.Helper()
	set, err := cpuplacement.ParseSet(s)
	if err != nil {
		t.Fatal(err)
	}
	return set
}
func resourceFixture(t *testing.T) *runtimeResources {
	t.Helper()
	b := ateomcgroup.CPUBoundary{CPUs: cpuSet(t, "2,4,6"), Mems: cpuSet(t, "0")}
	a, err := cpuplacement.New(cpuplacement.Options{Allowed: b.CPUs, Policy: cpuplacement.PolicyNonOverlapping, Placement: cpuplacement.PlacementAny})
	if err != nil {
		t.Fatal(err)
	}
	c := defaultResourceConfig()
	c.Policy = "nonoverlapping"
	return &runtimeResources{config: c, allocator: a, boundary: b, leases: map[string]cpuplacement.Lease{}, readBoundary: func() (ateomcgroup.CPUBoundary, error) { return b, nil }, openLeaf: func(string, int64, cpuplacement.Set, cpuplacement.Set) (*ateomcgroup.ActorLeaf, error) {
		return nil, nil
	}, emptyLeaf: func(string) (bool, error) { return true, nil }, removeLeaf: func(string) error { return nil }}
}
func TestActorFractionalQuotaPreserved(t *testing.T) {
	r := resourceFixture(t)
	r.openLeaf = func(_ string, milli int64, cpus, mems cpuplacement.Set) (*ateomcgroup.ActorLeaf, error) {
		if milli != 500 || cpus.Len() != 1 {
			t.Errorf("fractional quota inflated: %d %s", milli, cpus.String())
		}
		return nil, nil
	}
	if _, err := r.acquire("a", sizing.FromLimits(500, 0), 1); err != nil {
		t.Fatal(err)
	}
	if err := r.release("a"); err != nil {
		t.Fatal(err)
	}
	if len(r.allocator.Leases()) != 0 {
		t.Fatal("lease leaked")
	}
}
func TestActorCPUReleaseRequiresProof(t *testing.T) {
	r := resourceFixture(t)
	if _, err := r.acquire("a", sizing.FromLimits(1000, 0), 1); err != nil {
		t.Fatal(err)
	}
	r.emptyLeaf = func(string) (bool, error) { return false, nil }
	if err := r.release("a"); err == nil {
		t.Fatal("released populated cgroup")
	}
	if len(r.allocator.Leases()) != 1 {
		t.Fatal("lost lease")
	}
	r.emptyLeaf = func(string) (bool, error) { return true, nil }
	r.removeLeaf = func(string) error { return errors.New("busy") }
	if err := r.release("a"); err == nil {
		t.Fatal("released unremovable cgroup")
	}
	r.removeLeaf = func(string) error { return nil }
	if err := r.release("a"); err != nil {
		t.Fatal(err)
	}
	if err := r.release("a"); err != nil {
		t.Fatal(err)
	}
}
func TestActorCPUApplyRollback(t *testing.T) {
	for _, empty := range []bool{true, false} {
		r := resourceFixture(t)
		r.openLeaf = func(string, int64, cpuplacement.Set, cpuplacement.Set) (*ateomcgroup.ActorLeaf, error) {
			return nil, errors.New("placement write failed")
		}
		r.emptyLeaf = func(string) (bool, error) { return empty, nil }
		if _, err := r.acquire("a", sizing.FromLimits(1000, 0), 1); err == nil {
			t.Fatal("apply unexpectedly succeeded")
		}
		if got := len(r.allocator.Leases()); (empty && got != 0) || (!empty && got != 1) {
			t.Errorf("empty=%v leases=%d", empty, got)
		}
	}
}
func TestActorCPUCapacityNeverDowngrades(t *testing.T) {
	r := resourceFixture(t)
	r.config.Enforcement = "best-effort"
	if _, err := r.acquire("a", sizing.FromLimits(3000, 0), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.acquire("b", sizing.FromLimits(500, 0), 1); err == nil {
		t.Fatal("nonoverlapping allocation silently oversubscribed")
	}
}
func TestActorCPUReservationPreservesActorPool(t *testing.T) {
	r := resourceFixture(t)
	r.config.Policy = "off"
	r.config.Reserved = "2"
	a, err := cpuplacement.New(cpuplacement.Options{Allowed: r.boundary.CPUs, Reserved: cpuSet(t, "2"), Policy: cpuplacement.PolicyShared, Placement: cpuplacement.PlacementAny})
	if err != nil {
		t.Fatal(err)
	}
	r.allocator = a
	r.openLeaf = func(_ string, _ int64, cpus, mems cpuplacement.Set) (*ateomcgroup.ActorLeaf, error) {
		if cpus.String() != "4,6" {
			t.Errorf("Actor fence = %s", cpus.String())
		}
		return nil, nil
	}
	if _, err := r.acquire("a", sizing.FromLimits(500, 0), 1); err != nil {
		t.Fatal(err)
	}
}
func TestActorMemoryPlacementRollback(t *testing.T) {
	r := resourceFixture(t)
	r.config.MemoryPlacement = "local"
	if _, err := r.acquire("a", sizing.FromLimits(500, 0), 1); err == nil {
		t.Fatal("unknown NUMA allowed strict local allocation")
	}
	if len(r.allocator.Leases()) != 0 {
		t.Fatal("invalid memory placement leaked lease")
	}
}
func TestActorCPUHotplugStopsAdmission(t *testing.T) {
	r := resourceFixture(t)
	if _, err := r.acquire("a", sizing.FromLimits(3000, 0), 1); err != nil {
		t.Fatal(err)
	}
	r.readBoundary = func() (ateomcgroup.CPUBoundary, error) {
		return ateomcgroup.CPUBoundary{CPUs: cpuSet(t, "4,6"), Mems: cpuSet(t, "0")}, nil
	}
	if _, err := r.acquire("b", sizing.FromLimits(500, 0), 1); err == nil {
		t.Fatal("admitted after active CPU vanished")
	}
	if len(r.allocator.Leases()) != 1 {
		t.Fatal("lost active ownership")
	}
}

func TestRuntimeStartupAlreadyCanceledWithCapacity(t *testing.T) {
	c := defaultResourceConfig()
	c.StartupConcurrency = 2
	r, err := newRuntimeResources(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &AteomService{runtimeResources: r}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 20 {
		if _, err := s.waitRuntimeStartup(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal("canceled startup admitted")
		}
	}
	if len(r.startup) != 0 {
		t.Fatal("canceled startup leaked a slot")
	}
}

func TestEnsureActorStoppedBoundsCleanup(t *testing.T) {
	r := resourceFixture(t)
	if _, err := r.acquire("a", sizing.FromLimits(1000, 0), 1); err != nil {
		t.Fatal(err)
	}
	r.killLeaf = func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
			t.Fatal("cleanup has no finite five-second bound")
		}
		return context.DeadlineExceeded
	}
	r.emptyLeaf = func(string) (bool, error) { return false, nil }
	s := &AteomService{runtimeResources: r}
	if err := s.ensureActorStopped(context.Background(), "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup error=%v", err)
	}
	if len(r.allocator.Leases()) != 1 {
		t.Fatal("uncertain cleanup lost CPU ownership")
	}
}

func TestRuntimeResourcesMissingChildController(t *testing.T) {
	c := defaultResourceConfig()
	c.Policy = "shared"
	missing := func() error { return errors.New("cpuset is not enabled for Actor descendants") }
	system := testResourceSystem(t)
	system.requireControllers = missing
	if _, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system); err == nil {
		t.Fatal("required policy accepted absent child controller")
	}
	c.Enforcement = "best-effort"
	r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
	if err != nil || r.allocator != nil || r.fallback == "" {
		t.Fatalf("missing controller did not yield explicit initialization fallback: %+v %v", r, err)
	}
}
