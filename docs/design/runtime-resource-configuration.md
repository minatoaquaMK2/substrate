# Typed MicroVM WorkerPool resource configuration

`WorkerPool.spec.microVMResources` is an optional, typed operator configuration
block. The controller translates these fields into the MicroVM runtime's
supported arguments. It does not accept arbitrary command-line arguments,
replace the Worker image, add host mounts, or configure the node scheduler.

Omitting the block preserves the existing generated Deployment and runtime
defaults. A gVisor pool may not specify the block, including an empty block.
The CRD enforces this with CEL against the single `spec.sandboxClasses` entry;
the controller also rejects invalid stored/direct input before applying a
Deployment.

## API shape

The JSON field is `microVMResources`; the Go field is `MicroVMResources`.
This preserves the conventional embedded acronym `VM`. API enums use
CamelCase values independently of the lower-case runtime flags. Numeric values
use bounded `int32`; all optional scalar fields use pointers to retain explicit
presence, including the difference between an omitted value and invalid empty
strings. Zero is meaningful for the numeric options.
No per-Actor CPU allocation is persisted in this WorkerPool spec.

| API field | Default inside block | Values / meaning | Runtime flag |
| --- | --- | --- | --- |
| `cpuAllocationPolicy` | `Off` | `Off`, `Shared`, `NonOverlapping`; nonoverlap is Worker-local logical-CPU allocation | `--actor-cpu-policy` |
| `cpuEnforcement` | `Required` | `Required`, `BestEffort`; missing-support initialization fallback only | `--actor-cpu-enforcement` |
| `actorCPUs` | omitted | Debug CPU range list, at most 4096 characters | `--actor-cpus` |
| `managementReservedCPUs` | omitted | CPUs excluded from Actors, at most 4096 characters | `--management-reserved-cpus` |
| `cpuPlacement` | `Any` | `Any`, `PreferLLC`, `RequireLLC`, `PreferNUMA`, `RequireNUMA` | `--actor-cpu-placement` |
| `memoryPlacement` | `Inherit` | `Inherit`, `Local` | `--actor-memory-placement` |
| `managementCPUWeight` | `0` | Zero preserves current weight; 1–10000 selects relative weight | `--management-cpu-weight` |
| `startupConcurrency` | `0` | Zero preserves unbounded concurrency; positive `int32` limits starts/restores per Worker | `--runtime-startup-concurrency` |
| `memoryRestorePolicy` | `Legacy` | `Legacy`, `CopyOnWritePreferred`, `CopyOnWriteRequired` | `--memory-restore-policy` |
| `memorySnapshotCacheDirectory` | omitted | Absolute private cache directory, required for either CoW policy | `--memory-snapshot-cache-dir` |

Defaults are installed only inside a present block. The API server does not
create a block when it is absent. An explicitly present empty block is valid
only for MicroVM pools and receives the listed static defaults.
When specifying `"Off"` in YAML, keep it quoted so a YAML 1.1 parser does not
interpret it as a boolean.

The controller uses an explicit whitelist mapping, for example
`NonOverlapping` → `nonoverlapping`, `RequireNUMA` → `require-numa`, and
`CopyOnWriteRequired` → `cow-required`. Unsupported enums are rejected; strings are
never interpreted as additional arguments or a shell command.

## Example

This is a spec fragment for a WorkerPool whose image includes these flags:

```yaml
spec:
  sandboxClasses:
    - name: microvm
  microVMResources:
    cpuAllocationPolicy: NonOverlapping
    cpuEnforcement: Required
    cpuPlacement: PreferLLC
    memoryPlacement: Inherit
    managementCPUWeight: 200
    startupConcurrency: 2
    memoryRestorePolicy: Legacy
```

The example deliberately does not guess host CPU IDs. To add
`managementReservedCPUs: "0-1"` or `actorCPUs: "2-3"`, first verify those IDs
exist, are online, fit the effective boundary of **every** selected Worker, and
are suitable for the operator's reservation. CPUs numbered 0–1 are not
necessarily available, adjacent physical cores, or a portable management pool.

Keep Worker Pod requests/limits and Actor bandwidth budgets unchanged during a
placement comparison. A fractional 500m Actor still has 500m bandwidth even
when one logical CPU is assigned. Nonoverlap reduces packing opportunities;
the resulting capacity cost belongs in the benchmark results.

## Validation and runtime responsibility

Admission validates enum values, bounded numbers/strings, CPU-list syntax,
MicroVM-only scope, and these cross-field rules:

- `actorCPUs`, topology placement other than `Any`, and memory placement `Local`
  require `cpuAllocationPolicy: Shared` or `NonOverlapping`
- A management reservation alone is allowed with `Off`; the runtime fences
  Actors to the remaining shared CPU pool
- `CopyOnWritePreferred` and `CopyOnWriteRequired` require `memorySnapshotCacheDirectory`, so a
  future compatible restore never discovers an omitted cache path halfway
  through an otherwise successful mode selection
- Cache paths must be absolute, use simple alphanumeric/dot/underscore/hyphen
  components, and contain neither `.` nor `..` components. Root-only paths,
  environment substitutions, and shell syntax are not accepted

The controller repeats these checks and additionally uses the common CPU parser
to reject reversed ranges, excessive expansion, and overlap between explicit
Actor CPUs and management reservation CPUs. Kernel-dependent facts stay
runtime checks: parent cpuset/affinity, online CPUs, NUMA memory permissions,
quota visibility, cgroup delegation, reservation availability, running-cgroup
recovery, allocator capacity, and VMM/snapshot compatibility. Passing admission
does not prove those prerequisites or any performance benefit.

`Required` is the default enforcement. `BestEffort` allows an explicitly logged
fallback only when initialization support is unavailable. Invalid configuration,
occupied nonoverlapping capacity, and unresolved live leases do not silently
become shared allocations. A `Prefer` locality policy can degrade within the
same allowed boundary with a recorded reason; a `Require` locality policy fails
when a known single domain cannot satisfy the request.

`Local` memory placement filters CPU candidates to known NUMA nodes allowed by
the effective parent memory set before CPU selection. Explicit CPU sets follow
the same rule; management reservation CPUs remain checked against the full effective CPU
boundary. A later memory-boundary shrink quarantines affected live leases and
blocks admission until their execution is reconciled. It does not establish
that existing or shared snapshot pages are local, or authorize global page
migration. Likewise, weight and CPU-set reservation do not guarantee management
CPU time beneath a shared exhausted ancestor quota.

Current virtiofs-backed Actors require shared guest memory and cannot use the
private-memory CoW restore path. `CopyOnWriteRequired` therefore rejects incompatible
restores; `CopyOnWritePreferred` reports its actual fallback. Exposing a typed policy is
not evidence that CoW works for those Actors. See the
[CoW design](mmap-cow-restore.md) for dependency and hardware gates.

The controller creates no cache volume and grants no extra path access. The
operator must supply a writable Worker-private directory with the desired
storage lifecycle. A container-local cache can be lost on Pod replacement; an
existing host-mounted directory can be shared accidentally between Workers.
Do not point multiple independent runtime owners at a shared directory without
the cache's required ownership/lifecycle coordination.

Managed runtime ownership depends on a stable delegated cgroup hierarchy.
The runtime captures its identity, verifies continuity before admission and
signals, and refuses reuse when exit is uncertain. External managers must not
move live Actor tasks out of the hierarchy; restart must present the same
surviving delegation. CPU-off nonlegacy restore still recovers live Actor
occupancy independently of CPU controller support.

## Rollout and rollback

Changing the typed block changes the Worker Deployment's arguments and triggers
the existing WorkerPool rolling replacement/eviction behavior. This is not a
live modification of an already-running Actor's CPU set. Removing the block
removes the additional arguments on replacement Workers and returns to the
binary's defaults. Existing Workers still follow their original arguments until
their normal termination completes. Do not interpret a changed spec as proof
that all Actors have already transitioned.

Kubernetes CPU Manager retains ownership of outer CPU boundaries. The feature
does not modify kubelet checkpoints, request exclusive physical cores, or
coordinate overlapping Workers at node scope. Node-level management reservation
and node-wide startup admission are separate deployment policies.

## Verification

Regenerate with the repository's existing generators:

```sh
go generate ./pkg/api/v1alpha1
```

The source change generates the WorkerPool CRD and pointer-aware deepcopy code.
The existing client/lister/informer generators run but require no changed
output because the top-level resource methods are unchanged.

Tests:

```sh
go test -race -p 1 ./pkg/api/v1alpha1 ./cmd/atecontroller/internal/controllers
go vet ./pkg/api/v1alpha1 ./cmd/atecontroller/internal/controllers
```

The API tests install the actual generated CRD into envtest, exercise defaulting
and CEL rejection, and verify absence of the optional block stays absent. The
controller tests check exact flag mapping, zero intent, unchanged nil/gVisor
behavior, no Deployment changes beyond the requested arguments, rejection before
any API write, and generated deepcopy isolation. They do not require KVM.

On the constrained implementation sandbox, envtest 1.36.2 assets were downloaded
to a task-local data directory. Its route table is unavailable to
kube-apiserver's automatic advertise-address discovery. A test-only launcher
appended `--advertise-address=127.0.0.1` to the official binary; no host network
setting changed. With that documented harness adjustment, both complete packages
passed under the race detector. The unmodified harness failure is retained in
the validation evidence. Real Worker rollout and cgroup/KVM acceptance remain
the separate gates described in [Actor CPU placement](actor-cpu-placement.md).
