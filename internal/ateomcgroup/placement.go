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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/agent-substrate/substrate/internal/cpuplacement"
	"golang.org/x/sys/unix"
)

// RequireActorCPUControllers verifies child delegation separately from the
// parent's effective files: a parent can expose a controller without enabling
// it for descendants. This check never enables or changes a controller.
func RequireActorCPUControllers() error { return requireActorCPUControllers(Root) }

func requireActorCPUControllers(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "cgroup.subtree_control"))
	if err != nil {
		return fmt.Errorf("read Actor controller delegation: %w", err)
	}
	enabled := " " + strings.Join(strings.Fields(string(data)), " ") + " "
	for _, controller := range []string{"cpu", "cpuset"} {
		if !strings.Contains(enabled, " "+controller+" ") {
			return fmt.Errorf("%s controller is not enabled for Actor descendants", controller)
		}
	}
	return nil
}

// CPUBoundary describes the delegated Worker, not a node-wide allocation.
type CPUBoundary struct {
	CPUs          cpuplacement.Set
	Mems          cpuplacement.Set
	QuotaMilliCPU int64
	QuotaFinite   bool
	// Namespace ancestors are not visible from a private cgroup namespace.
	AncestorQuotaHidden bool
}

// ReadCPUBoundary intersects the real delegated cpuset, online CPUs and the
// process affinity. It never synthesizes CPU IDs from a processor count.
func ReadCPUBoundary() (CPUBoundary, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(Root, &stat); err != nil {
		return CPUBoundary{}, err
	}
	if stat.Type != unix.CGROUP2_SUPER_MAGIC {
		return CPUBoundary{}, fmt.Errorf("%s is not a cgroup v2 mount", Root)
	}
	var affinity unix.CPUSet
	if err := unix.SchedGetaffinity(0, &affinity); err != nil {
		return CPUBoundary{}, fmt.Errorf("read process affinity: %w", err)
	}
	var ids []int
	for i := 0; i < 1024; i++ {
		if affinity.IsSet(i) {
			ids = append(ids, i)
		}
	}
	allowed, err := cpuplacement.NewSet(ids...)
	if err != nil {
		return CPUBoundary{}, err
	}
	return readCPUBoundary(Root, "/sys/devices/system/cpu/online", allowed)
}

func readCPUBoundary(root, onlinePath string, affinity cpuplacement.Set) (CPUBoundary, error) {
	readSet := func(path string) (cpuplacement.Set, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return cpuplacement.Set{}, err
		}
		return cpuplacement.ParseSet(strings.TrimSpace(string(data)))
	}
	cpus, err := readSet(filepath.Join(root, "cpuset.cpus.effective"))
	if err != nil {
		return CPUBoundary{}, fmt.Errorf("read effective CPUs: %w", err)
	}
	mems, err := readSet(filepath.Join(root, "cpuset.mems.effective"))
	if err != nil {
		return CPUBoundary{}, fmt.Errorf("read effective memory nodes: %w", err)
	}
	online, err := readSet(onlinePath)
	if err != nil {
		return CPUBoundary{}, fmt.Errorf("read online CPUs: %w", err)
	}
	cpus = cpus.Intersection(online).Intersection(affinity)
	if cpus.Len() == 0 || mems.Len() == 0 {
		return CPUBoundary{}, fmt.Errorf("empty effective CPU or memory-node boundary")
	}
	data, err := os.ReadFile(filepath.Join(root, "cpu.max"))
	if err != nil {
		return CPUBoundary{}, fmt.Errorf("read parent CPU quota: %w", err)
	}
	milli, finite, err := parseQuota(string(data))
	if err != nil {
		return CPUBoundary{}, err
	}
	return CPUBoundary{CPUs: cpus, Mems: mems, QuotaMilliCPU: milli, QuotaFinite: finite, AncestorQuotaHidden: true}, nil
}

func parseQuota(value string) (int64, bool, error) {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return 0, false, fmt.Errorf("invalid cpu.max %q", value)
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || period <= 0 {
		return 0, false, fmt.Errorf("invalid CPU quota period %q", fields[1])
	}
	if fields[0] == "max" {
		return 0, false, nil
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || quota <= 0 {
		return 0, false, fmt.Errorf("invalid CPU quota %q", fields[0])
	}
	// Reject malformed values that cannot be represented in millicores.
	if quota > (1<<63-1)/1000 {
		return 0, false, fmt.Errorf("CPU quota overflow")
	}
	return quota * 1000 / period, true, nil
}

// OpenPlacedActorLeaf sets the entire Actor's CPU boundary before clone into
// the cgroup. No new or recreated VMM/backend thread can escape that boundary.
// Unlike the legacy path, it requires the cpu and cpuset controllers.
func OpenPlacedActorLeaf(actorUID string, milliCPU int64, cpus, mems cpuplacement.Set) (*ActorLeaf, error) {
	return openPlacedActorLeaf(Root, actorUID, milliCPU, cpus, mems)
}
func openPlacedActorLeaf(root, actorUID string, milliCPU int64, cpus, mems cpuplacement.Set) (*ActorLeaf, error) {
	if cpus.Len() == 0 || mems.Len() == 0 {
		return nil, fmt.Errorf("placement requires nonempty CPU and memory-node sets")
	}
	if milliCPU < 0 || milliCPU > (1<<63-1)/cpuPeriodUS {
		return nil, fmt.Errorf("actor CPU quota out of range")
	}
	path, err := actorLeafPath(root, actorUID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		empty, err := leafEmpty(path)
		if err != nil {
			return nil, fmt.Errorf("refusing to change unreadable Actor cgroup %s: %w", actorUID, err)
		}
		if !empty {
			return nil, fmt.Errorf("refusing to change populated Actor cgroup %s", actorUID)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	leaf, err := openActorLeaf(root, actorUID, milliCPU)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*ActorLeaf, error) { _ = leaf.Close(); return nil, err }
	// A bandwidth limit is never lifted to make restore faster.
	if err := writeExisting(filepath.Join(path, "cpu.max"), cpuMax(milliCPU)); err != nil {
		return fail(fmt.Errorf("require Actor CPU controller: %w", err))
	}
	quota, err := os.ReadFile(filepath.Join(path, "cpu.max"))
	if err != nil {
		return fail(err)
	}
	if strings.Join(strings.Fields(string(quota)), " ") != cpuMax(milliCPU) {
		return fail(fmt.Errorf("actor CPU quota readback differs from requested budget"))
	}
	for _, field := range []struct{ name, value string }{{"cpuset.mems", mems.String()}, {"cpuset.cpus", cpus.String()}} {
		if err := writeExisting(filepath.Join(path, field.name), field.value); err != nil {
			return fail(fmt.Errorf("apply %s: %w", field.name, err))
		}
	}
	for _, field := range []struct {
		name string
		want cpuplacement.Set
	}{{"cpuset.mems.effective", mems}, {"cpuset.cpus.effective", cpus}} {
		data, err := os.ReadFile(filepath.Join(path, field.name))
		if err != nil {
			return fail(err)
		}
		actual, err := cpuplacement.ParseSet(strings.TrimSpace(string(data)))
		if err != nil {
			return fail(err)
		}
		if !actual.Equal(field.want) {
			return fail(fmt.Errorf("%s changed while placing Actor: requested %s, effective %s", field.name, field.want.String(), actual.String()))
		}
	}
	return leaf, nil
}

// SetManagementWeight only changes the existing ateom leaf's relative share.
// It makes no CPU-time guarantee under a throttled ancestor.
func SetManagementWeight(weight int) error {
	if weight < 1 || weight > 10000 {
		return fmt.Errorf("management CPU weight must be in [1,10000]")
	}
	return writeExisting(filepath.Join(Root, workerLeaf, "cpu.weight"), strconv.Itoa(weight))
}

// ActorLeafState is the kernel's persistent ownership record, independent of
// recycled PIDs and the lifetime of the Worker process.
type ActorLeafState struct {
	Owner      string
	CPUs, Mems cpuplacement.Set
	Populated  bool
}

func ActorLeafStates() ([]ActorLeafState, error) { return actorLeafStates(Root) }
func actorLeafStates(root string) ([]ActorLeafState, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []ActorLeafState
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == workerLeaf {
			continue
		}
		path, err := actorLeafPath(root, entry.Name())
		if err != nil {
			return nil, err
		}
		empty, err := leafEmpty(path)
		if err != nil {
			return nil, err
		}
		state := ActorLeafState{Owner: entry.Name(), Populated: !empty}
		for _, field := range []struct {
			name   string
			target *cpuplacement.Set
		}{{"cpuset.cpus.effective", &state.CPUs}, {"cpuset.mems.effective", &state.Mems}} {
			b, err := os.ReadFile(filepath.Join(path, field.name))
			if err != nil {
				return nil, err
			}
			*field.target, err = cpuplacement.ParseSet(strings.TrimSpace(string(b)))
			if err != nil {
				return nil, err
			}
		}
		out = append(out, state)
	}
	return out, nil
}

func leafEmpty(path string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(path, "cgroup.events"))
	if err != nil {
		return false, err
	}
	return parseLeafEvents(b, path)
}

func parseLeafEvents(b []byte, path string) (bool, error) {
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "populated" {
			if f[1] == "0" {
				return true, nil
			}
			if f[1] == "1" {
				return false, nil
			}
		}
	}
	return false, fmt.Errorf("missing valid populated state in %s", path)
}

// ActorHierarchy identifies one delegated root for a Worker lifetime. External
// managers must not move live Actor tasks outside this hierarchy.
type ActorHierarchy struct {
	path           string
	device         uint64
	inode          uint64
	requireCgroup2 bool
}

// CaptureActorHierarchy captures the root before recovery or Actor execution.
func CaptureActorHierarchy() (*ActorHierarchy, error) { return captureActorHierarchy(Root, true) }
func captureActorHierarchy(path string, requireCgroup2 bool) (*ActorHierarchy, error) {
	root, stat, err := openActorHierarchyRoot(path, requireCgroup2)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return &ActorHierarchy{path: path, device: stat.Dev, inode: stat.Ino, requireCgroup2: requireCgroup2}, nil
}

// Verify refuses admission if the captured delegation disappeared or changed.
func (h *ActorHierarchy) Verify() error {
	root, err := h.open()
	if err != nil {
		return err
	}
	return root.Close()
}

// Empty checks identity and Actor state through the same opened root.
func (h *ActorHierarchy) Empty(actorUID string) (bool, error) {
	root, err := h.open()
	if err != nil {
		return false, err
	}
	defer root.Close()
	return actorLeafEmptyUnder(root, actorUID)
}
func (h *ActorHierarchy) open() (*os.Root, error) {
	if h == nil {
		return nil, fmt.Errorf("actor hierarchy identity was not captured")
	}
	root, stat, err := openActorHierarchyRoot(h.path, h.requireCgroup2)
	if err != nil {
		return nil, err
	}
	if stat.Dev != h.device || stat.Ino != h.inode {
		_ = root.Close()
		return nil, fmt.Errorf("delegated Actor hierarchy changed; execution ownership cannot be proven")
	}
	return root, nil
}
func openActorHierarchyRoot(path string, requireCgroup2 bool) (*os.Root, unix.Stat_t, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, unix.Stat_t{}, fmt.Errorf("open delegated cgroup root: %w", err)
	}
	fail := func(err error) (*os.Root, unix.Stat_t, error) { _ = root.Close(); return nil, unix.Stat_t{}, err }
	dir, err := root.Open(".")
	if err != nil {
		return fail(err)
	}
	defer dir.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &stat); err != nil {
		return fail(err)
	}
	if requireCgroup2 {
		var fsStat unix.Statfs_t
		if err := unix.Fstatfs(int(dir.Fd()), &fsStat); err != nil {
			return fail(err)
		}
		if fsStat.Type != unix.CGROUP2_SUPER_MAGIC {
			return fail(fmt.Errorf("opened delegated root is not a cgroup v2 filesystem"))
		}
	}
	if _, err := root.ReadFile("cgroup.events"); err != nil {
		return fail(fmt.Errorf("read delegated root state: %w", err))
	}
	return root, stat, nil
}

// ActorLeafEmpty checks the current hierarchy. Managed lifecycles must use a
// captured ActorHierarchy so a replacement cannot hide their live execution.
func ActorLeafEmpty(actorUID string) (bool, error) { return actorLeafEmptyAt(Root, actorUID, true) }
func actorLeafEmptyAt(path, actorUID string, requireCgroup2 bool) (bool, error) {
	root, _, err := openActorHierarchyRoot(path, requireCgroup2)
	if err != nil {
		return false, err
	}
	defer root.Close()
	return actorLeafEmptyUnder(root, actorUID)
}
func actorLeafEmptyUnder(root *os.Root, actorUID string) (bool, error) {
	if _, err := actorLeafPath(".", actorUID); err != nil {
		return false, err
	}
	if _, err := root.ReadFile("cgroup.events"); err != nil {
		return false, fmt.Errorf("read delegated root state: %w", err)
	}
	data, err := root.ReadFile(filepath.Join(actorUID, "cgroup.events"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		if _, statErr := root.Stat(actorUID); errors.Is(statErr, fs.ErrNotExist) {
			return true, nil
		} else if statErr != nil {
			return false, statErr
		}
		return false, err
	}
	return parseLeafEvents(data, actorUID)
}

// PopulatedActorOwners recovers occupancy without requiring CPU controllers.
func PopulatedActorOwners() ([]string, error) {
	root, _, err := openActorHierarchyRoot(Root, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return populatedActorOwnersUnder(root)
}
func populatedActorOwnersUnder(root *os.Root) ([]string, error) {
	if _, err := root.ReadFile("cgroup.events"); err != nil {
		return nil, err
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var owners []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == workerLeaf {
			continue
		}
		empty, err := actorLeafEmptyUnder(root, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read Actor %s ownership: %w", entry.Name(), err)
		}
		if !empty {
			owners = append(owners, entry.Name())
		}
	}
	slices.Sort(owners)
	return owners, nil
}
