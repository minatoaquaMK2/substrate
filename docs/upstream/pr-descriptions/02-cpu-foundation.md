# Draft: Add a bounded Worker-local CPU allocation foundation

Unsubmitted upstream proposal; no runtime enforcement or performance claim from
this package alone.

## Summary

Introduce one shared `internal/cpuplacement` foundation: canonical sparse CPU
sets, explicit boundaries/reservations, shared versus Worker-local nonoverlap,
transactional leases with generation checks, recovery from observed cgroups,
and injectable Linux topology discovery. Pure allocation never writes cgroups,
alters quota, configures kubelet, or grants node-wide exclusivity.

Identical active requests are idempotent. Changed requests, insufficient
capacity, overlapping recovered ownership and invalid effective boundaries fail
closed. Recovery records surviving occupancy before admitting anything else;
recovered owners require proven exit before a new lease. CPU-set shrink remains
installed even when it blocks admission. Topology comes from online CPUs,
NUMA maps, socket/core/SMT metadata, and LLC shared CPU lists.

## Dependencies and review boundaries

Land a useful testable foundation before runtime admission. A maintainer may
prefer splitting pure set/lease code from topology or combining discovery with
the topology-policy PR. Keep one allocator for all policies; do not introduce a
second CPU owner. Node-wide coordination is deliberately absent.

Coordinate orphan/restart semantics with
[#1473](https://github.com/agent-substrate/substrate/pull/1473) and
[#1706](https://github.com/agent-substrate/substrate/pull/1706). Existing
multi-Actor Worker admission remains the authoritative higher-level budget.

## Verification and merge gates

Tests cover parsing/round trips, malformed ranges, sparse/online/reserved sets,
concurrency, exhausted capacity, rollback, stale generations, recovery,
fragmentation, unknown topology and changing boundaries. Focused reproduction:

```sh
go test -race -count=10 ./internal/cpuplacement
go vet ./internal/cpuplacement
go test ./internal/cpuplacement -run '^$' -fuzz=FuzzParseSet -fuzztime=5s -parallel=2
```

Consult MISSION_STATUS for commands actually executed and results. These pure
fixtures do not prove cgroup enforcement, physical topology, or speed. Real
restart/shrink, multi-Worker and topology measurements remain hardware-blocked.

## AI and submission disclosure

AI-assisted implementation, tests and draft. A human must independently review
and explain every change, write final commit messages and respond to reviewers
without AI as COLLABORATING requires. Checkpoint commits are not upstream-ready;
no CLA or external submission was made.
