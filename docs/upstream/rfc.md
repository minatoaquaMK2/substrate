# Draft RFC: measurable, opt-in MicroVM resource policies

**Local discussion draft, 2026-10-13. Not submitted upstream, not an upstream proposal
accepted by maintainers, and not a performance report. All five requested
workflows still have unmet hardware acceptance criteria. Ordinary Substrate
shared-memory/virtiofs Actors cannot use the implemented private-memory CoW
subset; that production feature remains dependency-blocked.**

## Contribution prerequisites

This work was AI-assisted, including source review, implementation, tests, and
these draft descriptions. A human contributor must independently review and
understand every change, reduce/rework the series with maintainers, write or
rewrite the final commit messages, and respond to reviewers without AI
assistance as required by the current
[COLLABORATING policy](../../COLLABORATING.md#using-ai). Local checkpoint commits
are reproducibility records, **not upstream-ready commits**. Do not list AI tooling as a coauthor or signer, or add AI-assistance
attribution trailers to the Substrate series. No CLA, DCO, upstream issue, PR or comment was submitted; only the authorized fork publication is in scope
by this work. A human must satisfy each repository's current contribution/legal
requirements before sending anything. This draft is not a signature or claim
that a contributor has completed those requirements.

[CONTRIBUTING](../../CONTRIBUTING.md) asks for discussion before substantial
contributions and favors small, coherent reviews. The implementation was
prepared locally under user authorization; upstream submission requires
separate authorization and human ownership. Fork publication is authorized. Architectural risks are discussed
at design level only. Any suspected exploitable security issue must follow the
project's private reporting process, outside a public feature PR.

## Motivation and audited baseline

Baseline Substrate is `be3aa280fad168326e73f75954f746fafc2953c4` on `main`;
Cocoon reference is `6d76309f8f31faf14f81b5a6d4af3b1309fc1b01` on `master`.
The [runtime audit](../research/runtime-resource-audit.md) traces actual
Actor→ateapi→atelet→ateom→CH/virtiofsd calls and cgroup ownership assumptions.
The [Cocoon comparison](../research/cocoon-comparison.md) separates source-backed
behavior from documentation and from unmeasured performance hypotheses.

Substrate already has multi-Actor Workers, per-Actor cgroup-FD process launch,
CPU bandwidth limits, workload benchmarks, snapshot phase logs, and an optional
dedicated control-plane node-pool arrangement. Extend those boundaries rather
than build another global scheduler. Actor process CPU fences, vCPU thread
pinning, helper affinity, Worker-local nonoverlap, and node-exclusive ownership
are distinct capabilities. This series implements the first and Worker-local
allocation; it does not advertise the other guarantees.

## Proposed behavior and current limits

| Area | Local implementation | Important remaining gate |
| --- | --- | --- |
| A: benchmarks | Existing CLI/Glutton adapter, bounded open-loop load, checked guest commands, complete raw outcomes, repeat/tail gates, host observations | Real KVM/cluster runs, controlled cache experiments and optimization comparisons |
| Shared foundation | Immutable sparse CPU sets, synchronized leases, explicit recovery, injectable Linux topology | Runtime restart on real delegated cgroups; fixture correctness is not hardware evidence |
| B: management protection | Separate node and Worker startup limits; opt-in node request component; Worker Actor-excluded CPU set and management weight | Stress-tested management responsiveness, ancestor quota effects and reservation cost |
| C: Actor CPU fence | Pre-exec CPU/memory sets for CH and virtiofsd, existing bandwidth retained, cleanup/recovery gating | Live lifecycle/restart/shrink and multi-Worker enforcement; no per-vCPU/helper split |
| D: NUMA/LLC | Single allocator with deterministic preference/requirement policies and optional local memory-node set | Physical multi-NUMA/multi-LLC/SMT and remote-memory performance measurements |
| E: CoW | Strict private-memory VMM patch, capability/config gates, content-addressed cache with leases, real mmap source tests, guest-test harness | Production shared-memfd/virtiofs incompatibility plus all KVM guest validation |

All production features are opt-in. Omission of `WorkerPool.spec.microVMResources`
and node flags leaves defaults unchanged; gVisor does not receive MicroVM-only
arguments. The typed WorkerPool surface maps supported enum values to known
runtime flags. Schema/controller validation rejects incompatible backend and
field combinations. Runtime checks remain authoritative for real effective
boundaries, delegated controllers, topology and VMM capabilities.

### CPU and admission policy

A Worker derives CPUs from its delegated effective cpuset, online CPUs and
process allowance, subtracts the management reservation, and allocates logical
CPU sets. It never manufactures host CPU IDs from a count or widens an ancestor
quota/cpuset. Shared and nonoverlapping modes remain distinct. The latter is
nonoverlap **within one Worker allocator**, not an exclusive physical core or a
node-wide promise. A 500m request retains 500m bandwidth even if one logical CPU
is unavailable to another Actor under nonoverlap; report that density cost.

The cgroup is configured and read back before CH/virtiofsd execution. Live
cgroups are the recovery record; absent PID bookkeeping is not proof of exit.
Lease reuse requires demonstrated emptiness/removal, and stale generations
cannot release newer allocations. Required support failures reject. Best-effort
initialization reports an actual unplaced mode/reason; capacity exhaustion never
silently turns nonoverlapping allocation into sharing.

Management retains its available parent set. Weight changes are relative CPU
share, not a CPU-time guarantee. A finite shared ancestor can throttle all
children, and ancestors hidden by namespaces remain an explicit limitation.
Node atelet/objectstore-plugin scheduling requests, node startup admission,
Worker startup admission, and cluster dedicated pools protect different scopes.
No kubelet checkpoints, CPU Manager settings, BIOS/SMT or global memory policy
are changed.

### Topology and memory

LLC identity comes from a canonical shared CPU set plus cache level, not a cache
index. NUMA, SMT, sparse/offline CPUs, incomplete topology and fragmented domains
are handled explicitly. Prefer policies expose degradation; Require policies
reject unsatisfied locality. Memory-node selection affects allowed allocations;
it does not migrate old/shared pages. Virtual-machine topology is not evidence
of physical-host topology. CoW clean pages can retain a different first-touch
NUMA location even when the VMM is CPU-local.

### CoW boundary

Pinned CH v53 has no CoW enum. Inspected CH upstream `c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c` has a private CoW path but can
copy unsuitable shared regions; successful API acceptance is insufficient
measurement evidence. Substrate's ordinary virtiofs-backed shared RAM is not
compatible with the independent strict private mapping subset. The dependency
patch provides real mapping code and a capability contract; it does **not**
resolve that production backend boundary or change default asset pins.

See [CoW design](../design/mmap-cow-restore.md) and the
[dependency build/guest instructions](dependencies/README.md). Scope-isolated,
immutable content objects and durable leases protect mappings through cache GC.
Host trust, authorization and guest state hygiene remain prerequisites. This
proposal does not add cross-tenant deduplication, KSM, or external connection
restoration. Compatible production device/backend design requires a separate
maintainer decision before claiming Workflow E complete.

## Upstream coordination before proposing code

Statuses below were read from GitHub metadata on 2026-10-13; recheck before any
submission. These links are coordination context, not results obtained here.

- [Offered-rate load #2350](https://github.com/agent-substrate/substrate/pull/2350)
  and [burst lifecycle draft #2399](https://github.com/agent-substrate/substrate/pull/2399):
  contribute pacing, checked-guest completion and raw-failure coverage to the
  accepted benchmark home rather than land two independent permanent engines
- [Dense-run guidance #2499](https://github.com/agent-substrate/substrate/pull/2499)
  and [KIND reservations #2495](https://github.com/agent-substrate/substrate/pull/2495):
  reconcile deployment prerequisites and distinguish host/node reservation from
  the Worker-local allocator
- [Page-cache eviction #2030](https://github.com/agent-substrate/substrate/pull/2030),
  [node suspend cache #2510](https://github.com/agent-substrate/substrate/pull/2510),
  [local/durable promotion #2545](https://github.com/agent-substrate/substrate/pull/2545),
  and [async checkpoint pruning #2529](https://github.com/agent-substrate/substrate/pull/2529):
  agree one object/lease/GC contract; avoid an unrelated persistence redesign
- [Restarted ateom #1473](https://github.com/agent-substrate/substrate/pull/1473)
  and [lost Worker state #1706](https://github.com/agent-substrate/substrate/pull/1706):
  coordinate orphan lifecycle and admission after restart
- [Restore compatibility #2536](https://github.com/agent-substrate/substrate/pull/2536),
  [CH socket launch #2506](https://github.com/agent-substrate/substrate/pull/2506),
  and [core scheduling #2508](https://github.com/agent-substrate/substrate/pull/2508):
  preserve their separate compatibility, launch and security semantics

## Suggested review series

These are review boundaries, not a requirement that current local checkpoint
commits map one-to-one to upstream PRs. Some shared runtime/API code must be
split or combined with maintainer input so every submitted stage builds and is
useful. Do not submit placeholder flags or advertise an enum as an implemented
runtime feature.

1. [Benchmark measurement contract and bounded driver](pr-descriptions/01-benchmarks.md)
2. [Shared CPU-set, lease and topology foundation](pr-descriptions/02-cpu-foundation.md)
3. [Node/Worker management protection](pr-descriptions/03-management-reservation.md)
4. [Actor cgroup CPU fence and lifecycle](pr-descriptions/04-actor-pinning.md)
5. [Topology placement and separate memory policy](pr-descriptions/05-topology-placement.md)
6. [Strict CoW dependency contract/test contribution](pr-descriptions/06-vmm-cow-subset.md)
7. [Runtime CoW compatibility/cache subset](pr-descriptions/07-runtime-cow-subset.md)
8. A measured integration-results contribution only after hardware gates pass;
   no performance title, summary or promised improvement before then

## Acceptance and experiment plan

The [benchmark runbook](../benchmarks/README.md) documents a finite isolated
run, deterministic guest verification, complete outcomes, randomized repeats,
source/environment manifests, and phase-log reuse. Synthetic runs validate
harness correctness only. Full original raw evidence is privately retained;
the public tree contains [aggregate summaries and reproduction instructions](../research/verification/README.md).
Native Glutton protocol smoke is not a VM lifecycle test. Small-sample P99.9 is withheld; even large samples require
repeated stability evidence under a predeclared descriptive gate.

A real experiment must first verify actual cgroups, inherited task affinity,
controller delegation, effective CPU/memory sets, parent quotas, backend mode,
and resource cost. Then compare equal budgets across baseline, each single
feature and combined features. Include nonzero low/medium/high contention,
bursts beyond capacity, concurrent golden clones, independent snapshots,
sparse/dense faults, CPU and I/O work, create/delete churn, and controlled
snapshot-cache/file-cache states. Report PSS/private-dirty plus aggregate memory
rather than RSS sums. Compare density only under a separate fixed latency and
failure target. Preserve regressions and all cleanup errors.

Before merge, run the project's complete race/lint/generated/license checks on
an environment meeting their prerequisites. Targeted tests are not a claim that
`go test -race ./...` or `hack/verify-all.sh` passes. Privileged cgroup/KVM tests
must fail their CI-required missing-prerequisite branch instead of silently
turning an absent environment into green acceptance. Review actual commands and
remaining gates in [MISSION_STATUS](../../MISSION_STATUS.md).

## Questions for maintainers

- Should the offered-rate/check-output contract extend the pending boomer work
  instead of retaining the standalone finite driver after initial experiments?
- Is Worker-local nonoverlap with external node isolation the intended initial
  ownership scope? Is a whole-Actor process fence the appropriate first API?
- Which recovery owner should reconcile old Actor cgroups and retained snapshot
  leases after ateom/node restarts, using the existing cleanup work?
- Is a strict VMM capability/no-silent-copy contract useful upstream independently
  of Substrate? Which production backend direction, if any, makes private CoW
  compatible without changing existing filesystem/volume semantics?
- Which isolated delegated-cgroup/KVM and physical multi-NUMA test environments
  should gate correctness and any future performance claims?
