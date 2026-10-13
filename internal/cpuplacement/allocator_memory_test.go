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
	"testing"
)

func memoryTopology(t testing.TB) Topology {
	return Topology{Online: set(t, "0-4"), CPUs: map[int]CPU{
		0: {ID: 0, NUMA: 0, LLC: "a"}, 1: {ID: 1, NUMA: 1, LLC: "b"},
		2: {ID: 2, NUMA: 1, LLC: "b"}, 3: {ID: 3, NUMA: 2, LLC: "c"}, 4: {ID: 4, NUMA: -1},
	}}
}
func TestLocalMemoryFiltersEveryPlacementPolicy(t *testing.T) {
	for _, policy := range []Policy{PolicyShared, PolicyNonOverlapping} {
		for _, placement := range []Placement{PlacementAny, PlacementPreferLLC, PlacementRequireLLC, PlacementPreferNUMA, PlacementRequireNUMA} {
			t.Run(string(policy)+"/"+string(placement), func(t *testing.T) {
				a := allocator(t, Options{Allowed: set(t, "0-4"), Policy: policy, Placement: placement, Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "1")})
				lease := acquire(t, a, "actor", 1, Set{})
				if lease.CPUs.String() != "1" || lease.Mems.String() != "1" {
					t.Fatalf("selected ineligible CPUs/mems: %+v", lease)
				}
				if _, err := a.Acquire("explicit-bad", 1, set(t, "0")); !errors.Is(err, ErrCapacity) {
					t.Fatalf("explicit set bypassed %s memory eligibility: %v", placement, err)
				}
				if lease := acquire(t, a, "explicit-good", 1, set(t, "2")); lease.Mems.String() != "1" {
					t.Fatalf("eligible explicit lease=%+v", lease)
				}
			})
		}
	}
}
func TestLocalMemoryRejectsExplicitIneligibleCPU(t *testing.T) {
	for _, explicit := range []string{"0", "3", "4"} {
		t.Run(explicit, func(t *testing.T) {
			a := allocator(t, Options{Allowed: set(t, "0-4"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "1")})
			if _, err := a.Acquire("bad", 1, set(t, explicit)); !errors.Is(err, ErrCapacity) {
				t.Fatalf("ineligible explicit CPU admitted: %v", err)
			}
			if len(a.Leases()) != 0 {
				t.Fatal("failed admission retained a lease")
			}
		})
	}
}

func TestLocalMemoryManagementReservationUsesPhysicalBoundary(t *testing.T) {
	a := allocator(t, Options{Allowed: set(t, "0-4"), Reserved: set(t, "0"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "1"), Policy: PolicyNonOverlapping})
	if got := a.ActorCPUs().String(); got != "1-2" {
		t.Fatalf("Actor pool=%s", got)
	}
	lease := acquire(t, a, "actor", 2, Set{})
	if lease.CPUs.String() != "1-2" || lease.Mems.String() != "1" {
		t.Fatalf("lease=%+v", lease)
	}
	if got := a.ActorCPUs().String(); got != "1-2" {
		t.Fatalf("ActorCPUs subtracted occupancy: %s", got)
	}
	if err := a.UpdateAllowed(set(t, "0-4")); err != nil {
		t.Fatal(err)
	}
	if err := a.UpdateAllowed(set(t, "1-4")); !errors.Is(err, ErrBoundary) {
		t.Fatalf("reservation outside physical boundary accepted: %v", err)
	}
}

func TestLocalMemoryBoundaryShrinkRetainsOwnership(t *testing.T) {
	a := allocator(t, Options{Allowed: set(t, "0-4"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "1-2")})
	lease := acquire(t, a, "actor", 1, set(t, "1"))
	for range 2 {
		err := a.UpdateBoundary(set(t, "0-4"), set(t, "2"))
		var boundary *BoundaryError
		if !errors.As(err, &boundary) || !boundary.LocalMemory || boundary.AllowedMems.String() != "2" || len(boundary.Owners) != 1 || boundary.Owners[0] != "actor" {
			t.Fatalf("shrink=%v", err)
		}
		for _, owner := range []string{"actor", "another"} {
			if _, err := a.Acquire(owner, 1, Set{}); !errors.Is(err, ErrBoundary) {
				t.Fatalf("unsafe admission %s: %v", owner, err)
			}
		}
		if got := a.Leases(); len(got) != 1 || got[0].Generation != lease.Generation || !got[0].CPUs.Equal(lease.CPUs) {
			t.Fatalf("shrink mutated lease: %+v", got)
		}
	}
	if err := a.UpdateAllowed(set(t, "0-4")); !errors.Is(err, ErrBoundary) {
		t.Fatalf("CPU-only update reset memory constraint: %v", err)
	}
	if err := a.Release(lease); err != nil {
		t.Fatal(err)
	}
	if got := acquire(t, a, "new", 1, Set{}).CPUs.String(); got != "3" {
		t.Fatalf("new memory boundary not retained: %s", got)
	}
}

func TestLocalMemoryRecoveryChecksObservedMemory(t *testing.T) {
	for _, observed := range []string{"", "0", "0-1"} {
		t.Run(observed, func(t *testing.T) {
			a := allocator(t, Options{Allowed: set(t, "0-4"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "1")})
			lease, err := a.Recover("old", set(t, "1"), set(t, observed))
			if !errors.Is(err, ErrBoundary) || lease.Generation == "" {
				t.Fatalf("unrecorded unsafe recovery: %+v %v", lease, err)
			}
			if _, err := a.Acquire("new", 1, Set{}); !errors.Is(err, ErrBoundary) {
				t.Fatalf("unsafe recovery did not block: %v", err)
			}
			if err := a.Release(lease); err != nil {
				t.Fatal(err)
			}
			acquire(t, a, "new", 1, Set{})
		})
	}
}

func TestLocalMemoryStrictAndPreferredFragmentation(t *testing.T) {
	for _, placement := range []Placement{PlacementPreferLLC, PlacementRequireLLC, PlacementPreferNUMA, PlacementRequireNUMA} {
		t.Run(string(placement), func(t *testing.T) {
			a := allocator(t, Options{Allowed: set(t, "0-4"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "1-2"), Placement: placement})
			lease, err := a.Acquire("actor", 3, Set{})
			strict := placement == PlacementRequireLLC || placement == PlacementRequireNUMA
			if strict {
				if !errors.Is(err, ErrPlacement) || len(a.Leases()) != 0 {
					t.Fatalf("strict degraded: %+v %v", lease, err)
				}
			} else if err != nil || lease.CPUs.String() != "1-3" {
				t.Fatalf("preference crossed memory boundary: %+v %v", lease, err)
			}
		})
	}
}

func TestLocalMemoryEmptyAndUnknownFailClosed(t *testing.T) {
	for _, opts := range []Options{
		{Allowed: set(t, "0-4"), Topology: memoryTopology(t), LocalMemory: true},
		{Allowed: set(t, "4"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "0")},
		{Allowed: set(t, "9"), LocalMemory: true, AllowedMems: set(t, "0")},
	} {
		if _, err := New(opts); err == nil {
			t.Fatalf("unknown/empty memory allowed: %+v", opts)
		}
	}
	a := allocator(t, Options{Allowed: set(t, "0-4"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "1")})
	if err := a.UpdateBoundary(set(t, "0-4"), Set{}); !errors.Is(err, ErrBoundary) {
		t.Fatalf("empty memory update=%v", err)
	}
	if _, err := a.Acquire("actor", 1, Set{}); !errors.Is(err, ErrBoundary) {
		t.Fatalf("empty boundary admitted: %v", err)
	}
	if err := a.UpdateBoundary(set(t, "0-4"), set(t, "1")); err != nil {
		t.Fatal(err)
	}
	acquire(t, a, "actor", 1, Set{})
}

func TestInheritedMemoryAndOnlineSnapshotRemainUnchanged(t *testing.T) {
	a := allocator(t, Options{Allowed: set(t, "0-4,9"), Topology: memoryTopology(t), AllowedMems: set(t, "1")})
	if got := acquire(t, a, "unknown", 1, set(t, "4")).CPUs.String(); got != "4" {
		t.Fatal(got)
	}
	if err := a.UpdateBoundary(set(t, "0-4,9"), Set{}); err != nil {
		t.Fatal(err)
	}
	if got := a.ActorCPUs().String(); got != "0-4" {
		t.Fatalf("newly online CPU invented: %s", got)
	}
	acquire(t, a, "node0", 1, set(t, "0"))
}
