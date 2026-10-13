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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
)

type Policy string

const (
	PolicyShared         Policy = "shared"
	PolicyNonOverlapping Policy = "nonoverlapping"
)

type Placement string

const (
	PlacementAny         Placement = "any"
	PlacementPreferLLC   Placement = "prefer-llc"
	PlacementRequireLLC  Placement = "require-llc"
	PlacementPreferNUMA  Placement = "prefer-numa"
	PlacementRequireNUMA Placement = "require-numa"
)

var (
	ErrCapacity    = errors.New("insufficient logical CPU capacity")
	ErrPlacement   = errors.New("required topology placement unavailable")
	ErrOwnerExists = errors.New("owner already has a different allocation")
	ErrStaleLease  = errors.New("stale CPU lease generation")
	ErrBoundary    = errors.New("CPU allocation boundary requires reconciliation")
	ErrOverlap     = errors.New("recovered nonoverlapping CPU leases overlap")
)

// BoundaryError reports an applied boundary change or recorded recovery which
// requires reconciliation. New allocations remain blocked while it is present.
type BoundaryError struct {
	Allowed         Set
	ReservedOutside Set
	AllowedMems     Set
	LocalMemory     bool
	Owners          []string
}

func (e *BoundaryError) Error() string {
	if e.LocalMemory {
		return fmt.Sprintf("%v: allowed=%q local memory nodes=%q reservation outside=%q affected owners=%v", ErrBoundary, e.Allowed, e.AllowedMems, e.ReservedOutside, e.Owners)
	}
	return fmt.Sprintf("%v: allowed=%q reservation outside=%q affected owners=%v", ErrBoundary, e.Allowed, e.ReservedOutside, e.Owners)
}
func (e *BoundaryError) Unwrap() error { return ErrBoundary }

type Options struct {
	Allowed, Reserved Set
	Policy            Policy
	Placement         Placement
	Topology          Topology
	// LocalMemory restricts Actor CPUs to known NUMA nodes in AllowedMems.
	// Management reservations remain governed by the full Allowed CPU set.
	LocalMemory bool
	AllowedMems Set
}

// Lease identifies one owner incarnation. Mems is a placement recommendation,
// not a memory migration guarantee. Empty Mems means unknown; the caller must
// inherit a valid effective parent memory-node set instead of writing empty.
type Lease struct {
	Owner      string
	Generation string
	CPUs, Mems Set
	Policy     Policy
	Placement  Placement
	Reason     string
}

type allocation struct {
	lease     Lease
	count     int
	explicit  Set
	recovered bool
}

// Allocator serializes allocation within one worker. It does not coordinate
// workers or alter quotas. Only call Release after all associated processes
// have stopped and cgroup ownership is verified, including startup rollback.
type Allocator struct {
	mu          sync.Mutex
	allowed     Set
	reserved    Set
	localMemory bool
	allowedMems Set
	policy      Policy
	placement   Placement
	topology    Topology
	epoch       string
	next        uint64
	active      map[string]allocation
}

func New(options Options) (*Allocator, error) {
	if options.Policy == "" {
		options.Policy = PolicyShared
	}
	if options.Placement == "" {
		options.Placement = PlacementAny
	}
	if options.Policy != PolicyShared && options.Policy != PolicyNonOverlapping {
		return nil, fmt.Errorf("unknown allocation policy %q", options.Policy)
	}
	switch options.Placement {
	case PlacementAny, PlacementPreferLLC, PlacementRequireLLC, PlacementPreferNUMA, PlacementRequireNUMA:
	default:
		return nil, fmt.Errorf("unknown topology placement %q", options.Placement)
	}
	a := &Allocator{
		reserved: options.Reserved, policy: options.Policy, placement: options.Placement,
		topology: options.Topology.clone(), active: make(map[string]allocation),
		localMemory: options.LocalMemory, allowedMems: options.AllowedMems,
	}
	a.allowed = a.onlineAllowed(options.Allowed)
	if err := a.boundaryError(); err != nil {
		return nil, err
	}
	if a.actorCPUs().Len() == 0 {
		return nil, fmt.Errorf("%w: reservation leaves no Actor CPUs", ErrCapacity)
	}
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, fmt.Errorf("create CPU lease epoch: %w", err)
	}
	a.epoch = hex.EncodeToString(epoch[:])
	return a, nil
}

// Acquire reserves exactly count logical CPUs; explicit, when nonempty, must
// have that width. Retrying an identical active request returns the same lease.
// Neither capacity exhaustion nor a strict placement error changes policy.
func (a *Allocator) Acquire(owner string, count int, explicit Set) (Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if owner == "" || count < 1 {
		return Lease{}, fmt.Errorf("owner must be nonempty and CPU count positive")
	}
	if explicit.Len() != 0 && explicit.Len() != count {
		return Lease{}, fmt.Errorf("explicit CPU width %d differs from requested count %d", explicit.Len(), count)
	}
	if err := a.stateError(); err != nil {
		return Lease{}, err
	}
	if current, ok := a.active[owner]; ok {
		if !current.recovered && current.count == count && current.explicit.Equal(explicit) {
			return current.lease, nil
		}
		return Lease{}, fmt.Errorf("%w: %s", ErrOwnerExists, owner)
	}
	available := a.actorCPUs()
	usage := a.usage()
	if a.policy == PolicyNonOverlapping {
		for _, current := range a.active {
			available = available.Difference(current.lease.CPUs)
		}
	}
	if explicit.Len() != 0 && !explicit.IsSubset(available) {
		return Lease{}, fmt.Errorf("%w: explicit set %s is not available within %s", ErrCapacity, explicit, available)
	}
	if count > available.Len() {
		return Lease{}, fmt.Errorf("%w: requested %d, available %d", ErrCapacity, count, available.Len())
	}
	cpus, reason, err := a.selectCPUs(available, count, explicit, usage)
	if err != nil {
		return Lease{}, err
	}
	lease := a.newLease(owner, cpus, a.topology.mems(cpus), reason)
	a.active[owner] = allocation{lease: lease, count: count, explicit: explicit}
	return lease, nil
}

// Release is idempotent when no lease for the owner remains. A stale generation
// can never release a newer incarnation, including one from another allocator.
// Owner and Generation are the release capability; other fields are diagnostic.
func (a *Allocator) Release(lease Lease) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if lease.Owner == "" || lease.Generation == "" {
		return ErrStaleLease
	}
	current, ok := a.active[lease.Owner]
	if !ok {
		return nil
	}
	if current.lease.Generation != lease.Generation {
		return fmt.Errorf("%w: %s", ErrStaleLease, lease.Owner)
	}
	delete(a.active, lease.Owner)
	return nil
}

// Recover records the effective sets of an already-running cgroup before any
// new allocation. A nonzero Lease with an error means occupancy WAS recorded,
// but is outside the current boundary or overlaps another recovered lease.
// Both cases block Acquire until reconciled. Invalid input or an owner mismatch
// returns a zero Lease and leaves state unchanged. Recovered leases cannot be
// reused by Acquire: the caller must verify process exit before releasing them.
func (a *Allocator) Recover(owner string, cpus, mems Set) (Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if owner == "" || cpus.Len() == 0 {
		return Lease{}, fmt.Errorf("recovery requires owner and nonempty effective CPUs")
	}
	if current, ok := a.active[owner]; ok {
		if !current.lease.CPUs.Equal(cpus) || !current.lease.Mems.Equal(mems) {
			return Lease{}, fmt.Errorf("%w: %s", ErrOwnerExists, owner)
		}
		return current.lease, a.stateError()
	}
	lease := a.newLease(owner, cpus, mems, "recovered running cgroup; observed placement retained")
	a.active[owner] = allocation{lease: lease, recovered: true}
	return lease, a.stateError()
}

// UpdateAllowed always installs the new online-filtered boundary, even when it
// returns ErrBoundary for active leases or a reservation no longer inside it.
// It never expands beyond the discovery snapshot's online CPUs. Rediscover
// topology and recover running leases before admitting newly-online CPU IDs.
func (a *Allocator) UpdateAllowed(allowed Set) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.allowed = a.onlineAllowed(allowed)
	return a.stateError()
}

// UpdateBoundary atomically installs CPU and memory-node constraints, even on
// error. Active leases remain recorded and block admission until reconciled.
func (a *Allocator) UpdateBoundary(allowed, mems Set) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.allowed = a.onlineAllowed(allowed)
	a.allowedMems = mems
	return a.stateError()
}

// ActorCPUs is the eligible online Actor pool before subtracting occupancy.
// Management reservations are still checked against the full CPU boundary.
func (a *Allocator) ActorCPUs() Set {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.actorCPUs()
}
func (a *Allocator) actorCPUs() Set {
	candidates := a.allowed.Difference(a.reserved)
	if !a.localMemory {
		return candidates
	}
	var ids []int
	for _, id := range candidates.ids {
		cpu, ok := a.topology.CPUs[id]
		if ok && cpu.NUMA >= 0 && a.allowedMems.Contains(cpu.NUMA) {
			ids = append(ids, id)
		}
	}
	return Set{ids: ids}
}

func (a *Allocator) Leases() []Lease {
	a.mu.Lock()
	defer a.mu.Unlock()
	leases := make([]Lease, 0, len(a.active))
	for _, current := range a.active {
		leases = append(leases, current.lease)
	}
	slices.SortFunc(leases, func(x, y Lease) int {
		if x.Owner < y.Owner {
			return -1
		}
		if x.Owner > y.Owner {
			return 1
		}
		return 0
	})
	return leases
}

func (a *Allocator) onlineAllowed(allowed Set) Set {
	if a.topology.Online.Len() != 0 {
		return allowed.Intersection(a.topology.Online)
	}
	return allowed
}

func (a *Allocator) boundaryError() error {
	out := a.reserved.Difference(a.allowed)
	actorAllowed := a.actorCPUs()
	var owners []string
	for owner, current := range a.active {
		if !current.lease.CPUs.IsSubset(actorAllowed) || (a.localMemory && current.recovered && (current.lease.Mems.Len() == 0 || !current.lease.Mems.IsSubset(a.allowedMems))) {
			owners = append(owners, owner)
		}
	}
	if a.allowed.Len() != 0 && (!a.localMemory || a.allowedMems.Len() != 0) && out.Len() == 0 && len(owners) == 0 {
		return nil
	}
	slices.Sort(owners)
	return &BoundaryError{Allowed: a.allowed, ReservedOutside: out, AllowedMems: a.allowedMems, LocalMemory: a.localMemory, Owners: owners}
}

func (a *Allocator) stateError() error {
	if err := a.boundaryError(); err != nil {
		return err
	}
	if a.policy == PolicyNonOverlapping {
		for _, count := range a.usage() {
			if count > 1 {
				return ErrOverlap
			}
		}
	}
	return nil
}

func (a *Allocator) usage() map[int]int {
	usage := make(map[int]int)
	for _, current := range a.active {
		for _, id := range current.lease.CPUs.ids {
			usage[id]++
		}
	}
	return usage
}

func (a *Allocator) newLease(owner string, cpus, mems Set, reason string) Lease {
	a.next++
	return Lease{Owner: owner, Generation: a.epoch + ":" + strconv.FormatUint(a.next, 10),
		CPUs: cpus, Mems: mems, Policy: a.policy, Placement: a.placement, Reason: reason}
}

func (a *Allocator) selectCPUs(available Set, count int, explicit Set, usage map[int]int) (Set, string, error) {
	if a.placement == PlacementAny {
		if explicit.Len() != 0 {
			return explicit, "explicit logical CPU set", nil
		}
		return leastUsed(available, count, usage), "logical CPUs selected without topology constraint", nil
	}
	isLLC := a.placement == PlacementPreferLLC || a.placement == PlacementRequireLLC
	strict := a.placement == PlacementRequireLLC || a.placement == PlacementRequireNUMA
	domainName := "NUMA"
	if isLLC {
		domainName = "LLC"
	}
	groups := make(map[string]Set)
	for _, id := range available.ids {
		cpu, ok := a.topology.CPUs[id]
		if !ok {
			continue
		}
		key := cpu.LLC
		if !isLLC {
			if cpu.NUMA < 0 {
				continue
			}
			key = strconv.Itoa(cpu.NUMA)
		}
		if key != "" {
			group := groups[key]
			group.ids = append(group.ids, id)
			groups[key] = group
		}
	}
	var best Set
	bestKey, bestLoad := "", 0
	for key, group := range groups {
		if group.Len() < count || (explicit.Len() != 0 && !explicit.IsSubset(group)) {
			continue
		}
		candidate := explicit
		if candidate.Len() == 0 {
			candidate = leastUsed(group, count, usage)
		}
		load := 0
		for _, id := range candidate.ids {
			load += usage[id]
		}
		if best.Len() == 0 || load < bestLoad || (load == bestLoad && slices.Compare(candidate.ids, best.ids) < 0) {
			best, bestKey, bestLoad = candidate, key, load
		}
	}
	if best.Len() != 0 {
		return best, "single " + domainName + " domain " + bestKey, nil
	}
	if strict {
		return Set{}, "", fmt.Errorf("%w: %d CPUs do not fit one known %s domain", ErrPlacement, count, domainName)
	}
	if explicit.Len() != 0 {
		return explicit, "explicit set spans domains or has unknown " + domainName + " topology; preference degraded", nil
	}
	return leastUsed(available, count, usage), "no single known " + domainName + " domain fits; preference degraded to allowed logical CPUs", nil
}

func leastUsed(available Set, count int, usage map[int]int) Set {
	ids := available.IDs()
	slices.SortFunc(ids, func(x, y int) int {
		if usage[x] != usage[y] {
			return usage[x] - usage[y]
		}
		return x - y
	})
	ids = ids[:count]
	slices.Sort(ids)
	return Set{ids: ids}
}
