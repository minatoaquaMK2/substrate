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
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-substrate/substrate/internal/cpuplacement"
	"golang.org/x/sys/unix"
)

func placementSet(t *testing.T, value string) cpuplacement.Set {
	t.Helper()
	set, err := cpuplacement.ParseSet(value)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// These are regular files modeling cgroupfs interfaces, not a kernel cgroup or
// a performance experiment. Kernel enforcement is tested separately below.
func placementFiles(t *testing.T, dir string, values map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func placementRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func TestReadCPUBoundaryIntersectsActualIDs(t *testing.T) {
	root := t.TempDir()
	placementFiles(t, root, map[string]string{
		"cpuset.cpus.effective": "2,4,9,16,31\n",
		"cpuset.mems.effective": "1,3\n",
		"online":                "0-4,9,31\n",
		"cpu.max":               "125000 100000\n",
	})
	b, err := readCPUBoundary(root, filepath.Join(root, "online"), placementSet(t, "4,16,31,63"))
	if err != nil {
		t.Fatal(err)
	}
	if got := b.CPUs.String(); got != "4,31" {
		t.Errorf("CPUs = %s; want parent ∩ online ∩ affinity = 4,31", got)
	}
	if got := b.Mems.String(); got != "1,3" {
		t.Errorf("Mems = %s, want actual noncontiguous parent nodes 1,3", got)
	}
	if !b.QuotaFinite || b.QuotaMilliCPU != 1250 {
		t.Errorf("quota = (%d, finite %t), want (1250, true)", b.QuotaMilliCPU, b.QuotaFinite)
	}
	if !b.AncestorQuotaHidden {
		t.Error("a private namespace cannot establish that invisible ancestors have unlimited bandwidth")
	}
}

func TestReadCPUBoundaryUnlimitedDoesNotClaimUnthrottledAncestors(t *testing.T) {
	root := t.TempDir()
	placementFiles(t, root, map[string]string{
		"cpuset.cpus.effective": "7", "cpuset.mems.effective": "2",
		"online": "7", "cpu.max": "max 100000",
	})
	b, err := readCPUBoundary(root, filepath.Join(root, "online"), placementSet(t, "7"))
	if err != nil {
		t.Fatal(err)
	}
	if b.QuotaFinite || b.QuotaMilliCPU != 0 || !b.AncestorQuotaHidden {
		t.Errorf("unlimited visible parent must retain unknown ancestor status: %+v", b)
	}
}

func TestReadCPUBoundaryFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, field, value, affinity string
		missing                      bool
	}{
		{name: "missing delegated cpuset", field: "cpuset.cpus.effective", missing: true},
		{name: "missing memory controller", field: "cpuset.mems.effective", missing: true},
		{name: "missing CPU controller", field: "cpu.max", missing: true},
		{name: "missing online CPUs", field: "online", missing: true},
		{name: "invalid CPU IDs", field: "cpuset.cpus.effective", value: "2-z"},
		{name: "invalid memory IDs", field: "cpuset.mems.effective", value: "-1"},
		{name: "empty memory nodes", field: "cpuset.mems.effective", value: ""},
		{name: "empty CPUs", field: "cpuset.cpus.effective", value: ""},
		{name: "offline delegated CPUs", field: "online", value: "6-7"},
		{name: "invalid online CPUs", field: "online", value: "1,,3"},
		{name: "affinity excludes delegated CPUs", affinity: "5-7"},
		{name: "invalid parent quota", field: "cpu.max", value: "1000 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			placementFiles(t, root, map[string]string{
				"cpuset.cpus.effective": "1-3", "cpuset.mems.effective": "0",
				"online": "1-3", "cpu.max": "max 100000",
			})
			if tc.field != "" {
				if tc.missing {
					if err := os.Remove(filepath.Join(root, tc.field)); err != nil {
						t.Fatal(err)
					}
				} else {
					placementFiles(t, root, map[string]string{tc.field: tc.value})
				}
			}
			affinity := tc.affinity
			if affinity == "" {
				affinity = "1-3"
			}
			if _, err := readCPUBoundary(root, filepath.Join(root, "online"), placementSet(t, affinity)); err == nil {
				t.Fatal("boundary accepted without a complete nonempty resource intersection")
			}
		})
	}
}

func TestParseQuota(t *testing.T) {
	for _, tc := range []struct {
		value  string
		milli  int64
		finite bool
	}{
		{"max 100000", 0, false}, {" max\t100000\n", 0, false},
		{"50000 100000", 500, true}, {"250000 100000", 2500, true},
		{"1 3", 333, true}, {"1 1000000", 0, true},
		{"9223372036854775 1", 9223372036854775000, true},
		{"9223372036854775 9223372036854775807", 0, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			milli, finite, err := parseQuota(tc.value)
			if err != nil || milli != tc.milli || finite != tc.finite {
				t.Fatalf("parseQuota(%q) = (%d, %t, %v), want (%d, %t, nil)", tc.value, milli, finite, err, tc.milli, tc.finite)
			}
		})
	}
}

func TestParseQuotaRejectsMalformedOrOverflow(t *testing.T) {
	for _, value := range []string{"", "max", "max 0", "max -1", "max x", "max 100000 extra", "0 100000", "-1 100000", "x 100000", "1 0", "1 -1", "9223372036854775808 100000", "1 9223372036854775808", "9223372036854775807 1", "9223372036854775806 9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			if _, _, err := parseQuota(value); err == nil {
				t.Errorf("parseQuota(%q) accepted invalid or unrepresentable quota", value)
			}
		})
	}
}

func FuzzParseQuota(f *testing.F) {
	for _, seed := range [][2]int64{{50000, 100000}, {1, 3}, {1, 1000000}, {9223372036854775806, 9223372036854775807}, {9223372036854775807, 1}, {9223372036854775, 1}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, quota, period int64) {
		value := strconv.FormatInt(quota, 10) + " " + strconv.FormatInt(period, 10)
		got, finite, err := parseQuota(value)
		if quota <= 0 || period <= 0 || quota > (1<<63-1)/1000 {
			if err == nil {
				t.Fatalf("nonpositive or unsupported quota/period accepted: %q", value)
			}
			return
		}
		want := new(big.Int).Mul(big.NewInt(quota), big.NewInt(1000))
		want.Quo(want, big.NewInt(period))
		if !want.IsInt64() {
			if err == nil {
				t.Fatalf("overflow accepted: %q = %d", value, got)
			}
			return
		}
		if err != nil || !finite || got != want.Int64() {
			t.Fatalf("parseQuota(%q) = (%d, %t, %v), want (%s, true, nil)", value, got, finite, err, want)
		}
	})
}

func placedLeafFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "actor-a")
	placementFiles(t, path, map[string]string{
		"cgroup.events": "populated 0\nfrozen 0\n", "cpu.max": "max 100000",
		"cpuset.cpus": "0-63", "cpuset.mems": "0-3",
		"cpuset.cpus.effective": "4,9", "cpuset.mems.effective": "1,3",
	})
	return root, path
}

func TestOpenPlacedActorLeafAppliesQuotaAndSetsBeforeExecution(t *testing.T) {
	root, path := placedLeafFixture(t)
	leaf, err := openPlacedActorLeaf(root, "actor-a", 500, placementSet(t, "4,9"), placementSet(t, "1,3"))
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Close()
	for file, want := range map[string]string{"cpu.max": "50000 100000", "cpuset.cpus": "4,9", "cpuset.mems": "1,3"} {
		if got := placementRead(t, filepath.Join(path, file)); got != want {
			t.Errorf("%s = %q, want %q before a process receives the leaf descriptor", file, got, want)
		}
	}
	attr := leaf.SysProcAttr()
	if attr == nil || !attr.UseCgroupFD {
		t.Fatal("pre-exec clone cgroup descriptor is missing")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(attr.CgroupFD, &stat); err != nil {
		t.Fatalf("cgroup descriptor is not live: %v", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		t.Fatal("clone descriptor is not a directory")
	}
}

func TestOpenPlacedActorLeafRefusesPopulatedOrUnknownWithoutMutation(t *testing.T) {
	for _, events := range []string{"populated 1\nfrozen 0\n", "frozen 0\n", "populated garbage", "populated 2", ""} {
		t.Run(events, func(t *testing.T) {
			root, path := placedLeafFixture(t)
			placementFiles(t, path, map[string]string{"cgroup.events": events})
			leaf, err := openPlacedActorLeaf(root, "actor-a", 500, placementSet(t, "4,9"), placementSet(t, "1,3"))
			if leaf != nil {
				_ = leaf.Close()
				t.Error("refused leaf must not return an execution descriptor")
			}
			if err == nil {
				t.Fatal("unknown/live ownership accepted")
			}
			for name, want := range map[string]string{"cpu.max": "max 100000", "cpuset.cpus": "0-63", "cpuset.mems": "0-3"} {
				if got := placementRead(t, filepath.Join(path, name)); got != want {
					t.Errorf("refused placement changed %s to %q", name, got)
				}
			}
		})
	}
}

func TestOpenPlacedActorLeafRequiresControllersAndReadback(t *testing.T) {
	for _, tc := range []struct {
		name, field, value string
		remove             bool
	}{
		{name: "cpu controller not delegated", field: "cpu.max", remove: true},
		{name: "CPU cpuset not delegated", field: "cpuset.cpus", remove: true},
		{name: "memory cpuset not delegated", field: "cpuset.mems", remove: true},
		{name: "missing effective CPUs", field: "cpuset.cpus.effective", remove: true},
		{name: "missing effective nodes", field: "cpuset.mems.effective", remove: true},
		{name: "CPU parent shrank", field: "cpuset.cpus.effective", value: "4"},
		{name: "memory parent shrank", field: "cpuset.mems.effective", value: "1"},
		{name: "unexpected extra CPU", field: "cpuset.cpus.effective", value: "4,9,15"},
		{name: "invalid effective CPUs", field: "cpuset.cpus.effective", value: "bad"},
		{name: "invalid effective nodes", field: "cpuset.mems.effective", value: "bad"},
		{name: "missing population state", field: "cgroup.events", remove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := placedLeafFixture(t)
			if tc.remove {
				if err := os.Remove(filepath.Join(path, tc.field)); err != nil {
					t.Fatal(err)
				}
			} else {
				placementFiles(t, path, map[string]string{tc.field: tc.value})
			}
			leaf, err := openPlacedActorLeaf(root, "actor-a", 500, placementSet(t, "4,9"), placementSet(t, "1,3"))
			if leaf != nil {
				_ = leaf.Close()
				t.Error("failed placement returned a descriptor")
			}
			if err == nil {
				t.Fatal("placement accepted missing controller or failed effective-set readback")
			}
			if tc.remove {
				if _, err := os.Stat(filepath.Join(path, tc.field)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("missing cgroupfs interface was synthesized: %v", err)
				}
			}
		})
	}
}

func TestOpenPlacedActorLeafValidatesBeforeCreating(t *testing.T) {
	for _, tc := range []struct{ name, cpus, mems string }{
		{"actor", "", "1"}, {"actor", "4", ""}, {"../escape", "4", "1"}, {workerLeaf, "4", "1"},
	} {
		root := t.TempDir()
		leaf, err := openPlacedActorLeaf(root, tc.name, 500, placementSet(t, tc.cpus), placementSet(t, tc.mems))
		if leaf != nil {
			_ = leaf.Close()
			t.Error("invalid request returned a descriptor")
		}
		if err == nil {
			t.Errorf("invalid request %+v accepted", tc)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("invalid request mutated root: %v", entries)
		}
	}
}

func TestLeafEmptyRequiresKernelPopulationProof(t *testing.T) {
	for _, tc := range []struct {
		value      string
		empty, bad bool
	}{
		{"populated 0\nfrozen 0\n", true, false}, {"frozen 0\npopulated 1\n", false, false},
		{"populated 10", false, true}, {"populated 0 trailing", false, true}, {"frozen 0", false, true},
	} {
		root := t.TempDir()
		placementFiles(t, root, map[string]string{"cgroup.events": tc.value})
		empty, err := leafEmpty(root)
		if (err != nil) != tc.bad || empty != tc.empty {
			t.Errorf("leafEmpty(%q) = (%t,%v)", tc.value, empty, err)
		}
	}
	if empty, err := leafEmpty(t.TempDir()); err == nil || empty {
		t.Errorf("missing cgroup.events is not proof of exit: (%t,%v)", empty, err)
	}
}

func TestActorLeafStatesReconstructsKernelOwnership(t *testing.T) {
	root := t.TempDir()
	placementFiles(t, filepath.Join(root, "actor-b"), map[string]string{"cgroup.events": "populated 0", "cpuset.cpus.effective": "7", "cpuset.mems.effective": "2"})
	placementFiles(t, filepath.Join(root, "actor-a"), map[string]string{"cgroup.events": "populated 1", "cpuset.cpus.effective": "4,9", "cpuset.mems.effective": "1,3"})
	// The worker is not an Actor; no interfaces need to be read in it. PIDs
	// are deliberately absent: an empty/recycled PID map cannot free CPUs.
	placementFiles(t, filepath.Join(root, workerLeaf), nil)
	placementFiles(t, root, map[string]string{"cgroup.controllers": "cpu cpuset"})
	states, err := actorLeafStates(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("got %d states, want both Actor leaves", len(states))
	}
	for i, want := range []struct {
		owner, cpus, mems string
		populated         bool
	}{{"actor-a", "4,9", "1,3", true}, {"actor-b", "7", "2", false}} {
		got := states[i]
		if got.Owner != want.owner || got.CPUs.String() != want.cpus || got.Mems.String() != want.mems || got.Populated != want.populated {
			t.Errorf("state %d = %+v, want %+v", i, got, want)
		}
	}
	allocator, err := cpuplacement.New(cpuplacement.Options{Allowed: placementSet(t, "4,7,9"), Policy: cpuplacement.PolicyNonOverlapping})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.Populated {
			if _, err := allocator.Recover(state.Owner, state.CPUs, state.Mems); err != nil {
				t.Fatal(err)
			}
		}
	}
	lease, err := allocator.Acquire("actor-c", 1, cpuplacement.Set{})
	if err != nil {
		t.Fatal(err)
	}
	if got := lease.CPUs.String(); got != "7" {
		t.Errorf("reconstructed live Actor CPUs reused: got %s, want 7", got)
	}
	if _, err := allocator.Acquire("actor-d", 1, cpuplacement.Set{}); !errors.Is(err, cpuplacement.ErrCapacity) {
		t.Errorf("reconstructed occupied capacity accepted: %v", err)
	}
}

func TestActorLeafStatesFailsClosedOnUnreadableState(t *testing.T) {
	for _, field := range []string{"cgroup.events", "cpuset.cpus.effective", "cpuset.mems.effective"} {
		for _, remove := range []bool{false, true} {
			t.Run(field+strconv.FormatBool(remove), func(t *testing.T) {
				root, path := placedLeafFixture(t)
				if remove {
					if err := os.Remove(filepath.Join(path, field)); err != nil {
						t.Fatal(err)
					}
				} else {
					placementFiles(t, path, map[string]string{field: "malformed"})
				}
				if states, err := actorLeafStates(root); err == nil {
					t.Fatalf("accepted unreconstructible %s: %+v", field, states)
				}
			})
		}
	}
	if _, err := actorLeafStates(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing delegated root accepted")
	}
}

func TestManagementWeightRejectsInvalidWithoutTouchingCgroups(t *testing.T) {
	for _, weight := range []int{-1, 0, 10001} {
		if err := SetManagementWeight(weight); err == nil {
			t.Errorf("invalid weight %d accepted", weight)
		}
	}
}

func TestOpenPlacedActorLeafRejectsQuotaOutsideSupportedRange(t *testing.T) {
	for _, milli := range []int64{-1, (1<<63-1)/cpuPeriodUS + 1} {
		root, path := placedLeafFixture(t)
		leaf, err := openPlacedActorLeaf(root, "actor-a", milli, placementSet(t, "4,9"), placementSet(t, "1,3"))
		if leaf != nil {
			_ = leaf.Close()
			t.Error("invalid budget returned an execution descriptor")
		}
		if err == nil {
			t.Errorf("invalid budget %d accepted", milli)
		}
		if got := placementRead(t, filepath.Join(path, "cpu.max")); got != "max 100000" {
			t.Errorf("invalid budget changed quota: %q", got)
		}
	}
}

func TestOpenPlacedActorLeafRejectsUnverifiableQuota(t *testing.T) {
	root, path := placedLeafFixture(t)
	if err := os.Remove(filepath.Join(path, "cpu.max")); err != nil {
		t.Fatal(err)
	}
	// A sink models a controller that accepts the write but cannot confirm it.
	// Only the fixture contains this link; no device or global policy changes.
	if err := os.Symlink("/dev/null", filepath.Join(path, "cpu.max")); err != nil {
		t.Fatal(err)
	}
	leaf, err := openPlacedActorLeaf(root, "actor-a", 500, placementSet(t, "4,9"), placementSet(t, "1,3"))
	if leaf != nil {
		_ = leaf.Close()
		t.Error("unverified quota returned an execution descriptor")
	}
	if err == nil || !strings.Contains(err.Error(), "quota readback") {
		t.Fatalf("quota write without readable confirmation accepted: %v", err)
	}
	if got := placementRead(t, filepath.Join(path, "cpuset.cpus")); got != "0-63" {
		t.Errorf("failed quota readback changed CPU set: %q", got)
	}
}

func TestRequireActorCPUControllers(t *testing.T) {
	for _, value := range []string{"cpu cpuset memory", "cpuset cpu", "cpu", "cpuset", "cpuacct cpuset", ""} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		err := requireActorCPUControllers(root)
		want := value == "cpu cpuset memory" || value == "cpuset cpu"
		if (err == nil) != want {
			t.Errorf("controllers %q: err=%v", value, err)
		}
	}
	if err := requireActorCPUControllers(t.TempDir()); err == nil {
		t.Fatal("missing delegation accepted")
	}
}

func TestActorLeafEmptyMissingHierarchyIsNotExitProof(t *testing.T) {
	if empty, err := actorLeafEmptyAt(filepath.Join(t.TempDir(), "missing"), "actor-a", false); err == nil || empty {
		t.Fatalf("missing hierarchy proved exit: empty=%v err=%v", empty, err)
	}
}
func TestActorLeafEmptyMissingRootInterfaceIsNotExitProof(t *testing.T) {
	if empty, err := actorLeafEmptyAt(t.TempDir(), "actor-a", false); err == nil || empty {
		t.Fatalf("unreadable hierarchy proved exit: empty=%v err=%v", empty, err)
	}
}

func TestActorHierarchyReplacementIsNotExitProof(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root")
	placementFiles(t, path, map[string]string{"cgroup.events": "populated 1\n"})
	placementFiles(t, filepath.Join(path, "actor-a"), map[string]string{"cgroup.events": "populated 1\n"})
	h, err := captureActorHierarchy(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(); err != nil {
		t.Fatal(err)
	}
	if empty, err := h.Empty("actor-a"); err != nil || empty {
		t.Fatalf("live Actor: %v %v", empty, err)
	}
	anchored, err := h.open()
	if err != nil {
		t.Fatal(err)
	}
	defer anchored.Close()
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(); err == nil {
		t.Fatal("missing hierarchy accepted")
	}
	placementFiles(t, path, map[string]string{"cgroup.events": "populated 0\n"})
	if err := h.Verify(); err == nil || !strings.Contains(err.Error(), "hierarchy changed") {
		t.Fatalf("replacement admitted: %v", err)
	}
	if empty, err := h.Empty("actor-a"); err == nil || empty {
		t.Fatalf("replacement hid live Actor: %v %v", empty, err)
	}
	if empty, err := actorLeafEmptyUnder(anchored, "actor-a"); err != nil || empty {
		t.Fatalf("anchored lookup followed replacement: %v %v", empty, err)
	}
}

func TestActorHierarchyStableIdentityAndErrors(t *testing.T) {
	path := t.TempDir()
	placementFiles(t, path, map[string]string{"cgroup.events": "populated 0\n"})
	h, err := captureActorHierarchy(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if empty, err := h.Empty("actor-a"); err != nil || !empty {
		t.Fatalf("absent Actor: %v %v", empty, err)
	}
	for _, state := range []string{"0", "1", "invalid"} {
		placementFiles(t, filepath.Join(path, "actor-a"), map[string]string{"cgroup.events": "populated " + state + "\n"})
		empty, err := h.Empty("actor-a")
		if empty != (state == "0") || (err != nil) != (state == "invalid") {
			t.Errorf("state=%s empty=%v err=%v", state, empty, err)
		}
	}
	if err := os.Remove(filepath.Join(path, "actor-a", "cgroup.events")); err != nil {
		t.Fatal(err)
	}
	if empty, err := h.Empty("actor-a"); err == nil || empty {
		t.Fatalf("existing leaf missing events proved exit: %v %v", empty, err)
	}
	for _, owner := range []string{"", "..", "../actor-a", workerLeaf} {
		if empty, err := h.Empty(owner); err == nil || empty {
			t.Fatalf("invalid owner %q: %v %v", owner, empty, err)
		}
	}
	if err := os.Remove(filepath.Join(path, "cgroup.events")); err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(); err == nil {
		t.Fatal("missing root interface accepted")
	}
	if empty, err := h.Empty("actor-b"); err == nil || empty {
		t.Fatalf("unreadable root proved exit: %v %v", empty, err)
	}
	if _, err := captureActorHierarchy(path, false); err == nil {
		t.Fatal("captured missing root interface")
	}
	if err := (*ActorHierarchy)(nil).Verify(); err == nil {
		t.Fatal("nil hierarchy accepted")
	}
	if empty, err := (*ActorHierarchy)(nil).Empty("actor-a"); err == nil || empty {
		t.Fatalf("nil hierarchy: %v %v", empty, err)
	}
}

func TestActorHierarchyCaptureRequiresCgroupFilesystem(t *testing.T) {
	path := t.TempDir()
	placementFiles(t, path, map[string]string{"cgroup.events": "populated 0\n"})
	if _, err := captureActorHierarchy(path, true); err == nil {
		t.Fatal("ordinary filesystem accepted as cgroup")
	}
}

func TestPopulatedActorOwnersNeedsNoCPUControllers(t *testing.T) {
	path := t.TempDir()
	placementFiles(t, path, map[string]string{"cgroup.events": "populated 1\n"})
	for name, state := range map[string]string{"actor-b": "1", "actor-a": "1", "actor-c": "0", workerLeaf: "invalid"} {
		placementFiles(t, filepath.Join(path, name), map[string]string{"cgroup.events": "populated " + state + "\n"})
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	owners, err := populatedActorOwnersUnder(root)
	if err != nil || strings.Join(owners, ",") != "actor-a,actor-b" {
		t.Fatalf("owners=%v err=%v", owners, err)
	}
}

func TestPopulatedActorOwnersFailsClosed(t *testing.T) {
	for _, state := range []string{"missing", "malformed", "missing-root-interface"} {
		t.Run(state, func(t *testing.T) {
			path := t.TempDir()
			placementFiles(t, path, map[string]string{"cgroup.events": "populated 1\n"})
			placementFiles(t, filepath.Join(path, "actor-a"), map[string]string{"cgroup.events": "populated 1\n"})
			switch state {
			case "missing":
				if err := os.Remove(filepath.Join(path, "actor-a", "cgroup.events")); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				placementFiles(t, filepath.Join(path, "actor-a"), map[string]string{"cgroup.events": "populated maybe\n"})
			case "missing-root-interface":
				if err := os.Remove(filepath.Join(path, "cgroup.events")); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if owners, err := populatedActorOwnersUnder(root); err == nil {
				t.Fatalf("uncertain owners accepted: %v", owners)
			}
		})
	}
}
