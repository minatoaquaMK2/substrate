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
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/agent-substrate/substrate/internal/ateomcgroup"
	"github.com/agent-substrate/substrate/internal/cpuplacement"
	"github.com/agent-substrate/substrate/internal/sizing"
	"github.com/spf13/pflag"
)

var (
	actorCPUPolicy            = pflag.String("actor-cpu-policy", "off", "Actor CPU set allocation: off, shared, or nonoverlapping (within this Worker only).")
	actorCPUEnforcement       = pflag.String("actor-cpu-enforcement", "required", "Unavailable placement support: required or best-effort. Capacity exhaustion never changes the allocation policy.")
	actorCPUs                 = pflag.String("actor-cpus", "", "Operator debug CPU set for each Actor; must fit the effective Worker set and reservation.")
	managementReservedCPUs    = pflag.String("management-reserved-cpus", "", "CPUs excluded from every managed Actor. Management retains the full parent set; this is not guaranteed CPU time or node exclusivity.")
	actorCPUPlacement         = pflag.String("actor-cpu-placement", "any", "Placement: any, prefer-llc, require-llc, prefer-numa, or require-numa.")
	actorMemoryPlacement      = pflag.String("actor-memory-placement", "inherit", "Memory nodes: inherit or local. Local binds future allocations via cpuset.mems, without migrating existing/shared file pages.")
	managementCPUWeight       = pflag.Int("management-cpu-weight", 0, "Optional ateom leaf cpu.weight (1-10000); relative share only, 0 leaves the existing weight unchanged.")
	runtimeStartupConcurrency = pflag.Int("runtime-startup-concurrency", 0, "Maximum concurrent cold boots/restores in this Worker; 0 preserves unbounded concurrency.")
)

type runtimeResourceConfig struct {
	Policy             string
	Enforcement        string
	Explicit           string
	Reserved           string
	Placement          string
	MemoryPlacement    string
	ManagementWeight   int
	StartupConcurrency int
	// TrackExecution preserves cgroup ownership for nonlegacy snapshot leases.
	TrackExecution bool
}

func resourceConfigFromFlags() runtimeResourceConfig {
	return runtimeResourceConfig{Policy: *actorCPUPolicy, Enforcement: *actorCPUEnforcement, Explicit: *actorCPUs, Reserved: *managementReservedCPUs, Placement: *actorCPUPlacement, MemoryPlacement: *actorMemoryPlacement, ManagementWeight: *managementCPUWeight, StartupConcurrency: *runtimeStartupConcurrency, TrackExecution: *memoryRestorePolicy != "legacy"}
}
func (c runtimeResourceConfig) validate() error {
	if c.Policy != "off" && c.Policy != "shared" && c.Policy != "nonoverlapping" {
		return fmt.Errorf("invalid --actor-cpu-policy %q", c.Policy)
	}
	if c.Enforcement != "required" && c.Enforcement != "best-effort" {
		return fmt.Errorf("invalid --actor-cpu-enforcement %q", c.Enforcement)
	}
	switch c.Placement {
	case "any", "prefer-llc", "require-llc", "prefer-numa", "require-numa":
	default:
		return fmt.Errorf("invalid --actor-cpu-placement %q", c.Placement)
	}
	if c.MemoryPlacement != "inherit" && c.MemoryPlacement != "local" {
		return fmt.Errorf("invalid --actor-memory-placement %q", c.MemoryPlacement)
	}
	if c.StartupConcurrency < 0 {
		return fmt.Errorf("--runtime-startup-concurrency must not be negative")
	}
	if c.ManagementWeight < 0 || c.ManagementWeight > 10000 {
		return fmt.Errorf("--management-cpu-weight must be 0 or in [1,10000]")
	}
	if c.Policy == "off" && (c.Explicit != "" || c.Placement != "any" || c.MemoryPlacement != "inherit") {
		return fmt.Errorf("placement and explicit CPUs require --actor-cpu-policy")
	}
	for _, set := range []string{c.Explicit, c.Reserved} {
		if _, err := cpuplacement.ParseSet(set); err != nil {
			return err
		}
	}
	explicit, _ := cpuplacement.ParseSet(c.Explicit)
	reserved, _ := cpuplacement.ParseSet(c.Reserved)
	if explicit.Intersection(reserved).Len() != 0 {
		return fmt.Errorf("explicit Actor CPUs overlap the management reservation")
	}
	return nil
}

// runtimeResources owns allocation and lifecycle state for one Worker. Cgroups,
// rather than PIDs or in-memory Actor status, prove whether a lease is reusable.
type runtimeResources struct {
	mu              sync.Mutex
	config          runtimeResourceConfig
	allocator       *cpuplacement.Allocator
	explicit        cpuplacement.Set
	boundary        ateomcgroup.CPUBoundary
	leases          map[string]cpuplacement.Lease
	startup         chan struct{}
	recovered       map[string]struct{}
	verifyHierarchy func() error
	fallback        string
	readBoundary    func() (ateomcgroup.CPUBoundary, error)
	openLeaf        func(string, int64, cpuplacement.Set, cpuplacement.Set) (*ateomcgroup.ActorLeaf, error)
	emptyLeaf       func(string) (bool, error)
	removeLeaf      func(string) error
	killLeaf        func(context.Context, string) error
}

type runtimeHierarchy interface {
	Verify() error
	Empty(string) (bool, error)
}

type runtimeResourceSystem struct {
	requireControllers   func() error
	readBoundary         func() (ateomcgroup.CPUBoundary, error)
	setManagementWeight  func(int) error
	discoverTopology     func(cpuplacement.Set) (cpuplacement.Topology, error)
	actorLeafStates      func() ([]ateomcgroup.ActorLeafState, error)
	populatedActorOwners func() ([]string, error)
	captureHierarchy     func() (runtimeHierarchy, error)
}

func defaultRuntimeResourceSystem() runtimeResourceSystem {
	return runtimeResourceSystem{
		requireControllers:  ateomcgroup.RequireActorCPUControllers,
		readBoundary:        ateomcgroup.ReadCPUBoundary,
		setManagementWeight: ateomcgroup.SetManagementWeight,
		discoverTopology: func(cpus cpuplacement.Set) (cpuplacement.Topology, error) {
			return cpuplacement.Discover(os.DirFS("/sys/devices/system"), cpus)
		},
		actorLeafStates:      ateomcgroup.ActorLeafStates,
		populatedActorOwners: ateomcgroup.PopulatedActorOwners,
		captureHierarchy:     func() (runtimeHierarchy, error) { return ateomcgroup.CaptureActorHierarchy() },
	}
}

func newRuntimeResources(ctx context.Context, c runtimeResourceConfig, delegated bool) (*runtimeResources, error) {
	return newRuntimeResourcesWithSystem(ctx, c, delegated, defaultRuntimeResourceSystem())
}

func newRuntimeResourcesWithSystem(ctx context.Context, c runtimeResourceConfig, delegated bool, system runtimeResourceSystem) (*runtimeResources, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	r := &runtimeResources{config: c, leases: map[string]cpuplacement.Lease{}, recovered: map[string]struct{}{}, readBoundary: system.readBoundary, openLeaf: ateomcgroup.OpenPlacedActorLeaf, emptyLeaf: ateomcgroup.ActorLeafEmpty, removeLeaf: ateomcgroup.RemoveActorLeaf, killLeaf: ateomcgroup.KillActorLeaf}
	if c.StartupConcurrency > 0 {
		r.startup = make(chan struct{}, c.StartupConcurrency)
	}
	enabled := c.Policy != "off" || c.Reserved != ""
	if !enabled && c.ManagementWeight == 0 && !c.TrackExecution {
		return r, nil
	}
	verify := func() error {
		if r.verifyHierarchy != nil {
			return r.verifyHierarchy()
		}
		return nil
	}
	fallback := func(err error) (*runtimeResources, error) {
		if c.Enforcement == "required" {
			return nil, err
		}
		if e := verify(); e != nil {
			return nil, e
		}
		r.fallback = err.Error()
		slog.WarnContext(ctx, "Runtime CPU resource policy unavailable; using existing unplaced path", slog.String("requestedPolicy", c.Policy), slog.String("actualMode", "off"), slog.String("reason", r.fallback))
		return r, nil
	}
	if !delegated {
		if enabled || c.ManagementWeight > 0 {
			return fallback(fmt.Errorf("CPU resource policy requires a delegated Worker cgroup; privileged host-root fallback is unsupported"))
		}
		// Restore mode selection independently rejects/falls back from CoW without delegation.
		return r, nil
	}
	capture := func() error {
		if r.verifyHierarchy != nil {
			return verify()
		}
		h, err := system.captureHierarchy()
		if err != nil {
			return fmt.Errorf("capture Actor execution hierarchy: %w", err)
		}
		r.verifyHierarchy = h.Verify
		r.emptyLeaf = h.Empty
		return verify()
	}
	if c.TrackExecution {
		if err := capture(); err != nil {
			return nil, err
		}
		owners, err := system.populatedActorOwners()
		if err != nil {
			return nil, fmt.Errorf("recover Actor execution owners: %w", err)
		}
		for _, owner := range owners {
			r.recovered[owner] = struct{}{}
		}
		if err := verify(); err != nil {
			return nil, err
		}
	}
	if !enabled && c.ManagementWeight == 0 {
		return r, nil
	}
	b, err := system.readBoundary()
	if err != nil {
		return fallback(err)
	}
	reserved, _ := cpuplacement.ParseSet(c.Reserved)
	explicit, _ := cpuplacement.ParseSet(c.Explicit)
	// Operator mistakes remain errors even if topology support is unavailable.
	if !reserved.IsSubset(b.CPUs) || !explicit.IsSubset(b.CPUs.Difference(reserved)) {
		return nil, fmt.Errorf("configured CPUs exceed the effective Actor/reservation boundary")
	}
	if enabled && b.CPUs.Difference(reserved).Len() == 0 {
		return nil, fmt.Errorf("reservation leaves no Actor CPUs")
	}
	if enabled {
		if err := system.requireControllers(); err != nil {
			return fallback(err)
		}
	}
	if b.QuotaFinite {
		slog.WarnContext(ctx, "Management CPU reservation cannot guarantee CPU time under a finite shared parent quota", slog.Int64("parentQuotaMilliCPU", b.QuotaMilliCPU))
	}
	slog.InfoContext(ctx, "Runtime resource boundary", slog.String("cpus", b.CPUs.String()), slog.String("mems", b.Mems.String()), slog.Bool("ancestorQuotaHidden", b.AncestorQuotaHidden), slog.String("protectionScope", "worker-local-cpuset-only"))
	if enabled {
		topology, topErr := system.discoverTopology(b.CPUs)
		if topErr != nil {
			if c.Placement == "require-llc" || c.Placement == "require-numa" || c.MemoryPlacement == "local" {
				return fallback(fmt.Errorf("required topology unavailable: %w", topErr))
			}
			slog.WarnContext(ctx, "CPU topology incomplete", slog.Any("err", topErr))
		}
		if err := preflightRuntimeTopology(c, b, reserved, explicit, topology); err != nil {
			if errors.Is(err, errRuntimeTopologyUnavailable) {
				return fallback(err)
			}
			return nil, err
		}
		policy := cpuplacement.Policy(c.Policy)
		if c.Policy == "off" {
			policy = cpuplacement.PolicyShared
		}
		allocator, err := cpuplacement.New(cpuplacement.Options{Allowed: b.CPUs, Reserved: reserved, Policy: policy, Placement: cpuplacement.Placement(c.Placement), Topology: topology, LocalMemory: c.MemoryPlacement == "local", AllowedMems: b.Mems})
		if err != nil {
			return nil, err
		}
		if explicit.Len() > 0 && !explicit.IsSubset(allocator.ActorCPUs()) {
			return nil, fmt.Errorf("explicit CPUs are outside the eligible Actor topology")
		}
		if err := capture(); err != nil {
			return fallback(err)
		}
		states, err := system.actorLeafStates()
		if err != nil {
			return nil, fmt.Errorf("reconstruct CPU leases: %w", err)
		}
		for _, state := range states {
			if !state.Populated {
				continue
			}
			r.recovered[state.Owner] = struct{}{}
			lease, err := allocator.Recover(state.Owner, state.CPUs, state.Mems)
			if lease.Generation != "" {
				r.leases[state.Owner] = lease
			}
			if err != nil {
				return nil, fmt.Errorf("refusing admission with unresolved live Actor %s: %w", state.Owner, err)
			}
		}
		if err := verify(); err != nil {
			return nil, err
		}
		r.allocator = allocator
		r.explicit = explicit
		r.boundary = b

	}
	if err := verify(); err != nil {
		return nil, err
	}
	// Mutate only after every fallible preflight/recovery. A write error leaves
	// an uncertain value and cannot truthfully be reported as an unmodified fallback.
	if c.ManagementWeight > 0 {
		if err := system.setManagementWeight(c.ManagementWeight); err != nil {
			return nil, fmt.Errorf("set management weight (actual value uncertain): %w", err)
		}
	}
	if r.allocator != nil {
		actualPolicy := c.Policy
		if actualPolicy == "off" {
			actualPolicy = string(cpuplacement.PolicyShared)
		}
		slog.InfoContext(ctx, "Runtime CPU policy ready", slog.String("actualMode", actualPolicy), slog.String("reservedCPUs", reserved.String()), slog.String("scope", "worker-local"), slog.Int("recoveredLeases", len(r.leases)))
	}
	return r, nil
}

var errRuntimeTopologyUnavailable = errors.New("required runtime topology unavailable")

// preflightRuntimeTopology separates unavailable metadata (support fallback) from
// known-but-ineligible capacity or an invalid explicit domain (hard failure).
func preflightRuntimeTopology(c runtimeResourceConfig, b ateomcgroup.CPUBoundary, reserved, explicit cpuplacement.Set, topology cpuplacement.Topology) error {
	allowed := b.CPUs
	if topology.Online.Len() > 0 {
		allowed = allowed.Intersection(topology.Online)
		if !reserved.IsSubset(allowed) || !explicit.IsSubset(allowed.Difference(reserved)) {
			return fmt.Errorf("%w: configured CPUs left the known online boundary", cpuplacement.ErrBoundary)
		}
		if allowed.Difference(reserved).Len() == 0 {
			return fmt.Errorf("%w: known online boundary leaves no Actor CPUs", cpuplacement.ErrCapacity)
		}
	}
	needNUMA := c.MemoryPlacement == "local" || c.Placement == "require-numa"
	needLLC := c.Placement == "require-llc"
	if !needNUMA && !needLLC {
		return nil
	}
	candidates := allowed.Difference(reserved)
	if explicit.Len() > 0 {
		candidates = explicit
	}
	// Known violations are decisive even when another field or CPU lacks metadata.
	// Otherwise an explicit impossible request could masquerade as support fallback.
	knownMems, unknownMems, allowedMems := 0, 0, 0
	explicitNodes := map[int]struct{}{}
	explicitLLCs := map[string]struct{}{}
	for _, id := range candidates.IDs() {
		cpu, ok := topology.CPUs[id]
		if !ok || cpu.NUMA < 0 {
			unknownMems++
		} else {
			knownMems++
			if b.Mems.Contains(cpu.NUMA) {
				allowedMems++
			} else if c.MemoryPlacement == "local" && explicit.Len() > 0 {
				return fmt.Errorf("explicit CPU %d exceeds the effective local memory-node boundary", id)
			}
			if explicit.Len() > 0 && c.Placement == "require-numa" {
				explicitNodes[cpu.NUMA] = struct{}{}
			}
		}
		if ok && explicit.Len() > 0 && needLLC && cpu.LLC != "" {
			explicitLLCs[cpu.LLC] = struct{}{}
		}
	}
	if len(explicitNodes) > 1 || len(explicitLLCs) > 1 {
		return fmt.Errorf("%w: explicit CPUs span known required domains", cpuplacement.ErrPlacement)
	}
	if c.MemoryPlacement == "local" && knownMems > 0 && unknownMems == 0 && allowedMems == 0 {
		return fmt.Errorf("%w: known CPU nodes are outside the effective memory-node boundary", cpuplacement.ErrCapacity)
	}
	known, unknown, eligible := 0, 0, 0
	nodes := map[int]struct{}{}
	llcs := map[string]struct{}{}
	for _, id := range candidates.IDs() {
		cpu, ok := topology.CPUs[id]
		if !ok || (needNUMA && cpu.NUMA < 0) || (needLLC && cpu.LLC == "") {
			unknown++
			continue
		}
		known++
		if c.MemoryPlacement == "local" && !b.Mems.Contains(cpu.NUMA) {
			continue
		}
		eligible++
		nodes[cpu.NUMA] = struct{}{}
		llcs[cpu.LLC] = struct{}{}
	}
	if (explicit.Len() > 0 && unknown > 0) || (eligible == 0 && unknown > 0) || known == 0 {
		return fmt.Errorf("%w: selected CPU/memory topology is unknown", errRuntimeTopologyUnavailable)
	}
	if eligible == 0 {
		return fmt.Errorf("%w: known CPU nodes are outside the effective memory-node boundary", cpuplacement.ErrCapacity)
	}
	if explicit.Len() > 0 {
		if eligible != explicit.Len() {
			return fmt.Errorf("explicit CPUs exceed the effective local memory-node boundary")
		}
		if (needLLC && len(llcs) > 1) || (c.Placement == "require-numa" && len(nodes) > 1) {
			return fmt.Errorf("%w: explicit CPUs span required topology domains", cpuplacement.ErrPlacement)
		}
	}
	return nil
}

func (r *runtimeResources) acquire(actorUID string, size sizing.SandboxSize, defaultVCPUs int) (*ateomcgroup.ActorLeaf, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verifyHierarchy != nil {
		if err := r.verifyHierarchy(); err != nil {
			return nil, err
		}
	}
	b, err := r.readBoundary()
	if err != nil {
		return nil, err
	}
	if err := r.allocator.UpdateBoundary(b.CPUs, b.Mems); err != nil {
		return nil, fmt.Errorf("CPU boundary changed: %w", err)
	}
	count := size.VCPUs()
	if count == 0 {
		count = defaultVCPUs
	}
	if r.config.Policy == "off" {
		count = r.allocator.ActorCPUs().Len()
	}
	if r.explicit.Len() > 0 {
		count = r.explicit.Len()
	}
	_, hadLease := r.leases[actorUID]
	lease, err := r.allocator.Acquire(actorUID, count, r.explicit)
	if err != nil {
		return nil, err
	}
	r.leases[actorUID] = lease
	mems := b.Mems
	if r.config.MemoryPlacement == "local" {
		if lease.Mems.Len() == 0 || !lease.Mems.IsSubset(b.Mems) {
			if !hadLease {
				_ = r.allocator.Release(lease)
				delete(r.leases, actorUID)
			}
			return nil, fmt.Errorf("selected CPUs have no valid local memory-node boundary")
		}
		mems = lease.Mems
	}
	leaf, err := r.openLeaf(actorUID, size.MilliCPU, lease.CPUs, mems)
	if err != nil {
		// A partial write/start failure retains ownership unless the kernel proves
		// that the cgroup is empty and it is successfully removed.
		if empty, e := r.emptyLeaf(actorUID); e == nil && empty {
			if e = r.removeLeaf(actorUID); e == nil {
				_ = r.allocator.Release(lease)
				delete(r.leases, actorUID)
			}
		}
		return nil, fmt.Errorf("apply Actor CPU placement (no policy downgrade): %w", err)
	}
	r.boundary = b
	slog.Info("Actor CPU placement applied", slog.String("actorUID", actorUID), slog.String("cpus", lease.CPUs.String()), slog.String("mems", mems.String()), slog.Int64("cpuMilli", size.MilliCPU), slog.String("policy", string(lease.Policy)), slog.String("placement", string(lease.Placement)), slog.String("reason", lease.Reason), slog.String("scope", "worker-local"))
	return leaf, nil
}

func (r *runtimeResources) release(actorUID string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	lease, ok := r.leases[actorUID]
	_, recovered := r.recovered[actorUID]
	if !ok && !recovered {
		return nil
	}
	empty, err := r.emptyLeaf(actorUID)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("actor %s CPU lease retained: cgroup still populated", actorUID)
	}
	if err := r.removeLeaf(actorUID); err != nil {
		return err
	}
	if ok {
		if err := r.allocator.Release(lease); err != nil {
			return err
		}
	}
	delete(r.recovered, actorUID)
	delete(r.leases, actorUID)
	return nil
}

func (s *AteomService) waitRuntimeStartup(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.runtimeResources == nil || s.runtimeResources.startup == nil {
		return func() {}, nil
	}
	select {
	case s.runtimeResources.startup <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.runtimeResources.startup
			return nil, err
		}
		return func() { <-s.runtimeResources.startup }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for bounded runtime startup: %w", ctx.Err())
	}
}

// releaseRuntimeResources must run after all Actor execution has stopped.
func (s *AteomService) releaseRuntimeResources(actorUID string) error {
	if s.runtimeResources == nil {
		return nil
	}
	return s.runtimeResources.release(actorUID)
}

// requiresActorExitProof enables fail-closed lifecycle tracking only for opt-in policies.
func (s *AteomService) requiresActorExitProof() bool {
	return (s.runtimeResources != nil && s.runtimeResources.allocator != nil) || (s.actorCgroups && *memoryRestorePolicy != "legacy")
}

func (s *AteomService) actorExecutionEmpty(actorUID string) (bool, error) {
	if s.runtimeResources == nil || s.runtimeResources.emptyLeaf == nil {
		return false, fmt.Errorf("actor execution hierarchy not captured")
	}
	return s.runtimeResources.emptyLeaf(actorUID)
}

// ensureActorStopped refuses to release ownership while kernel exit is uncertain.
func (s *AteomService) ensureActorStopped(ctx context.Context, actorUID string) error {
	if !s.requiresActorExitProof() {
		return nil
	}
	r := s.runtimeResources
	if r == nil {
		return fmt.Errorf("actor execution hierarchy unavailable")
	}
	if r.verifyHierarchy != nil {
		if err := r.verifyHierarchy(); err != nil {
			return err
		}
	}
	kill := r.killLeaf
	if kill == nil {
		kill = ateomcgroup.KillActorLeaf
	}
	killCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	killErr := kill(killCtx, actorUID)
	empty, err := s.actorExecutionEmpty(actorUID)
	if err != nil || !empty {
		return errors.Join(killErr, err, fmt.Errorf("actor %s still owns runtime resources", actorUID))
	}
	return s.releaseRuntimeResources(actorUID)
}

func (r *runtimeResources) recoveredActors() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	owners := make([]string, 0, len(r.recovered))
	for owner := range r.recovered {
		owners = append(owners, owner)
	}
	return owners
}

// stopActorHostProcess never waits for a task whose kernel exit is uncertain.
func (s *AteomService) stopActorHostProcess(ctx context.Context, actorUID string, cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if s.requiresActorExitProof() && s.runtimeResources != nil && s.runtimeResources.verifyHierarchy != nil {
		if err := s.runtimeResources.verifyHierarchy(); err != nil {
			return err
		}
	}
	_ = cmd.Process.Kill()
	if err := s.ensureActorStopped(ctx, actorUID); err != nil {
		return err
	}
	_, _ = cmd.Process.Wait()
	return nil
}

func (s *AteomService) failedActorProcessCleanup(actorUID string) func(context.Context, *exec.Cmd) error {
	if !s.requiresActorExitProof() {
		return nil
	}
	return func(ctx context.Context, cmd *exec.Cmd) error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.stopActorHostProcess(cleanupCtx, actorUID, cmd)
	}
}
