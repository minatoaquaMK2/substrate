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

package cpuplacement

import (
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
)

// CPU describes one visible logical processor. Unknown IDs are -1; unknown
// siblings and LLC identity are empty. Siblings are filtered to allowed online
// CPUs, so a visible sibling set need not be a complete physical core.
type CPU struct {
	ID, Socket, Core, NUMA int
	Siblings               Set
	LLC                    string
}

// Topology is a snapshot of kernel-visible topology, which may be virtual.
// Source is provenance, never evidence of physical host exclusivity. The zero
// value means unknown topology; it remains usable with PlacementAny.
type Topology struct {
	Online   Set
	CPUs     map[int]CPU
	Source   string
	Warnings []string
}

// Discover reads an fs.FS rooted at /sys/devices/system (for example,
// os.DirFS("/sys/devices/system")). CPUs and siblings are filtered to the
// intersection of online CPUs and allowed. Online retains the full online
// snapshot. A missing/invalid online list fails closed; absent optional topology
// becomes unknown with a warning. No physical-host topology is inferred.
func Discover(fsys fs.FS, allowed Set) (Topology, error) {
	online, err := readSet(fsys, "cpu/online")
	if err != nil || online.Len() == 0 {
		return Topology{}, fmt.Errorf("read online CPUs: %w", nonemptyError(err))
	}
	t := Topology{Online: online, CPUs: make(map[int]CPU), Source: "linux-sysfs-visible"}
	visible := online.Intersection(allowed)
	nodes := discoverNodes(fsys, &t)
	for _, id := range visible.ids {
		base := fmt.Sprintf("cpu/cpu%d/", id)
		cpu := CPU{ID: id, Socket: -1, Core: -1, NUMA: -1}
		cpu.Socket = topologyID(fsys, base+"topology/physical_package_id", &t)
		cpu.Core = topologyID(fsys, base+"topology/core_id", &t)
		if siblings, err := readSet(fsys, base+"topology/thread_siblings_list"); err == nil && siblings.Contains(id) {
			cpu.Siblings = siblings.Intersection(visible)
		} else {
			t.Warnings = append(t.Warnings, fmt.Sprintf("CPU %d: SMT siblings unknown", id))
		}
		if node, ok := nodes[id]; ok {
			cpu.NUMA = node
		}
		cpu.LLC = discoverLLC(fsys, base, id, &t)
		t.CPUs[id] = cpu
	}
	// Do not publish a topology assembled across an observed online-set change.
	after, err := readSet(fsys, "cpu/online")
	if err != nil || !after.Equal(online) {
		return Topology{}, fmt.Errorf("online CPUs changed during topology discovery; retry")
	}
	slices.Sort(t.Warnings)
	return t, nil
}

func readSet(fsys fs.FS, name string) (Set, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Set{}, err
	}
	return ParseSet(string(data))
}

func nonemptyError(err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("empty set")
}

func topologyID(fsys fs.FS, path string, t *Topology) int {
	data, err := fs.ReadFile(fsys, path)
	if err == nil {
		if id, err := parseID(strings.TrimSpace(string(data))); err == nil {
			return id
		}
	}
	t.Warnings = append(t.Warnings, path+": unknown")
	return -1
}

func discoverNodes(fsys fs.FS, t *Topology) map[int]int {
	assignments := make(map[int]int)
	entries, err := fs.ReadDir(fsys, "node")
	if err != nil {
		t.Warnings = append(t.Warnings, "NUMA topology unknown")
		return assignments
	}
	for _, entry := range entries {
		id, err := parseID(strings.TrimPrefix(entry.Name(), "node"))
		if !strings.HasPrefix(entry.Name(), "node") || err != nil {
			continue
		}
		cpus, err := readSet(fsys, "node/"+entry.Name()+"/cpulist")
		if err != nil {
			t.Warnings = append(t.Warnings, entry.Name()+": CPU membership unknown")
			continue
		}
		for _, cpu := range cpus.Intersection(t.Online).ids {
			if previous, ok := assignments[cpu]; ok && previous != id {
				assignments[cpu] = -1
				t.Warnings = append(t.Warnings, fmt.Sprintf("CPU %d: conflicting NUMA memberships", cpu))
			} else {
				assignments[cpu] = id
			}
		}
	}
	return assignments
}

func discoverLLC(fsys fs.FS, base string, id int, t *Topology) string {
	entries, err := fs.ReadDir(fsys, base+"cache")
	if err != nil {
		t.Warnings = append(t.Warnings, fmt.Sprintf("CPU %d: LLC unknown", id))
		return ""
	}
	level := -1
	identity := ""
	ambiguous := false
	malformed := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "index") {
			continue
		}
		if _, err := parseID(strings.TrimPrefix(entry.Name(), "index")); err != nil {
			continue
		}
		path := base + "cache/" + entry.Name() + "/"
		kind, err := fs.ReadFile(fsys, path+"type")
		if err != nil {
			malformed = true
			continue
		}
		cacheType := strings.TrimSpace(string(kind))
		if cacheType == "Instruction" {
			continue
		}
		if cacheType != "Unified" && cacheType != "Data" {
			malformed = true
			continue
		}
		data, err := fs.ReadFile(fsys, path+"level")
		if err != nil {
			malformed = true
			continue
		}
		current, err := parseID(strings.TrimSpace(string(data)))
		if err != nil || current == 0 {
			malformed = true
			continue
		}
		if current < level {
			continue
		}
		shared, err := readSet(fsys, path+"shared_cpu_list")
		if err != nil || !shared.Contains(id) {
			malformed = true
			continue
		}
		// The complete shared list identifies the cache instance, not its
		// per-CPU index or a clipped list which could change with the fence.
		key := "L" + strconv.Itoa(current) + ":" + shared.String()
		if current == level && key != identity {
			ambiguous = true
		} else if current > level {
			ambiguous = false
		}
		level, identity = current, key
	}
	if malformed || ambiguous || identity == "" {
		t.Warnings = append(t.Warnings, fmt.Sprintf("CPU %d: LLC unknown or inconsistent", id))
		return ""
	}
	return identity
}

func (t Topology) clone() Topology {
	copy := t
	copy.CPUs = make(map[int]CPU, len(t.CPUs))
	for id, cpu := range t.CPUs {
		copy.CPUs[id] = cpu
	}
	copy.Warnings = slices.Clone(t.Warnings)
	return copy
}

func (t Topology) mems(cpus Set) Set {
	ids := make([]int, 0, cpus.Len())
	for _, id := range cpus.ids {
		cpu, ok := t.CPUs[id]
		if !ok || cpu.NUMA < 0 {
			return Set{}
		}
		ids = append(ids, cpu.NUMA)
	}
	slices.Sort(ids)
	return Set{ids: slices.Compact(ids)}
}
