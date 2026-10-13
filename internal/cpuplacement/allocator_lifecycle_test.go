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
	"slices"
	"sync"
	"testing"
)

func sameLeases(left, right []Lease) bool {
	return slices.EqualFunc(left, right, func(a, b Lease) bool {
		return a.Owner == b.Owner && a.Generation == b.Generation && a.CPUs.Equal(b.CPUs) && a.Mems.Equal(b.Mems) && a.Policy == b.Policy && a.Placement == b.Placement && a.Reason == b.Reason
	})
}

func FuzzAllocatorLifecycle(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	f.Add([]byte{0, 0, 0, 0, 64, 128, 192, 255, 3, 2, 1, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256 {
			data = data[:256]
		}
		policy := PolicyNonOverlapping
		if len(data) > 0 && data[0]&1 != 0 {
			policy = PolicyShared
		}
		a := allocator(t, Options{Allowed: set(t, "0-4"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "0-2"), Policy: policy})
		allowed, mems := set(t, "0-4"), set(t, "0-2")
		recovered := map[string]bool{}
		var history []Lease
		maskSet := func(mask byte, max int) Set {
			var ids []int
			for i := 0; i < max; i++ {
				if mask&(1<<i) != 0 {
					ids = append(ids, i)
				}
			}
			return Set{ids: ids}
		}
		for _, b := range data {
			owner := fmt.Sprintf("actor-%d", (b>>3)%4)
			before := a.Leases()
			switch b % 4 {
			case 0:
				lease, err := a.Acquire(owner, 1+int((b>>5)&1), Set{})
				if err == nil {
					history = append(history, lease)
					recovered[owner] = false
				} else if !sameLeases(before, a.Leases()) {
					t.Fatal("failed acquire changed state")
				}
			case 1:
				if len(history) > 0 {
					lease := history[int(b)%len(history)]
					active := Lease{}
					for _, current := range before {
						if current.Owner == lease.Owner {
							active = current
						}
					}
					err := a.Release(lease)
					if active.Generation != "" && active.Generation != lease.Generation {
						if !errors.Is(err, ErrStaleLease) || !sameLeases(before, a.Leases()) {
							t.Fatal("stale release changed ownership")
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						delete(recovered, lease.Owner)
					}
				}
			case 2:
				allowed = maskSet(b>>2, 5)
				mems = maskSet(b>>5, 3)
				_ = a.UpdateBoundary(allowed, mems)
				if !sameLeases(before, a.Leases()) {
					t.Fatal("boundary update changed active leases")
				}
			case 3:
				cpus := Set{ids: []int{int(b >> 5)}}
				observed := maskSet(b>>2, 3)
				lease, err := a.Recover(owner, cpus, observed)
				if lease.Generation != "" {
					history = append(history, lease)
					existed := false
					for _, current := range before {
						existed = existed || current.Owner == owner
					}
					if !existed {
						recovered[owner] = true
					}
				} else if err == nil || !sameLeases(before, a.Leases()) {
					t.Fatal("invalid recovery changed ownership")
				}
			}
			eligible := Set{}
			for _, id := range allowed.ids {
				node := -1
				switch id {
				case 0:
					node = 0
				case 1, 2:
					node = 1
				case 3:
					node = 2
				}
				if node >= 0 && mems.Contains(node) {
					eligible.ids = append(eligible.ids, id)
				}
			}
			if !a.ActorCPUs().Equal(eligible) {
				t.Fatalf("eligibility differs: got=%s want=%s", a.ActorCPUs(), eligible)
			}
			boundaryBad := allowed.Len() == 0 || mems.Len() == 0
			overlap := false
			usage := map[int]int{}
			occupied := Set{}
			for _, lease := range a.Leases() {
				boundaryBad = boundaryBad || !lease.CPUs.IsSubset(eligible) || (recovered[lease.Owner] && (lease.Mems.Len() == 0 || !lease.Mems.IsSubset(mems)))
				occupied = occupied.Union(lease.CPUs)
				for _, id := range lease.CPUs.ids {
					usage[id]++
					overlap = overlap || usage[id] > 1
				}
			}
			lease, err := a.Acquire("probe", 1, Set{})
			switch {
			case boundaryBad:
				if !errors.Is(err, ErrBoundary) {
					t.Fatalf("unsafe boundary admitted probe: %v", err)
				}
			case policy == PolicyNonOverlapping && overlap:
				if !errors.Is(err, ErrOverlap) {
					t.Fatalf("overlap admitted probe: %v", err)
				}
			default:
				available := eligible
				if policy == PolicyNonOverlapping {
					available = available.Difference(occupied)
				}
				if available.Len() == 0 {
					if !errors.Is(err, ErrCapacity) {
						t.Fatalf("empty capacity: %v", err)
					}
				} else {
					if err != nil || lease.CPUs.Len() != 1 || !lease.CPUs.IsSubset(available) {
						t.Fatalf("valid admission failed/broke boundary: %+v %v", lease, err)
					}
					if err := a.Release(lease); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	})
}

func TestConcurrentLocalMemoryBoundaryLifecycle(t *testing.T) {
	a := allocator(t, Options{Allowed: set(t, "0-4"), Topology: memoryTopology(t), LocalMemory: true, AllowedMems: set(t, "1-2"), Policy: PolicyNonOverlapping})
	var wg sync.WaitGroup
	for worker := range 12 {
		wg.Go(func() {
			owner := fmt.Sprintf("worker-%d", worker)
			for range 100 {
				lease, err := a.Acquire(owner, 1, Set{})
				if err != nil {
					continue
				}
				if !lease.CPUs.IsSubset(set(t, "1-3")) {
					t.Errorf("unsafe CPU: %+v", lease)
				}
				if err := a.Release(lease); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Go(func() {
		for i := range 100 {
			if i%2 == 0 {
				_ = a.UpdateBoundary(set(t, "0-4"), set(t, "1"))
			} else {
				_ = a.UpdateBoundary(set(t, "0-4"), set(t, "1-2"))
			}
		}
	})
	wg.Wait()
	if len(a.Leases()) != 0 {
		t.Fatal("concurrent ownership leaked")
	}
}
