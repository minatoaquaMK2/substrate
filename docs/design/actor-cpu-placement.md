# Actor CPU placement

Status: implemented allocator and runtime integration; privileged cgroup/KVM
enforcement and hardware performance validation remain separate acceptance gates.
The implementation is opt-in. CPU-set placement is not a measured performance
claim.

## Scope and terminology

The MicroVM backend already starts Cloud Hypervisor and virtiofsd directly in an
Actor cgroup before either process executes. The placement path configures that
leaf's `cpuset.cpus`, `cpuset.mems`, and unchanged `cpu.max` before process
creation and verifies effective values. Descendant threads inherit the leaf's
constraint. This is an **Actor-wide logical-CPU fence** for its managed host
processes, including VMM vCPU, VMM I/O, and virtiofsd threads.

This does not implement individual vCPU-to-host-CPU affinity, separate I/O-thread
affinity, physical-core allocation, or node-wide exclusivity. The allocator does
not scan thread names or use a one-time `/proc` snapshot to claim lifecycle-safe
per-thread pinning. SMT siblings are topology metadata; sharing a core with an
unmanaged sibling can still cause contention. Different Workers can overlap
unless an external owner such as Kubernetes CPU Manager supplies disjoint
boundaries. The policy name intentionally describes Worker-local nonoverlap.

Managed lifecycles capture the delegated cgroup root's filesystem and directory
identity before recovery or execution. Admission and signaling verify that
identity; each exit proof anchors Actor lookups to the same opened root used
for its identity check. A missing or replaced root blocks release rather than
making an absent Actor look stopped. External managers must not move live
Actor tasks outside the hierarchy or replace/re-root the delegation. Restart
recovery assumes the Worker receives the same surviving hierarchy.

The gVisor path and the default MicroVM path remain unchanged. No host scheduler,
SMT, kubelet checkpoint, cluster scheduler, or node-wide affinity is modified.

## Configuration

MicroVM Worker operator flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--actor-cpu-policy` | `off` | `off`, `shared`, or `nonoverlapping` |
| `--actor-cpu-enforcement` | `required` | Missing support rejects initialization; `best-effort` explicitly logs an initialization fallback to the existing unplaced path |
| `--actor-cpus` | empty | Debug logical-CPU list; bounded by the current effective Worker set and management reservation |
| `--actor-cpu-placement` | `any` | `any`, `prefer-llc`, `require-llc`, `prefer-numa`, `require-numa` |
| `--actor-memory-placement` | `inherit` | `inherit` or `local` memory-node policy |
| `--management-reserved-cpus` | empty | Exclude these CPUs from every managed Actor; management retains its allowed parent set |
| `--management-cpu-weight` | `0` | Optional management leaf relative weight; no hard-time guarantee |
| `--runtime-startup-concurrency` | `0` | Bound concurrent cold boots/restores; zero retains existing concurrency |

The reservation-only configuration fences Actors to the full allowed set minus
reserved CPUs, using shared allocation. Invalid configuration, exhausted
nonoverlapping capacity, unresolved recovery, and a boundary shrink never cause
a silent switch to shared allocation. A placement preference can degrade to
cross-domain or unknown-topology placement, with the requested policy and actual
reason recorded in its lease. A required topology placement rejects admission
when no known single domain fits.

### Logical CPUs, quotas, and fractional requests

The allocator consumes a positive integer width. The runtime supplies the Actor's
guest vCPU width, `ceil(milliCPU / 1000)`, or the existing default guest width when
no size was specified. It retains the original `milliCPU` bandwidth limit in
`cpu.max`; affinity never enlarges the Actor's CPU-time budget.

For example, a 500m Actor uses one allowed logical CPU but keeps its 500m budget.
In nonoverlapping mode that logical CPU is unavailable to other Actors in this
Worker, even though its bandwidth is not fully consumed. This deliberate density
cost is not a claim of one full physical core or node-wide exclusive CPU time.
A 1500m Actor normally receives two logical CPUs and keeps a 1500m budget. VMM
and helper threads compete within the same fence and quota. An explicit debug
set chooses its own width, while retaining the declared bandwidth and existing
guest vCPU count; operators must account for that possible mismatch.

Shared mode permits CPU-set overlap and deterministically favors less-used CPUs
based on active lease counts. Counts are a simple placement tie-breaker, not a
utilization predictor or replacement for the existing Worker admission budget.
Nonoverlapping mode has no oversubscription fallback.

## Unified allocation API

`internal/cpuplacement` has no cgroup writes or process operations. Its immutable
`Set` represents sorted nonnegative logical CPU or NUMA node IDs. `ParseSet`
accepts Linux range-list syntax, canonicalizes duplicates, and rejects malformed
or excessively large ranges. Sparse IDs are preserved; no set is synthesized
from `runtime.NumCPU()`.

`New(Options)` accepts `Allowed`, `Reserved`, `Policy`, `Placement`, `Topology`,
and an opt-in `LocalMemory` constraint with `AllowedMems`.
Allowed CPUs are intersected with the discovery snapshot's online CPUs when
known. The reservation must fit this effective boundary and must leave Actor
capacity. The caller supplies its effective cgroup/affinity/online intersection;
the package cannot discover or enlarge delegated permissions itself.

`Acquire(owner, count, explicit)` atomically chooses exactly `count` CPUs. A
nonempty explicit set must have this width and fit every boundary and policy.
Identical retries return the same lease. Changed requests for an active owner
fail. A mutex protects acquisition, release, recovery, and boundary updates;
callers cannot mutate a lease's internal set storage through returned IDs.

A lease records owner, random allocator epoch plus monotonic generation, selected
CPUs, recommended memory nodes, requested policy/placement, and actual selection
reason. A newer owner incarnation cannot be released by an old generation or a
lease from another allocator. Release is idempotent after the owner has no active
lease. Generations are capabilities for bookkeeping, not authentication tokens.

### Lifecycle transaction and restart

1. Discover the current effective boundary; check/reconcile recovered cgroups
   before admitting Actors
2. Reserve a lease, then configure and verify the empty Actor leaf
3. Start VMM and helpers directly in that leaf before Guest execution
4. On startup failure, destroy partial processes and verify the cgroup is empty
   before releasing its CPUs; retain uncertain occupancy on cleanup failure
5. On suspend/destroy/timeout, confirm all associated processes have exited and
   the leaf can be removed before release; an Actor state or PID alone is not
   evidence of quiescence

`Recover(owner, cpus, mems)` reserves the **observed effective** sets of surviving
running cgroups after Worker restart. A successful recovery returns a fresh
generation. A nonzero lease accompanied by `ErrBoundary` or `ErrOverlap` means
the running occupancy was recorded but requires reconciliation; admission is
blocked. A zero lease with another error means the observation was not recorded.
All populated leaves must be enumerated before allowing new work. Recovered
owners cannot be adopted through `Acquire`: the caller must verify exit and
release them first. This prevents Actor-ID reuse from treating old processes as
a new launch. Recovering the same observation is idempotent.

Recovered live execution also consumes Actor admission slots, without inventing
network or usage attribution. Nonlegacy snapshot lifecycle tracking recovers
these owners even when CPU placement is off and cpuset interfaces are absent.
Run/Restore must prove exit before rehosting or unpacking the next activation.
Uncertain exit retains CPU/Actor ownership, filesystems and source leases; no
new background reclamation guarantee is provided.

`UpdateAllowed` installs the new boundary even when it returns a `BoundaryError`.
An out-of-bound active lease, a now-invalid management reservation, or an empty
boundary blocks **all** acquisition, including otherwise identical retries.
Existing leases remain recorded until process ownership has been reconciled;
there is no rollback to the wider, unsafe boundary. Repeated shrink notifications
stay blocked. Restoring a valid boundary or safely releasing affected leases can
unblock admission. `UpdateBoundary(allowed, mems)` updates CPU and memory-node
constraints atomically. In local-memory mode, an occupied CPU whose known NUMA
node is no longer eligible, or a recovered lease's observed memory nodes outside
the current constraint, blocks admission without changing that lease.
`UpdateAllowed` preserves the current memory-node constraint.

`ActorCPUs()` reports the eligible online Actor pool before subtracting occupied
leases. Reservation-only fencing uses this pool rather than raw current CPU
allowance; newly online IDs absent from the topology snapshot require
rediscovery and recovery rather than guessed topology.

Source-machine CPU IDs are host runtime state and are never portable snapshot
metadata. Restoration on a different Worker makes a fresh allocation there;
guest CPU-feature compatibility remains the existing separate validation.

## Linux topology and deterministic locality

`Discover(fs.FS, allowed)` reads an injectable filesystem rooted at
`/sys/devices/system`. Tests use in-memory files and need no privileged sysfs
access. Discovery reads:

- `cpu/online`; missing/invalid/empty data fails closed
- `cpu/cpuN/topology/physical_package_id`, `core_id`, and
  `thread_siblings_list`
- `node/nodeN/cpulist` for NUMA membership
- Each data/unified cache's `level` and `shared_cpu_list`, selecting the highest
  exposed level as LLC

LLC identity uses the full canonical shared CPU list and cache level. A cache
index is local to a CPU and is never its cache-instance identity. Multiple LLCs
inside one NUMA node, differing index numbers, non-contiguous CPU IDs, multiple
sockets, offline CPUs, and partial sibling visibility are supported. Offline and
parent-disallowed CPUs are excluded from selection. The full LLC identity is
retained even when only a subset is visible. Missing, malformed, or inconsistent
optional data is marked unknown and produces warnings; malformed LLC data is
not silently reinterpreted as a lower-level cache. A changed online set during
discovery rejects the snapshot for retry.

Unknown socket/core/NUMA IDs are `-1`, unknown LLC identity is empty, and unknown
sibling sets are empty. The zero topology value contains no inferred domains.
Provenance is `linux-sysfs-visible`: VM-exposed IDs cannot prove the physical
host's sockets, LLC, SMT sharing, or NUMA locality. No physical-host topology or
exclusivity claim follows from successful discovery.

Placement first filters by the effective boundary, management reservation, and
nonoverlapping occupancy. It then considers known single-LLC or single-NUMA
domains large enough for the request. It selects the least-used candidate, with
the lexicographically lowest CPU list as deterministic tie-breaker. A fragmented
domain does not fit merely because total Worker capacity is sufficient. Explicit
sets must also satisfy required locality. Preference failure preserves the
allowed boundary and records degradation; required failure reserves nothing.

## CPU locality is not memory locality

Default memory placement inherits the parent's effective node set. `local`
filters CPU candidates to known NUMA nodes inside the effective parent
memory-node boundary before applying CPU locality preferences or requirements.
Explicit CPU sets must meet the same constraint. Management reservation CPUs
may live on other nodes; their validity uses the full effective CPU boundary.
The selected memory-node set must be nonempty. Unknown NUMA metadata does not
manufacture node 0. The allocator returns a recommendation;
the runtime owns permission checks, the actual `cpuset.mems` write, and readback.

These settings do not establish that existing guest pages, page-cache pages, or
shared mmap snapshot pages are local. In particular, a shared source first
faulted on another NUMA node may retain remote pages. No global page migration,
KSM, BIOS/SMT setting, or memory-policy expansion is performed. CPU-only and
CPU-plus-memory placements must be measured separately; local CPU selection
does not prove faster CoW restore or First Exec.

## Validation and remaining gates

The pure tests cover parsing and round trips, sparse boundaries, online and
reservation filtering, invalid configuration, shared/nonoverlapping capacity,
deterministic locality, explicit sets, unknown topology, fragmentation, rollback,
same-owner retries, stale generations, allocator epochs, recovery overlap,
repeated hotplug shrink, and simultaneous competing allocations/releases. Linux
sysfs fixtures include multi-socket/multi-LLC nodes, partial/offline siblings,
conflicting metadata, missing cache attributes, and hotplug during discovery.

Run focused tests:

```sh
go test -race -count=10 ./internal/cpuplacement
go vet ./internal/cpuplacement
go test ./internal/cpuplacement -run '^$' -fuzz=FuzzParseSet -fuzztime=5s -parallel=2
```

These are allocator/fixture checks, not proof of real cgroup enforcement, vCPU
affinity, memory locality, or improved performance. Privileged Linux acceptance
must verify every VMM/helper task's `/proc/.../status` allowed CPU list, effective
Actor leaf sets, quota retention, threads created after launch, cleanup failures,
restart recovery, and multiple Workers with overlapping versus externally
disjoint boundaries. Real KVM acceptance must exercise cold boot, suspend,
resume, and First Exec. Multi-NUMA/multi-LLC experiments must measure migrations,
remote memory, throughput, latency distribution, management responsiveness, and
reservation/density cost. None of those hardware outcomes is established by the
pure package tests.

## Reference basis

- [Linux CPU topology documentation](https://docs.kernel.org/admin-guide/cputopology.html), read 2026-10-13
- [Linux cgroup v2 documentation](https://docs.kernel.org/admin-guide/cgroup-v2.html), read 2026-10-13
- [Baseline Actor cgroup integration](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/internal/ateomcgroup/actor.go)
- [Runtime source audit](../research/runtime-resource-audit.md) and
  [Cocoon comparison](../research/cocoon-comparison.md)

The allocator/topology implementation is independently written. It reuses the
existing Substrate cgroup lifecycle rather than importing Cocoon's VM allocator
or changing the global scheduler.
