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
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// This fixture models several LLCs per NUMA node, two sockets, sparse IDs,
// offline siblings and a parent boundary exposing only part of a core.
func topologyFixture() fstest.MapFS {
	f := fstest.MapFS{}
	put := func(path, value string) { f[path] = &fstest.MapFile{Data: []byte(value + "\n")} }
	put("cpu/online", "0,2,4,6,8,10,12")
	put("node/node0/cpulist", "0,2,4,6")
	put("node/node3/cpulist", "8,10,12")
	for _, tc := range []struct {
		id, socket, core, llcIndex int
		siblings, llc              string
	}{
		{0, 0, 0, 3, "0,2", "0,2"}, {2, 0, 0, 3, "0,2", "0,2"},
		{4, 0, 1, 3, "4,6", "4,6"}, {6, 0, 1, 3, "4,6", "4,6"},
		{8, 1, 0, 7, "8-9", "8-10"}, {10, 1, 1, 1, "10-11", "8-10"},
		{12, 1, 2, 3, "12", "12"},
	} {
		base := fmt.Sprintf("cpu/cpu%d/", tc.id)
		put(base+"topology/physical_package_id", fmt.Sprint(tc.socket))
		put(base+"topology/core_id", fmt.Sprint(tc.core))
		put(base+"topology/thread_siblings_list", tc.siblings)
		put(base+"cache/index0/type", "Data")
		put(base+"cache/index0/level", "1")
		put(base+"cache/index0/shared_cpu_list", fmt.Sprint(tc.id))
		cache := fmt.Sprintf("%scache/index%d/", base, tc.llcIndex)
		put(cache+"type", "Unified")
		put(cache+"level", "3")
		put(cache+"shared_cpu_list", tc.llc)
	}
	return f
}

func TestDiscoverSparseAndClippedTopology(t *testing.T) {
	topo, err := Discover(topologyFixture(), set(t, "2,4,6,8-10"))
	if err != nil {
		t.Fatal(err)
	}
	if topo.Online.String() != "0,2,4,6,8,10,12" || len(topo.CPUs) != 5 || len(topo.Warnings) != 0 || topo.Source != "linux-sysfs-visible" {
		t.Fatalf("topology %+v", topo)
	}
	for _, id := range []int{0, 9, 11, 12} {
		if _, ok := topo.CPUs[id]; ok {
			t.Fatalf("CPU %d outside visible online boundary", id)
		}
	}
	if topo.CPUs[2].Siblings.String() != "2" || topo.CPUs[8].Siblings.String() != "8" {
		t.Fatal("partial and offline siblings not filtered")
	}
	if topo.CPUs[4].Siblings.String() != "4,6" || topo.CPUs[4].Socket != 0 || topo.CPUs[8].Socket != 1 || topo.CPUs[8].NUMA != 3 {
		t.Fatal("socket/core/NUMA topology incorrect")
	}
	if topo.CPUs[2].LLC == topo.CPUs[4].LLC {
		t.Fatal("same cache index confused with same cache instance")
	}
	if topo.CPUs[8].LLC != topo.CPUs[10].LLC || topo.CPUs[8].LLC != "L3:8-10" {
		t.Fatal("different cache indices or clipped masks confused with different instances")
	}
	a := allocator(t, Options{Allowed: set(t, "2,4,6,8-10"), Placement: PlacementRequireLLC, Topology: topo})
	if got := acquire(t, a, "llc", 2, Set{}).CPUs.String(); got != "4,6" {
		t.Fatalf("expected first full visible LLC, got %s", got)
	}
}

func TestDiscoverUnknownAndMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		fs   fstest.MapFS
	}{
		{"missing", fstest.MapFS{}},
		{"empty", fstest.MapFS{"cpu/online": {Data: []byte("")}}},
		{"malformed", fstest.MapFS{"cpu/online": {Data: []byte("0-z")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Discover(tc.fs, set(t, "0")); err == nil {
				t.Fatal("missing trusted online boundary accepted")
			}
		})
	}
	topo, err := Discover(fstest.MapFS{"cpu/online": {Data: []byte("0,4")}}, set(t, "0,4"))
	if err != nil || len(topo.Warnings) == 0 {
		t.Fatal("optional absent topology must be unknown", err)
	}
	for _, cpu := range topo.CPUs {
		if cpu.Core != -1 || cpu.Socket != -1 || cpu.NUMA != -1 || cpu.LLC != "" || cpu.Siblings.Len() != 0 {
			t.Fatalf("unknown topology inferred: %+v", cpu)
		}
	}
	a := allocator(t, Options{Allowed: topo.Online, Placement: PlacementRequireLLC, Topology: topo})
	if _, err := a.Acquire("unknown", 1, Set{}); !errors.Is(err, ErrPlacement) {
		t.Fatal(err)
	}
}

func TestDiscoverMalformedLLCNeverBecomesL1(t *testing.T) {
	for _, path := range []string{"level", "shared_cpu_list", "type"} {
		t.Run(path, func(t *testing.T) {
			fixture := topologyFixture()
			delete(fixture, "cpu/cpu0/cache/index3/"+path)
			topo, err := Discover(fixture, set(t, "0"))
			if err != nil || topo.CPUs[0].LLC != "" || len(topo.Warnings) == 0 {
				t.Fatalf("invalid LLC metadata was treated as L1 placement: %+v, %v", topo, err)
			}
		})
	}
}

func TestDiscoverInvalidAndConflictingMetadata(t *testing.T) {
	fixture := topologyFixture()
	fixture["cpu/cpu0/topology/physical_package_id"].Data = []byte("-1")
	fixture["cpu/cpu0/topology/thread_siblings_list"].Data = []byte("2")
	fixture["node/node3/cpulist"].Data = []byte("0,8,10,12")
	fixture["cpu/cpu0/cache/index4/type"] = &fstest.MapFile{Data: []byte("Unified")}
	fixture["cpu/cpu0/cache/index4/level"] = &fstest.MapFile{Data: []byte("3")}
	fixture["cpu/cpu0/cache/index4/shared_cpu_list"] = &fstest.MapFile{Data: []byte("0,4")}
	topo, err := Discover(fixture, set(t, "0"))
	if err != nil {
		t.Fatal(err)
	}
	cpu := topo.CPUs[0]
	if cpu.Socket != -1 || cpu.NUMA != -1 || cpu.Siblings.Len() != 0 || cpu.LLC != "" {
		t.Fatalf("inconsistent metadata silently trusted: %+v", cpu)
	}
	if topo.mems(set(t, "0")).Len() != 0 || topo.mems(set(t, "999")).Len() != 0 {
		t.Fatal("unknown memory nodes inferred")
	}
}

type hotplugFS struct {
	fs.FS
	reads int
}

func (f *hotplugFS) Open(name string) (fs.File, error) {
	if name == "cpu/online" {
		f.reads++
		if f.reads > 1 {
			return fstest.MapFS{"cpu/online": {Data: []byte("0")}}.Open(name)
		}
	}
	return f.FS.Open(name)
}

func TestDiscoverHotplugDuringRead(t *testing.T) {
	fixture := &hotplugFS{FS: topologyFixture()}
	if _, err := Discover(fixture, set(t, "0,2")); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("inconsistent online snapshots accepted", err)
	}
}
