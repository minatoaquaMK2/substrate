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
	"strings"
	"sync"
	"testing"
)

func allocator(t testing.TB, options Options) *Allocator {
	t.Helper()
	a, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func acquire(t testing.TB, a *Allocator, owner string, count int, explicit Set) Lease {
	t.Helper()
	l, err := a.Acquire(owner, count, explicit)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAllocatorValidation(t *testing.T) {
	for _, options := range []Options{
		{}, {Allowed: set(t, "1-3"), Reserved: set(t, "0")},
		{Allowed: set(t, "1-3"), Reserved: set(t, "1-3")},
		{Allowed: set(t, "1-3"), Policy: "exclusive"},
		{Allowed: set(t, "1-3"), Placement: "core"},
		{Allowed: set(t, "0-3"), Reserved: set(t, "3"), Topology: Topology{Online: set(t, "0-2")}},
	} {
		if _, err := New(options); err == nil {
			t.Fatalf("accepted invalid options %+v", options)
		}
	}
	a := allocator(t, Options{Allowed: set(t, "1-3")})
	for _, tc := range []struct {
		owner string
		count int
		cpus  Set
	}{{"", 1, Set{}}, {"a", 0, Set{}}, {"a", -1, Set{}}, {"a", 2, set(t, "1")}, {"a", 1, set(t, "99")}} {
		if _, err := a.Acquire(tc.owner, tc.count, tc.cpus); err == nil {
			t.Fatalf("accepted invalid request %+v", tc)
		}
	}
	if len(a.Leases()) != 0 {
		t.Fatal("failed acquisitions leaked leases")
	}
}

func TestPoliciesAndCapacity(t *testing.T) {
	for _, policy := range []Policy{PolicyShared, PolicyNonOverlapping} {
		t.Run(string(policy), func(t *testing.T) {
			a := allocator(t, Options{Allowed: set(t, "1,3,5,7"), Reserved: set(t, "1"), Policy: policy})
			l := acquire(t, a, "a", 2, Set{})
			if l.CPUs.String() != "3,5" || l.Policy != policy || l.Mems.Len() != 0 {
				t.Fatalf("wrong lease %+v", l)
			}
			next, err := a.Acquire("b", 2, Set{})
			if policy == PolicyNonOverlapping {
				if !errors.Is(err, ErrCapacity) {
					t.Fatalf("capacity error %v", err)
				}
				last := acquire(t, a, "c", 1, Set{})
				if last.CPUs.String() != "7" || last.Policy != PolicyNonOverlapping {
					t.Fatalf("silent policy change %+v", last)
				}
			} else if err != nil || next.CPUs.String() != "3,7" {
				t.Fatalf("shared balancing %+v, %v", next, err)
			}
			if _, err := a.Acquire("huge", 4, Set{}); !errors.Is(err, ErrCapacity) {
				t.Fatalf("expected capacity error: %v", err)
			}
		})
	}
}

func TestLeaseRetryRollbackAndStaleGeneration(t *testing.T) {
	a := allocator(t, Options{Allowed: set(t, "0-1"), Policy: PolicyNonOverlapping})
	l := acquire(t, a, "actor", 2, Set{})
	retry := acquire(t, a, "actor", 2, Set{})
	if retry.Generation != l.Generation {
		t.Fatal("retry changed generation")
	}
	if _, err := a.Acquire("actor", 1, Set{}); !errors.Is(err, ErrOwnerExists) {
		t.Fatal(err)
	}
	if err := a.Release(l); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(l); err != nil {
		t.Fatal("duplicate release", err)
	}
	newLease := acquire(t, a, "actor", 2, Set{})
	if err := a.Release(l); !errors.Is(err, ErrStaleLease) {
		t.Fatal("old generation released new lease", err)
	}
	other := allocator(t, Options{Allowed: set(t, "0-1")})
	otherLease := acquire(t, other, "actor", 2, Set{})
	if newLease.Generation == otherLease.Generation || !errors.Is(a.Release(otherLease), ErrStaleLease) {
		t.Fatal("allocator epochs not isolated")
	}
	if !errors.Is(a.Release(Lease{}), ErrStaleLease) || len(a.Leases()) != 1 {
		t.Fatal("invalid release altered occupancy")
	}
	if err := a.Release(newLease); err != nil {
		t.Fatal(err)
	}
	acquire(t, a, "after-rollback", 2, Set{})
}

func TestShrinkBlocksUntilReconciled(t *testing.T) {
	a := allocator(t, Options{Allowed: set(t, "0-4"), Reserved: set(t, "0"), Policy: PolicyNonOverlapping})
	l := acquire(t, a, "actor", 1, set(t, "4"))
	for range 2 {
		err := a.UpdateAllowed(set(t, "0-3"))
		var boundary *BoundaryError
		if !errors.As(err, &boundary) || len(boundary.Owners) != 1 || boundary.Owners[0] != "actor" {
			t.Fatalf("bad shrink error %v", err)
		}
		if _, err := a.Acquire("another", 1, Set{}); !errors.Is(err, ErrBoundary) {
			t.Fatalf("new allocation admitted during unresolved shrink: %v", err)
		}
		if _, err := a.Acquire("actor", 1, set(t, "4")); !errors.Is(err, ErrBoundary) {
			t.Fatalf("unsafe idempotent retry: %v", err)
		}
	}
	if err := a.Release(l); err != nil {
		t.Fatal(err)
	}
	replacement := acquire(t, a, "replacement", 3, Set{})
	if replacement.CPUs.String() != "1-3" {
		t.Fatal("shrink was rolled back")
	}
	if err := a.UpdateAllowed(set(t, "1-3")); !errors.Is(err, ErrBoundary) {
		t.Fatal("reservation outside shrink accepted")
	}
	if _, err := a.Acquire("blocked", 1, Set{}); !errors.Is(err, ErrBoundary) {
		t.Fatal("invalid reservation not blocking", err)
	}
	if err := a.UpdateAllowed(set(t, "0-4")); err != nil {
		t.Fatal(err)
	}
	acquire(t, a, "restored", 1, set(t, "4"))
	if err := a.UpdateAllowed(Set{}); !errors.Is(err, ErrBoundary) {
		t.Fatal("empty hotplug boundary accepted")
	}
}

func TestRecoveryRetainsUnsafeOccupancy(t *testing.T) {
	a := allocator(t, Options{Allowed: set(t, "0-3"), Policy: PolicyNonOverlapping})
	l, err := a.Recover("old-processes", set(t, "3-5"), set(t, "2"))
	if !errors.Is(err, ErrBoundary) || l.Generation == "" || len(a.Leases()) != 1 {
		t.Fatalf("recovery must record occupancy and return error: %+v, %v", l, err)
	}
	if _, err := a.Acquire("new", 1, Set{}); !errors.Is(err, ErrBoundary) {
		t.Fatal("out-of-bound recovery not blocking", err)
	}
	if err := a.UpdateAllowed(set(t, "0-5")); err != nil {
		t.Fatal(err)
	}
	retry, err := a.Recover("old-processes", set(t, "3-5"), set(t, "2"))
	if err != nil || retry.Generation != l.Generation {
		t.Fatal("recovery retry", err)
	}
	if _, err := a.Acquire("old-processes", 3, set(t, "3-5")); !errors.Is(err, ErrOwnerExists) {
		t.Fatal("recovered owner reused without process-exit verification", err)
	}
	if l, err := a.Recover("old-processes", set(t, "2"), set(t, "2")); !errors.Is(err, ErrOwnerExists) || l.Generation != "" {
		t.Fatal("mismatched recovery accepted")
	}
	conflict, err := a.Recover("another-old-process", set(t, "2-3"), Set{})
	if !errors.Is(err, ErrOverlap) || conflict.Generation == "" || len(a.Leases()) != 2 {
		t.Fatal("overlapping recovery was not retained", err)
	}
	if _, err := a.Acquire("new", 1, Set{}); !errors.Is(err, ErrOverlap) {
		t.Fatal("conflicting recovery not blocking", err)
	}
	if err := a.Release(conflict); err != nil {
		t.Fatal(err)
	}
	newLease := acquire(t, a, "new", 3, Set{})
	if newLease.CPUs.String() != "0-2" {
		t.Fatal("recovered running CPUs reused")
	}
	if l, err := a.Recover("", Set{}, Set{}); err == nil || l.Generation != "" {
		t.Fatal("invalid recovery")
	}
}

func placementTopology(t testing.TB) Topology {
	t.Helper()
	return Topology{Online: set(t, "0,2,4,6,8,10"), Source: "fixture-visible", CPUs: map[int]CPU{
		0: {ID: 0, NUMA: 0, LLC: "L3:0,2"}, 2: {ID: 2, NUMA: 0, LLC: "L3:0,2"},
		4: {ID: 4, NUMA: 0, LLC: "L3:4,6"}, 6: {ID: 6, NUMA: 0, LLC: "L3:4,6"},
		8: {ID: 8, NUMA: 3, LLC: "L3:8,10"}, 10: {ID: 10, NUMA: 3, LLC: "L3:8,10"},
	}}
}

func TestTopologyPlacement(t *testing.T) {
	for _, tc := range []struct {
		placement Placement
		count     int
		want      string
		wantMems  string
		wantError bool
		degraded  bool
	}{
		{PlacementRequireLLC, 2, "0,2", "0", false, false},
		{PlacementRequireLLC, 3, "", "", true, false},
		{PlacementPreferLLC, 3, "0,2,4", "0", false, true},
		{PlacementRequireNUMA, 3, "0,2,4", "0", false, false},
		{PlacementRequireNUMA, 5, "", "", true, false},
		{PlacementPreferNUMA, 5, "0,2,4,6,8", "0,3", false, true},
	} {
		t.Run(fmt.Sprintf("%s_%d", tc.placement, tc.count), func(t *testing.T) {
			a := allocator(t, Options{Allowed: set(t, "0-10"), Placement: tc.placement, Topology: placementTopology(t)})
			l, err := a.Acquire("a", tc.count, Set{})
			if tc.wantError {
				if !errors.Is(err, ErrPlacement) || len(a.Leases()) != 0 {
					t.Fatalf("required topology must fail without leak: %v", err)
				}
				return
			}
			if err != nil || l.CPUs.String() != tc.want || l.Mems.String() != tc.wantMems || strings.Contains(l.Reason, "degraded") != tc.degraded {
				t.Fatalf("wrong lease %+v err %v", l, err)
			}
		})
	}
}

func TestPlacementFragmentationUnknownAndExplicit(t *testing.T) {
	topo := placementTopology(t)
	a := allocator(t, Options{Allowed: topo.Online, Policy: PolicyNonOverlapping, Placement: PlacementRequireLLC, Topology: topo})
	acquire(t, a, "fragment0", 1, set(t, "0"))
	acquire(t, a, "fragment4", 1, set(t, "4"))
	if got := acquire(t, a, "whole", 2, Set{}).CPUs.String(); got != "8,10" {
		t.Fatal(got)
	}
	if _, err := a.Acquire("fragmented", 2, Set{}); !errors.Is(err, ErrPlacement) {
		t.Fatal("aggregate capacity is not a single domain", err)
	}
	for _, placement := range []Placement{PlacementRequireLLC, PlacementRequireNUMA} {
		a := allocator(t, Options{Allowed: set(t, "0-2"), Placement: placement})
		if _, err := a.Acquire("unknown", 1, Set{}); !errors.Is(err, ErrPlacement) {
			t.Fatal("unknown topology accepted", err)
		}
	}
	for _, placement := range []Placement{PlacementPreferLLC, PlacementPreferNUMA} {
		a := allocator(t, Options{Allowed: set(t, "0-2"), Placement: placement})
		if l := acquire(t, a, "unknown", 1, Set{}); !strings.Contains(l.Reason, "degraded") || l.Mems.Len() != 0 {
			t.Fatal("unknown topology did not report downgrade")
		}
	}
	for _, placement := range []Placement{PlacementRequireLLC, PlacementPreferLLC} {
		a := allocator(t, Options{Allowed: topo.Online, Placement: placement, Topology: topo})
		l, err := a.Acquire("explicit-cross", 2, set(t, "0,4"))
		if placement == PlacementRequireLLC && !errors.Is(err, ErrPlacement) {
			t.Fatal(err)
		}
		if placement == PlacementPreferLLC && (err != nil || !strings.Contains(l.Reason, "degraded")) {
			t.Fatalf("%+v %v", l, err)
		}
	}
}

func TestTopologySnapshotDefensiveCopy(t *testing.T) {
	topo := placementTopology(t)
	a := allocator(t, Options{Allowed: set(t, "0-20"), Topology: topo, Placement: PlacementRequireLLC})
	delete(topo.CPUs, 0)
	delete(topo.CPUs, 2)
	if l := acquire(t, a, "a", 2, Set{}); l.CPUs.String() != "0,2" {
		t.Fatal("caller mutated topology snapshot")
	}
	if err := a.UpdateAllowed(set(t, "0-20")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire("offline", 1, set(t, "20")); !errors.Is(err, ErrCapacity) {
		t.Fatal("online snapshot boundary bypassed", err)
	}
}

func TestConcurrentNonoverlapAndRelease(t *testing.T) {
	a := allocator(t, Options{Allowed: set(t, "0-7"), Policy: PolicyNonOverlapping})
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan Lease, 64)
	for i := range 64 {
		wg.Go(func() {
			<-start
			l, err := a.Acquire(fmt.Sprintf("actor-%02d", i), 1, Set{})
			if err == nil {
				results <- l
			} else if !errors.Is(err, ErrCapacity) {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	seen := Set{}
	var leases []Lease
	for l := range results {
		if l.CPUs.Intersection(seen).Len() != 0 {
			t.Fatal("concurrent lease overlap")
		}
		seen = seen.Union(l.CPUs)
		leases = append(leases, l)
	}
	if seen.Len() != 8 || len(leases) != 8 {
		t.Fatalf("allocated %d CPUs in %d leases", seen.Len(), len(leases))
	}
	for _, l := range leases {
		wg.Go(func() {
			if err := a.Release(l); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(a.Leases()) != 0 {
		t.Fatal("lease leak")
	}
	acquire(t, a, "all-after-exit", 8, Set{})
}
