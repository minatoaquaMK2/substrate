# Runtime resource optimization mission

Updated 2026-10-13 UTC. **Reviewed implementation with explicit acceptance gaps.**
No speedup, latency, density or physical-isolation claim is established.

## Sources and publication scope

- Publication base: `c1ab95dea49bacfdeefcb00dd67b66d27372021c`
- Original tested source: `65097aaff066dcb3769a9e38d9fc57734a0f67ab`
- Cocoon reference: `6d76309f8f31faf14f81b5a6d4af3b1309fc1b01`; independent design and license review in [comparison](docs/research/cocoon-comparison.md)
- Cloud Hypervisor v53 base: `9ed824d6d08df3e96f7d5f50795d9449ac99f431`; independent patch in [dependency instructions](docs/upstream/dependencies/README.md)

This publication includes implementation, tests, reproduction instructions and
aggregate verification summaries. Full original evidence is privately retained;
raw benchmark records, captured host/environment metadata and raw tool output
are not published. No raw-evidence ancestor commits are included in the new
publication history.

Production and test source bytes are retained from the original tested source;
[source hashes](docs/research/verification/source-hashes.json) identify the
changed source and dependency patch. Documentation and evidence references have
been replaced. The public commit and whole Git tree therefore differ from the
original checkpoint; **whole-tree byte identity is not claimed**. Historical
results remain attributed to the original checkpoint. The publication candidate
requires its own final verification and source-hash comparison.

## Workflow status

| Workflow | Status | Implemented and tested | Unmet acceptance |
| --- | --- | --- | --- |
| A Benchmark Suite | **hardware_blocked** | Real kubectl-ate/Glutton adapter; bounded open-loop scheduling; raw terminal outcomes; ownership-safe cleanup; absolute deadlines; snapshot provenance; repeat/tail gates; 41 Python tests and native Glutton protocol smoke | Real cluster trials, cold/warm proof, phase attribution and fixed-SLO density. Synthetic samples are not Actor measurements |
| Shared CPU foundation | **tested** | Concurrent Worker-local allocator, generation leases/recovery, online/effective CPU and memory boundaries, injected topology, race/model/fuzz checks | Kernel lifecycle acceptance remains part of C/D |
| B Control Plane CPU Reservation | **hardware_blocked** | Opt-in node requests, node/Worker startup admission, Actor exclusions and management weight; actual controller/manifest paths | Management responsiveness under saturation and ancestor quota; no hard CPU-time guarantee |
| C Per-Actor CPU Pinning | **hardware_blocked** | Real pre-exec CH/virtiofsd cgroup FD, unchanged quota, required/fallback checks, hierarchy identity, ownership recovery and bounded fail-closed cleanup | Delegated kernel integration and multi-Actor/restart/multi-Worker/cross-node matrix. Whole-Actor fence, not separate vCPU/I/O affinity or node exclusivity |
| D NUMA/LLC Placement | **hardware_blocked** | Shared deterministic strict/preferred policies, sparse IDs, SMT/domain fixtures and optional memory-node constraint | Real multi-NUMA/LLC and memory observations; no migration or shared-page locality claim |
| E mmap CoW Snapshot Restore | **dependency_blocked** | Actual independent VMM private-memory mmap path, strict capability/full-layout gate, immutable cache with durable leases/GC; 11 VMM source tests and cache race/crash tests | Production shared-memfd/external-virtiofs coherence unresolved. Compatible-subset guest isolation/recheckpoint tests also need KVM |

**None of A–E is labeled complete.** The user will supply hardware. Executable
validation scripts and negative prerequisite checks are supplied; E has a
production dependency gap in addition to missing hardware.

## Verification summary

Original tested source `65097aaff066dcb3769a9e38d9fc57734a0f67ab`:

- Full `hack/verify-all.sh` passed, including code generation, modules,
  formatting, full Go lint, API conventions, licenses, metrics, migrations,
  protos, shell and threat checks
- Whole root module: **214 packages evaluated; 2,453 top-level tests passed,
  1 failed and 344 skipped**. Exactly **58 named AF_UNIX test families** were
  explicitly excluded after denied socket operations
- Sole failure: unchanged `internal/tarutil/TestExtractAbsoluteNameStaysInside`,
  also reproduced on pristine `c1ab95dea49bacfdeefcb00dd67b66d27372021c` with matching normalized process
  settings. The ownership operation remains an unresolved baseline failure;
  this is **not a fully green repository test run**
- Four Linux amd64 entrypoint builds and the full Linux arm64 root-module
  cross-build passed
- Python suites passed: **41 benchmark, 9 CoW handoff, 1 delegation-scope and
  3 evidence-classifier tests**. Native Glutton protocol smoke passed
- The benchmark timeout fixture passed 100 repeated checks; the recovered old
  adapter still failed the bounded-drain regression

[Verification and reproduction](docs/research/verification/README.md) preserves
counts, exclusions and limits without publishing raw output. The independent
[CH summary](docs/upstream/dependencies/validation.json) records 11 passing CoW
source tests, source/build checks and the unchanged patch identity. Strict CH
Clippy/fmt retain baseline failures; the named Clippy exception is not a strict
pass. Full VMM guest tests and hardware acceptance remain unrun.

## Validation boundaries

The validation did not provide KVM, a delegated cgroup hierarchy, authoritative
physical topology or a live test cluster. Those prerequisites were neither
fabricated nor bypassed. Portable reproduction requirements are documented in
the verification and hardware runbooks; captured host details stay private.

Nil/default WorkerPool configuration and gVisor stay unchanged. Required modes
reject before guest execution. BestEffort/Preferred report actual enforcement
and reasons; known invalid requests never become support fallback. Actor
bandwidth quotas remain. Nonoverlap is Worker-local; privileged host moves and
overlapping Worker pools are outside that guarantee. Uncertain exits/cache
leases remain quarantined and may need operator reconciliation.

## Handoff and contribution gates

Use [hardware acceptance](docs/benchmarks/hardware-handoff.md), scoped
`hack/runtime-resources/validate.sh`, and [acceptance gaps](docs/research/acceptance-review.md).
Do not enable production CopyOnWriteRequired expecting virtiofs-backed Actors
to qualify. [Production CoW feasibility](docs/research/cow-production-feasibility.md)
explains the remaining architecture work.

The authorized fork is the publication target for reviewable code, tests and
aggregate technical summaries; only a verified receipt establishes publication.
No upstream issue, PR or comment is authorized; no CLA/DCO was signed. The
[seven contribution drafts](docs/upstream/pr-descriptions/) are proposed review
boundaries, not submitted PRs. Human ownership/review, upstream design agreement,
contribution-policy compliance and appropriate hardware evidence remain gates.
Removing opt-in configuration and replacing Workers through normal rollout
restores baseline behavior; existing Actors are not rebound live.
