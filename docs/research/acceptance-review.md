# Acceptance review: benchmarks and CoW restore

Reviewed 2026-10-13 during source reconstruction. This covers workflows A and E
and their executable handoff; it is not hardware acceptance. Fresh results must
identify the reconstructed source hashes. Historical first-delivery logs are
privately retained for provenance, not treated as validation of changed files.
The public tree contains [aggregate verification and reproduction instructions](verification/README.md),
without raw logs or captured environment records.

## A. Benchmark suite

Review sources: [runner](../../benchmarking/runtime/runner.py),
[adapter](../../benchmarking/runtime/adapter.py), their tests and the
[benchmark runbook](../benchmarks/README.md).

The driver offers work independently of completions, bounds outstanding work,
starts deadlines at scheduled arrival and preserves terminal outcomes. First
Exec ends after the deterministic Glutton HTTP operation and request echo are
verified. It includes local queueing and restore time; it is not shell exec,
VMM API completion or a prewarmed-object claim. Distinct server phases require
server-side measurements rather than inferred client timestamps.

Golden trials must retain the same completed tag-owned MEMORY snapshot identity;
per-Actor trials require separate Actor-owned sources. Keep template, snapshot
UUID/URI, fidelity and ownership in raw records and repeat fingerprints. API
provenance alone cannot establish actual runtime fidelity, restore mode or host
inode sharing. A template changed between preparation and resume can change the
server path, so fix templates and collect actual-mode evidence.

Successful-sample percentiles must remain separate from failure/timeout/drop
counts. Repeated ranges require matching configurations and distinct repeats.
P99.9 additionally needs the documented sample and repeat-spread gate; even that
is descriptive stability, not a universal confidence guarantee. Synthetic
self-tests and native Glutton protocol smoke do not establish VM performance.
Cleanup failure stops new work and preserves the recovery inventory; delete
acknowledgement is not proof that all host execution has stopped.

Reproducible source entrypoints:

```sh
python3 -m unittest discover -s benchmarking/runtime -v
go build -o /tmp/runtime-glutton ./cmd/benchmarking/glutton
python3 benchmarking/runtime/smoke_glutton.py --binary /tmp/runtime-glutton
```

Real runs require the [existing isolated cluster setup](../benchmarks/README.md).
The harness does not provision infrastructure or modify host caches/CPU policy.

**Unmet:** real cluster lifecycle acceptance; budget-matched baseline,
individual-feature and combined comparisons; validated target-node cold/warm
cache preparation; prewarmed-claim timing; automatic client/server phase
correlation; physical density, NUMA/LLC, multi-Worker and cross-node results.
Fixed PID observation requires operator-maintained target mappings; advanced
scheduling counters require a separate authorized collector. No performance
conclusion follows from the local synthetic population.

## E. mmap CoW restore

The [independent CH patch](../upstream/dependencies/README.md) reaches the real
MAP_PRIVATE restore path with an explicit strict capability. Its reconstructed
preflight rejects missing memory-manager state, empty/partial/overlapping RAM
ranges and source-length mismatch before replacing mappings. Required mode must
fail rather than silently copy; default Copy/OnDemand behavior remains unchanged.
The fresh no-KVM tests exercise actual production helpers and constructor
selection. Build success is not guest execution.

The [cache](../../cmd/ateom-microvm/internal/snapshotcache/cache.go) creates a
separate immutable-by-ownership memory inode, pins the source bundle, detects
mutation/replacement, hashes contents, publishes atomically and protects objects
with durable hardlink leases. GC never truncates an inode or uses PID guesses.
The [owner-crash regression](../../cmd/ateom-microvm/internal/snapshotcache/cache_crash_test.go)
uses a bounded real child process that acquires then exits without cleanup;
subsequent source truncation and a new cache handle must not destroy its lease:

```sh
go test -race -count=10 ./cmd/ateom-microvm/internal/snapshotcache -run '^TestCrashLease'
```

The trusted runtime must protect the cache and its filesystem. Mode 0400 is not
kernel-enforced immutability against a privileged writer. Metadata admission is
cancellable and each config/state file is capped at 16 MiB; this strict subset
rejects larger metadata instead of reading it without a bound.

[`restore.go`](../../cmd/ateom-microvm/restore.go) marks CoW snapshots
self-contained. [`snapshotVMState`](../../cmd/ateom-microvm/checkpoint.go) must
therefore bypass OnDemand delta merging into the immutable source. CH snapshots
private live memory bytes, including dirty pages, rather than substituting the
source file. No-KVM tests cover this source path across repeated generations;
actual guest recheckpoint semantics still need KVM. Release requires proven
cgroup emptiness; unresolved teardown retains ownership and leases.

The [guest harness](../../hack/mmap-cow/check.py) has separate no-KVM failure tests
and a real x86_64 KVM entrypoint:

```sh
python3 -m unittest discover -s hack/mmap-cow -v
python3 hack/mmap-cow/check.py --required --binary /path/to/patched-ch \
  --kernel /path/to/test-vmlinux --output /tmp/new-cow-run
```

It creates a credential-free initramfs and no network; tests two clones, sparse
writes, repeated checkpoints and dense writes. Every acknowledgement checks the
whole 16 MiB guest region. Injected middle-byte corruption must fail explicitly.
Absolute serial/API deadlines, capped serial diagnostics and cleanup-all error
preservation have no-KVM regressions. Missing hardware must fail with --required
or CI=true. Full-region verification faults the region, so PSS samples here are
functional diagnostics rather than sparse-working-set density measurements.

**Unmet:** current Substrate Actors use shared RAM and external virtiofsd and
cannot use this private subset. No compatible native filesystem backend is
implemented. Real KVM A/B/recheckpoint execution, clone storms, storage-fault and
live-owner-crash testing, production restart/cross-node semantics, cold/warm
pressure tests and whole-cgroup performance/density remain unverified.
Automatic orphan-lease reconciliation and hard cache-capacity policy are also
missing. The [feasibility assessment](cow-production-feasibility.md) describes
possible integration work, not a prototype. Keep legacy restore for current
Actors and report explicit preferred-mode fallback or required-mode rejection.

## Related CPU/cgroup handoff

The [kernel integration test](../../internal/ateomcgroup/placement_linux_test.go)
and [runbook](../benchmarks/cpu-placement-validation.md) require an existing
owned delegated subtree and root only for the specifically scoped test. Use
`TestCPUPlacementKernelEnforcement`; broad package tests include legacy root
cases with different setup behavior. Missing required prerequisites must fail.
No global CPU/kernel setting or parent quota should be changed by the handoff.
A future kernel-test pass still does not prove actual VMM/backend coverage,
management latency under load, memory locality or production Actor CoW.
