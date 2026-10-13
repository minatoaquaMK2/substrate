# Draft: Constrain complete Actor process trees before MicroVM execution

Unsubmitted upstream proposal. This implements an **Actor CPU-set fence**, not
one-vCPU-per-core pinning or separate helper-thread affinity.

## Summary

Extend the existing per-Actor cgroup and cgroup-FD process launch. Derive the
actual delegated effective CPU/memory boundaries, intersect CPU allowance and
online CPUs, and apply/read back quota plus cpuset before starting CH or
virtiofsd. New threads/descendants inherit the same kernel boundary.

Use the single Worker allocator for explicit/shared/nonoverlapping assignments.
A fractional request keeps its original bandwidth quota; a 500m Actor receiving
one nonoverlapping logical CPU still has 500m CPU time. Explicit sets may have a
different width from guest vCPU count and must remain inside all boundaries.

Recover populated cgroups before admission and retain uncertain ownership after
failure. Reuse only after the cgroup is empty and removable. CPU IDs are local
host policy and are not stored as portable snapshot placement. Required failure
rejects; best-effort support fallback is observable, while exhausted capacity
never changes the requested sharing policy.

## API and compatibility

The optional typed WorkerPool `microVMResources` fields translate to known
runtime flags; gVisor pools reject MicroVM-only configuration and omission adds
no arguments. The controller/schema validation and regenerated artifacts must
travel with the usable runtime path. No arbitrary argument pass-through, parent
cpuset expansion, or kubelet checkpoint modification is introduced.

Actual resource tests check default-off behavior, fractional quotas, cleanup
proof, partial apply rollback, capacity exhaustion and boundary shrink. Targeted
race tests passed. The delegated Linux integration test examines real inherited
thread affinity and ownership when its prerequisites exist; missing prerequisites
are not evidence of enforcement, and CI-required mode must fail closed.

Real KVM cold boot/restore/checkpoint/restart, multi-Actor/multi-Worker execution,
and end-to-end enforcement remain **hardware-blocked**. No physical-core or
node-exclusive claim is made. See [design](../../design/actor-cpu-placement.md).
Coordinate recovery with [#1473](https://github.com/agent-substrate/substrate/pull/1473)
and [#1706](https://github.com/agent-substrate/substrate/pull/1706); keep guest
compatibility work [#2536](https://github.com/agent-substrate/substrate/pull/2536)
separate from host CPU IDs.

## AI and submission disclosure

AI-assisted implementation, review, tests and draft. A human contributor must
independently understand/review the work, write final commit messages and reply
to reviewers without AI under COLLABORATING. Local checkpoints need reworking
before submission. No CLA was signed or public communication performed.
