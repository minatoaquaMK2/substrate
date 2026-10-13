# Runtime resource implementation results

2026-10-13 UTC. See [MISSION_STATUS](../../MISSION_STATUS.md) for status and
checkpoint hashes. **This delivery does not complete the five hardware/runtime
acceptance workflows. Production CoW has an additional unresolved dependency.**

## What changed

1. **BenchmarkSuite:** a bounded, absolute-arrival open-loop driver using the
   existing kubectl-ate lifecycle and real Glutton protobuf workload protocol.
   It retains every drop/error/timeout, cleanup inventory and client queue delay,
   distinguishes golden/distinct actors and lifecycle phases, and supports
   low/medium/high/overload, storms and sparse/dense memory trials. It never
   presents client timing as an unavailable server/VMM phase. P99.9 requires
   three distinct matching repeats with at least 10,000 successes each and the
   predeclared stability gate. See [benchmark instructions](../benchmarks/README.md)
2. **ControlPlaneCPUReservation:** an opt-in Kustomize component raises existing
   atelet and native-init objectstore-plugin CPU requests without new limits;
   existing dedicated control-plane/Postgres placement stays reusable. Node-wide
   and Worker-local start/restore admission limits are independently opt-in.
   Actor CPU exclusions and management relative weight preserve the full
   management parent set. Shared ancestor throttling can still starve it
3. **PerActorMicroVMCPUPinning:** a common transactional allocator and real
   pre-exec cgroup FD integration constrain CH, virtiofsd and descendants before
   Guest execution. CPU quotas, including 500m, remain unchanged; placement follows the existing rounded guest-vCPU width, with density cost
   disclosed. An explicit debug CPU set can override fence width without
   changing guest vCPUs. Required
   controller/boundary/capacity checks fail closed, recovery quarantines live
   ownership, and cancellation/cleanup cannot reuse an occupied lease. This is
   whole-Actor cpuset placement, not separate vCPU/I/O thread affinity
4. **NUMA/LLC placement:** the same allocator reads injectable Linux topology,
   intersects real allowed sets, distinguishes socket/core/SMT/cache identities,
   and deterministically supports strict/preferred single domains. Unknown
   locality is not fabricated. Optional memory-node constraints are separate
   from CPU placement and do not migrate existing/shared pages
5. **mmap CoW:** an independent CH v53 patch enters the actual VMM restore path
   with MAP_PRIVATE mapping, full range preflight and strict capability marker.
   A supported private-memory subset is executable and source-tested. Runtime
   checks reject unsupported layouts; a scoped content-addressed cache verifies
   bytes, publishes atomically, and coordinates durable hardlink leases with GC.
   **Current production shared-memfd/virtiofs Actors remain incompatible.**
   CopyOnWriteRequired rejects; CopyOnWritePreferred reports the actual legacy fallback

The optional typed `WorkerPool.spec.microVMResources` connects configuration to
real runtime flags through a whitelist. API defaults/CEL/generated deepcopy and
controller checks cover nil/default behavior, gVisor rejection, invalid values
and no Deployment write on invalid input. See
[operator configuration](../design/runtime-resource-configuration.md).

## Verified implementation and publication scope

Original tested source `65097aaff066dcb3769a9e38d9fc57734a0f67ab` incorporates upstream
`c1ab95dea49bacfdeefcb00dd67b66d27372021c`. This is fresh validation of reconstructed code, not reuse of
results from an unavailable earlier tree. [Deep review](deep-review.md) records
counterexamples, fixes and remaining limits.

The public version retains production/test source bytes and the dependency
patch, with [source hashes](verification/source-hashes.json) for comparison.
Documentation and evidence packaging changed for publication, so the public
whole tree is different. Full original evidence remains private; public
summaries do not contain captured host data, benchmark rows or raw tool output.
Final publication-candidate checks must be reported separately.

### Go, generated API and default-runtime integration

At the original tested checkpoint, every root-module package was evaluated with
per-package commands equivalent to:

```sh
env -u NO_COLOR -u OTEL_TRACES_SAMPLER_ARG OTEL_TRACES_SAMPLER=always_on \
  TZ=UTC GOMAXPROCS=4 go test -race -json -p 2 -count=1 -timeout=5m PACKAGE
```

Only the [58 explicitly named AF_UNIX test families](verification/af-unix-exclusions.json)
were excluded, in their respective packages. **214 packages evaluated; 2,453
top-level tests passed, 1 failed and 344 skipped.** The sole failure is
`internal/tarutil/TestExtractAbsoluteNameStaysInside`, an ownership-restoration
failure also reproduced on pristine tested upstream `c1ab95dea49bacfdeefcb00dd67b66d27372021c` with matching
normalized process settings. This is not an unrestricted green repository run;
skips and exclusions are not passes.

The current-upstream baseline comparison covers the 10 package directories
changed since the prior upstream checkpoint plus tarutil: 493 passes, one tar
failure and 127 skips, with six named AF_UNIX exclusions in that smaller scope.
It is not a full current-upstream race run. The prior full baseline and its
separately normalized tracing/color reruns are privately retained. Normalization
only changes child-process environment variables; no production code was changed
to make those environment-sensitive assertions pass.

Full `hack/verify-all.sh` passed, including generated API checks and full Go
lint. Linux amd64 builds of atecontroller, atelet, ateom-microvm and ateom-gvisor,
and the full Linux arm64 root-module cross-build passed. Independent affected-
package race repeats, cache crash tests, allocator model/fuzz cases and negative
admission checks supplement the run. [Verification](verification/README.md)
provides the aggregate summary, scope and portable reproduction commands.

### Benchmark integrity, not VM performance

```sh
python3 -m unittest discover -s benchmarking/runtime -p 'test_*.py' -v
```

All 41 tests passed. New counterexamples cover existing Actor ownership,
failed cleanup, record/observer errors, deadlines and pipe draining, snapshot
provenance and tail comparability. They were also exercised against recovered
first-delivery code as expected failures. A repeated independent review found
a timing-dependent fixture startup race; explicit readiness/cleanup handshakes
fixed it without changing production adapter code. The final fixture passed
100 repetitions and the old adapter still exceeded its 0.5-second bound.

Real native Go Glutton echo/CPU/disk/RAM protocol smoke passed without VM
lifecycle. Synthetic trials exercised arrivals, overload, deadlines, injected
errors and cleanup accounting. These are correctness tests, not Substrate
latency, density or throughput measurements. Full raw trial evidence is
privately retained and is not included in this public tree or its history.

### Independent actual VMM path

See the [version-locked patch and hardware harness](../upstream/dependencies/README.md)
and [aggregate command/status manifest](../upstream/dependencies/validation.json).

- CH all-target source check and debug binary build passed
- Eleven strict CoW source tests passed, including complete-range rejection,
  missing state, clone isolation and recheckpointed private-byte behavior
- Nine no-KVM handoff tests passed; guest C build and exact cgroup-script scope
  guard passed. These do not establish guest correctness or kernel enforcement
- Strict Clippy and formatting report untouched upstream issues. The named
  Clippy-exception run passed; neither strict check is described as green
- The combined patch applies cleanly to official CH v53. Full VMM tests were
  not rerun because the required hypervisor is absent; historical first-delivery
  guest-suite counts are not evidence for the changed patch
- Cache race tests use real private mappings and source/clone isolation, plus
  an abrupt subprocess crash, persistent lease/GC, corruption/truncation,
  pinned source descriptors, bounded metadata and acquisition from a prior lease
- Real KVM/delegated-cgroup/NUMA/LLC and live-cluster acceptance remain unrun

## Review and safety outcomes

Cross-review corrected native sidecar Kustomize targeting, source quota overflow,
missing child cpu/cpuset delegation checks, optional API scalar presence and enum
conventions, bounded failed-stop cleanup, and crash-staging hardlink cleanup.
Regression tests cover the fixes. Source checks remain enabled. The dependency
patch is preserved byte-for-byte; no captured raw evidence is published.

Nonoverlap is only inside one Worker. Overlapping Worker sets, physical SMT
siblings, host services and finite/hidden ancestors limit isolation. Required
reconfiguration fails rather than silently overcommitting. Unknown cleanup
retains leases conservatively, which may reduce availability until reconciliation.
Cache crash-orphan leases deliberately favor safety over reclamation and require
operator reconciliation; automatic hard disk capacity management is not claimed.
Read-only file mode is not protection against an equally privileged writer.
Existing authorization, guest reseeding and device/network/volume semantics stay
in force; no cross-tenant golden sharing is newly authorized.

## Remaining acceptance and rollback

Run [the hardware matrix](../benchmarks/hardware-handoff.md): real delegated
cgroup threads/effective sets, multiple Actors/Workers, restarts, partial starts,
source-to-target restore, allowed-set shrink, saturation/ancestor quota, real
NUMA/LLC and memory observations, and baseline-vs-patched open-loop trials.
Required test modes must fail if prerequisites are missing. No live KVM/cgroup,
physical NUMA/LLC, cold-cache, PSS-density or performance acceptance was run here.

Production CoW additionally needs a coherent shared-memory filesystem/device
solution. Simply clearing `Shared`, using independent reflinked inodes, or
silently copying memory is not that solution. Alternatives and their boundaries
are evaluated in [the CoW design](../design/mmap-cow-restore.md).

All features are default-off or legacy. Remove `microVMResources`, the optional
node component and opt-in atelet arguments, then use normal Worker replacement
to roll back. Existing Actors retain their original placement until termination.
Default SandboxConfig dependency pins were not changed to an unpublished fork.

## Independent design and contribution order

The [Cocoon comparison](cocoon-comparison.md) records pinned source evidence and
licenses. This implementation preserves Substrate quotas rather than adopting
Cocoon's restore quota exemption; uses Worker-local transactional leases instead
of claiming node exclusivity; rejects unknown strict locality rather than making
up a cache domain; and gates CoW against the actual backend rather than equating
a supported enum with coherent shared memory. Cocoon implementation was not
copied into the allocator/cache or the independent dependency patch.

Local stages are benchmark → shared foundation → management → Actor placement →
NUMA/LLC → typed integration → compatible CoW experiment, with safety corrections
as separate checkpoints. [RFC](../upstream/rfc.md) and
[seven staged drafts](../upstream/pr-descriptions/) are unsubmitted proposals. Upstream
submission requires human review, AI disclosure, human-authored/approved
contribution text and the project's applicable CLA/DCO process; no legal agreement was signed and no upstream submission was made.
Only the explicitly authorized fork publication is in scope.
