# Draft: Gate strict CoW restore and protect immutable snapshot sources

Unsubmitted upstream proposal. **Ordinary Substrate Actors are not eligible for
this private-memory subset. Workflow E is not complete.**

## Summary

Add explicit legacy/preferred/required restoration policy at the real VMM
restore call. Default legacy behavior and asset pins remain unchanged. A strict
capability plus compatible snapshot/device configuration is required; preferred
fallback reports actual mode/reason and required incompatibility errors.

For a compatible private-memory fixture, use scope-isolated content-addressed
memory objects, integrity verification, atomic publication, private restore
views, durable hardlink leases and coordinated GC. Mutable snapshot metadata is
kept private; memory clones refer to the same immutable inode. A disk reflink
alone is not claimed to provide page-cache sharing. Cache admission/copy/hash
cost remains inside measured restoration, not omitted for a favorable result.

Release leases only after process cleanup and authoritative cgroup emptiness.
Crashes retain uncertain leases instead of guessing from recycled PIDs. Warm
retention and source-lifetime tests do not constitute a hard disk-capacity or
complete orphan-reconciliation policy.

## Production boundary and coordination

Substrate's current shared-memfd/virtiofs backend needs shared access within the
VMM/device processes and is incompatible with this private mapping subset.
Do not bypass those checks, label fallback as CoW, or publish this as a finished
production restore optimization. A compatible backend/device design requires
maintainer approval and is outside an enum/cache-only change.

Coordinate with [#2030](https://github.com/agent-substrate/substrate/pull/2030),
[#2510](https://github.com/agent-substrate/substrate/pull/2510),
[#2545](https://github.com/agent-substrate/substrate/pull/2545), and
[#2529](https://github.com/agent-substrate/substrate/pull/2529) before choosing a
single cache/lifecycle owner. Preserve existing network, volume, guest-device
and compatibility semantics. Do not add cross-atespace deduplication or KSM.
Keep sensitive security assessment details in the private reporting process.

## Verification and remaining gates

Local cache/policy tests exercise real OS MAP_PRIVATE isolation, source
integrity/lifetime, scope separation, repeated checkpoint bytes, concurrent
lease/GC, rollback and corruption failures. Consult MISSION_STATUS and the
[CoW design](../../design/mmap-cow-restore.md) for actual commands/results.
Production shared-device compatibility, real guest A/B clones and recheckpoint,
service/node crash recovery, live storage faults, multi-node restoration,
PSS/private-dirty/aggregate density, pressure and NUMA effects remain unverified.
No performance improvement is claimed. The synthetic/private VMM harness cannot
stand in for Substrate guest lifecycle acceptance.

Rollback is `Legacy`, unchanged assets, and deliberate teardown before cache
retirement. Never delete uncertain live leases merely to make a cleanup check
green. See [dependency instructions](../dependencies/README.md).

## AI and submission disclosure

AI-assisted implementation, review, tests and draft. A human must independently
review/explain the code, write the final commit messages and answer reviewers
without AI as COLLABORATING requires. Checkpoint commits are not upstream-ready.
No CLA, DCO, upstream submission or external message was made.
