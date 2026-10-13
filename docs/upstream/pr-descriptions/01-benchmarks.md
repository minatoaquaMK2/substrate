# Draft: Measure offered-load lifecycle through verified guest completion

Unsubmitted upstream proposal text. **No microVM performance result is available.**

## Summary

Reuse `kubectl-ate`, HTTP-mode Glutton, and existing phase-log tooling. Add a
finite standard-library Python driver with absolute-time arrivals, bounded
concurrency/queue admission, arrival-based deadlines, cleanup inventories and
raw outcomes for every planned request. Distinguish new activation, durable
restore, golden clones, paused resume, and running guest commands.

First Exec includes queueing through checked guest output. It is a Glutton guest
API operation, not a shell-exec API or VMM restore timer. CPU, disk digest and
sparse/dense RAM operations reuse the existing workload. P99.9 is withheld per
trial and gated on repeated sample count/configuration/stability evidence.
Read-only observations explicitly distinguish load-generator from target-host
metrics and preserve unavailable values.

## Scope and coordination

The generated scenarios cover low/medium/high contention, overload, lifecycle
churn, golden versus per-Actor snapshots, and a bounded resident-density hold.
Cold/hot cache labels need operator preparation evidence; no global cache flush
or deployment mutation is performed. Actual feature mode must come from runtime
logs rather than requested profile names.

Coordinate with [#2350](https://github.com/agent-substrate/substrate/pull/2350),
[#2399](https://github.com/agent-substrate/substrate/pull/2399), and
[#2499](https://github.com/agent-substrate/substrate/pull/2499) before selecting
an upstream home. These measurement pieces may belong in the accepted boomer
work rather than a second permanent engine.

## Verification and unmet gates

- `python3 -m unittest discover -s benchmarking/runtime -v`: passed 41 tests
- Native Glutton smoke checks the actual Go workload's command protocol; no VM
- [Aggregate validation and reproduction instructions](../../research/verification/README.md);
  full raw synthetic evidence is privately retained and not published
- Real KVM/cluster lifecycle, server phase joins, cache preparation, resource
  contention and density results: **not run, hardware-blocked**
- Full project verification must be rerun by the contributor before submission

No improvement percentage or latency/density conclusion follows from these
artifacts. See the [runbook](../../benchmarks/README.md) for prerequisites,
cleanup reconciliation and the output contract.

## AI and submission disclosure

This change and draft were AI-assisted. A human contributor must independently
review/understand it, author the final commit messages and respond to reviewers
without AI under COLLABORATING. Local checkpoint commits are not upstream-ready.
No upstream submission or CLA signing has occurred.
