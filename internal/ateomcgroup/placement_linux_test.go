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

package ateomcgroup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/internal/cpuplacement"
	"github.com/agent-substrate/substrate/internal/roottest"
	"golang.org/x/sys/unix"
)

// cpuPlacementRequired makes every missing integration precondition fatal in
// CI and is directly exercisable on a workstation without cgroup privileges.
func cpuPlacementRequired() bool {
	return os.Getenv("CI") == "true" || os.Getenv("REQUIRE_CPU_PLACEMENT") == "true"
}

func TestCPUPlacementRequired(t *testing.T) {
	for _, tc := range []struct {
		ci, require string
		want        bool
	}{
		{"", "", false}, {"true", "", true}, {"", "true", true}, {"true", "true", true},
		{"false", "false", false}, {"1", "1", false}, {"TRUE", "TRUE", false},
	} {
		t.Run(tc.ci+"/"+tc.require, func(t *testing.T) {
			t.Setenv("CI", tc.ci)
			t.Setenv("REQUIRE_CPU_PLACEMENT", tc.require)
			if got := cpuPlacementRequired(); got != tc.want {
				t.Errorf("required = %t, want %t", got, tc.want)
			}
		})
	}
}

func cpuPlacementUnavailable(t *testing.T, format string, args ...any) {
	t.Helper()
	reason := fmt.Sprintf(format, args...)
	if cpuPlacementRequired() {
		t.Fatalf("CPU placement integration required (CI=true or REQUIRE_CPU_PLACEMENT=true): %s", reason)
	}
	t.Skipf("CPU placement integration unavailable: %s", reason)
}

// delegatedPlacementRoot creates only a disposable child of an explicitly
// provided, already-delegated subtree. It never enables a host/Pod controller,
// moves this test process, changes affinity, or alters any ancestor's quota.
func delegatedPlacementRoot(t *testing.T) (string, CPUBoundary) {
	t.Helper()
	root := os.Getenv("CPU_PLACEMENT_CGROUP_ROOT")
	if root == "" {
		cpuPlacementUnavailable(t, "set CPU_PLACEMENT_CGROUP_ROOT to a writable, disposable cgroup v2 subtree with cpu and cpuset already enabled")
	}
	if !filepath.IsAbs(root) {
		cpuPlacementUnavailable(t, "CPU_PLACEMENT_CGROUP_ROOT must be an absolute delegated subtree path, got %q", root)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		cpuPlacementUnavailable(t, "resolve CPU_PLACEMENT_CGROUP_ROOT=%q: %v", root, err)
	}
	if resolved == "/" || resolved == Root {
		cpuPlacementUnavailable(t, "CPU_PLACEMENT_CGROUP_ROOT must name a dedicated subtree, never the host mount root %q", resolved)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(resolved, &stat); err != nil {
		cpuPlacementUnavailable(t, "inspect delegated cgroup filesystem: %v", err)
	}
	if stat.Type != unix.CGROUP2_SUPER_MAGIC {
		cpuPlacementUnavailable(t, "%q is not a real cgroup v2 filesystem", resolved)
	}
	// Keep the root-test discovery convention while checking strictness first:
	// roottest.Require alone would silently skip the CI precondition.
	if os.Geteuid() != 0 && cpuPlacementRequired() {
		t.Fatal("CPU placement integration required: run as root in the explicitly delegated test environment; roottest.Require must not skip CI")
	}
	roottest.Require(t, "starts children inside a specifically delegated cgroup v2 subtree")
	controls, err := os.ReadFile(filepath.Join(resolved, "cgroup.subtree_control"))
	if err != nil {
		cpuPlacementUnavailable(t, "read delegated controllers: %v", err)
	}
	for _, name := range []string{"cpu", "cpuset"} {
		if !slices.Contains(strings.Fields(string(controls)), name) {
			cpuPlacementUnavailable(t, "%s must already be delegated below %q by the test operator", name, resolved)
		}
	}
	var affinity unix.CPUSet
	if err := unix.SchedGetaffinity(0, &affinity); err != nil {
		cpuPlacementUnavailable(t, "read test affinity: %v", err)
	}
	var ids []int
	for i := 0; i < 1024; i++ {
		if affinity.IsSet(i) {
			ids = append(ids, i)
		}
	}
	allowed, err := cpuplacement.NewSet(ids...)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := readCPUBoundary(resolved, "/sys/devices/system/cpu/online", allowed)
	if err != nil {
		cpuPlacementUnavailable(t, "read delegated effective boundary: %v", err)
	}
	if boundary.CPUs.Len() < 2 {
		cpuPlacementUnavailable(t, "need at least two effective, online, affinity-allowed logical CPUs to verify exclusion; have %s", boundary.CPUs)
	}
	testRoot, err := os.MkdirTemp(resolved, "substrate-cpu-placement-")
	if err != nil {
		cpuPlacementUnavailable(t, "create disposable delegated test cgroup: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(testRoot); err != nil {
			t.Errorf("remove disposable cgroup %s: %v", testRoot, err)
		}
	})
	// Writes are confined to the newly created empty test cgroup.
	for _, field := range []struct{ name, value string }{
		{"cpuset.mems", boundary.Mems.String()}, {"cpuset.cpus", boundary.CPUs.String()},
		{"cgroup.subtree_control", "+cpu +cpuset"},
	} {
		if err := writeExisting(filepath.Join(testRoot, field.name), field.value); err != nil {
			cpuPlacementUnavailable(t, "prepare disposable test subtree %s: %v", field.name, err)
		}
	}
	return testRoot, boundary
}

type placementThreadReport struct {
	TID      int    `json:"tid"`
	CPUs     string `json:"cpus"`
	Mems     string `json:"mems"`
	Checksum uint64 `json:"checksum"`
	Error    string `json:"error,omitempty"`
}

type placementWorkloadReport struct {
	CPUs    string                  `json:"cpus"`
	Mems    string                  `json:"mems"`
	Threads []placementThreadReport `json:"threads"`
}

func placementStatus(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var cpus, mems string
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "Cpus_allowed_list:"); ok {
			cpus = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "Mems_allowed_list:"); ok {
			mems = strings.TrimSpace(value)
		}
	}
	if cpus == "" || mems == "" {
		return "", "", fmt.Errorf("missing resource masks in %s", path)
	}
	return cpus, mems, nil
}

// TestCPUPlacementWorkloadHelper is executed in a separate copy of the test
// binary. Go runtime startup and later-created OS threads all inherit the
// clone-into-cgroup boundary. Pipes keep it alive for external /proc checks.
func TestCPUPlacementWorkloadHelper(t *testing.T) {
	if os.Getenv("SUBSTRATE_CPU_PLACEMENT_HELPER") != "1" {
		return
	}
	cpus, mems, err := placementStatus("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	results := make(chan placementThreadReport, 4)
	for worker := range 4 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			tid := unix.Gettid()
			cpus, mems, err := placementStatus(fmt.Sprintf("/proc/self/task/%d/status", tid))
			report := placementThreadReport{TID: tid, CPUs: cpus, Mems: mems}
			if err != nil {
				report.Error = err.Error()
			}
			for n := uint64(1); n <= 100000; n++ {
				report.Checksum += n + uint64(worker)
			}
			results <- report
			<-stop
		}(worker)
	}
	report := placementWorkloadReport{CPUs: cpus, Mems: mems}
	for range 4 {
		report.Threads = append(report.Threads, <-results)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || line != "exit\n" {
		t.Errorf("parent completion handshake = %q, %v", line, err)
	}
	close(stop)
	wg.Wait()
}

func TestCPUPlacementKernelEnforcement(t *testing.T) {
	root, boundary := delegatedPlacementRoot(t)
	const owner = "actor-placement-test"
	const milliCPU = 500
	// Exclude at least one currently usable CPU to prove an actual boundary,
	// while preserving the parent's quota and all global CPU policy.
	cpus := placementSet(t, strconv.Itoa(boundary.CPUs.IDs()[0]))
	mems := boundary.Mems
	leaf, err := openPlacedActorLeaf(root, owner, milliCPU, cpus, mems)
	if err != nil {
		t.Fatalf("configure pre-exec placement: %v", err)
	}
	t.Cleanup(func() { _ = leaf.Close() })
	path := filepath.Join(root, owner)
	t.Cleanup(func() {
		if err := removeActorLeaf(root, owner); err != nil {
			t.Errorf("remove stopped Actor cgroup: %v", err)
		}
	})
	for name, want := range map[string]string{"cpu.max": "50000 100000", "cpuset.cpus.effective": cpus.String(), "cpuset.mems.effective": mems.String()} {
		if got := placementRead(t, filepath.Join(path, name)); got != want {
			t.Fatalf("pre-exec %s = %q, want %q", name, got, want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCPUPlacementWorkloadHelper$", "-test.timeout=25s")
	cmd.Env = append(os.Environ(), "SUBSTRATE_CPU_PLACEMENT_HELPER=1")
	cmd.SysProcAttr = leaf.SysProcAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("clone workload into configured cgroup: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if err := leaf.Close(); err != nil {
		t.Fatal(err)
	}
	var report placementWorkloadReport
	if err := json.NewDecoder(stdout).Decode(&report); err != nil {
		t.Fatalf("read bounded workload result: %v", err)
	}
	if report.CPUs != cpus.String() || report.Mems != mems.String() {
		t.Fatalf("initial process placement = CPUs %s / memory %s, want %s / %s", report.CPUs, report.Mems, cpus, mems)
	}
	if len(report.Threads) != 4 {
		t.Fatalf("got %d spawned threads, want 4", len(report.Threads))
	}
	seen := make(map[int]bool)
	var checksum uint64
	for _, thread := range report.Threads {
		if seen[thread.TID] {
			t.Errorf("OS thread %d was reused before all spawned threads were alive", thread.TID)
		}
		seen[thread.TID] = true
		if thread.Error != "" || thread.CPUs != cpus.String() || thread.Mems != mems.String() {
			t.Errorf("spawned thread escaped boundary: %+v", thread)
		}
		checksum += thread.Checksum
	}
	const wantChecksum = 4*(100000*100001/2) + 100000*(0+1+2+3)
	if checksum != wantChecksum {
		t.Errorf("workload checksum = %d, want %d", checksum, wantChecksum)
	}
	procRoot := filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid))
	threads, err := os.ReadDir(filepath.Join(procRoot, "task"))
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) < len(report.Threads) {
		t.Fatalf("only %d live OS threads", len(threads))
	}
	for _, thread := range threads {
		actualCPUs, actualMems, err := placementStatus(filepath.Join(procRoot, "task", thread.Name(), "status"))
		if err != nil {
			t.Fatal(err)
		}
		if actualCPUs != cpus.String() || actualMems != mems.String() {
			t.Errorf("/proc task %s escaped: CPUs %s, memory %s", thread.Name(), actualCPUs, actualMems)
		}
	}
	procs := strings.Fields(placementRead(t, filepath.Join(path, "cgroup.procs")))
	if !slices.Contains(procs, strconv.Itoa(cmd.Process.Pid)) {
		t.Errorf("workload PID missing from leaf cgroup.procs: %v", procs)
	}
	if empty, err := leafEmpty(path); err != nil || empty {
		t.Fatalf("running workload lost persistent cgroup ownership: empty=%t, error=%v", empty, err)
	}
	states, err := actorLeafStates(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Owner != owner || !states[0].Populated || !states[0].CPUs.Equal(cpus) || !states[0].Mems.Equal(mems) {
		t.Fatalf("live kernel reconstruction = %+v", states)
	}
	// Recreate only the allocator, as after a Worker restart. A recycled PID
	// cannot affect this record, and the occupied set stays unavailable.
	recovered, err := cpuplacement.New(cpuplacement.Options{Allowed: cpus, Policy: cpuplacement.PolicyNonOverlapping})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := recovered.Recover(states[0].Owner, states[0].CPUs, states[0].Mems)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Acquire("other-actor", 1, cpuplacement.Set{}); !errors.Is(err, cpuplacement.ErrCapacity) {
		t.Errorf("live lease reused after reconstruction: %v", err)
	}
	refused, err := openPlacedActorLeaf(root, owner, 1000, cpus, mems)
	if refused != nil {
		_ = refused.Close()
	}
	if err == nil {
		t.Fatal("live cgroup reconfiguration accepted")
	}
	if got := placementRead(t, filepath.Join(path, "cpu.max")); got != "50000 100000" {
		t.Errorf("refused reconfiguration changed quota to %q", got)
	}
	if _, err := io.WriteString(stdin, "exit\n"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("workload failed: %v", err)
	}
	waited = true
	if empty, err := leafEmpty(path); err != nil || !empty {
		t.Fatalf("cannot prove workload exit: empty=%t error=%v", empty, err)
	}
	if err := removeActorLeaf(root, owner); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Release(lease); err != nil {
		t.Fatal(err)
	}
	reused, err := recovered.Acquire("replacement-actor", 1, cpuplacement.Set{})
	if err != nil || !reused.CPUs.Equal(cpus) {
		t.Fatalf("stopped and removed cgroup did not release CPUs: %+v %v", reused, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Actor cgroup was not removed: %v", err)
	}
	t.Logf("verified kernel cgroup %s; logical CPUs=%s memory nodes=%s quota=%dm; child PID=%d; spawned workload threads=%d; this is correctness, not performance evidence", root, cpus, mems, milliCPU, cmd.Process.Pid, len(report.Threads))
}
