# Draft: Add deterministic LLC/NUMA placement on the shared CPU allocator

Unsubmitted upstream proposal. **No measured locality or latency benefit.**

## Summary

Extend the same allocator with `PreferLLC`, `RequireLLC`, `PreferNUMA` and
`RequireNUMA`. Filter by effective CPU availability, management reservations and
existing leases before considering domains. Select deterministically by current
lease usage and CPU-list ordering. A required single-domain fit rejects
fragmentation or unknown topology; a preference records its fallback reason.

Discovery uses injectable Linux sysfs data: online CPUs, socket/core/SMT,
NUMA membership, and cache-level/shared-CPU identity. CPU numbering and local
cache index are not topology. Fixtures include multiple LLCs in one NUMA node,
sparse IDs, multiple sockets, partial siblings, offline CPUs and incomplete or
conflicting data. Exposed VM topology is labeled as such.

`memoryPlacement: Local` constrains future allocations to known selected CPU
nodes within the parent's effective memory-node set. It does not migrate
existing RAM or shared snapshot pages. CPU locality and memory locality remain
separate measurements, particularly for shared file pages first touched on a
different node. No global migration, KSM, BIOS/SMT or scheduler policy changes.

## Dependencies and acceptance

Requires the shared foundation and pre-exec cgroup integration. Do not add a
second allocator. Worker-local nonoverlap remains Worker-local even when a
selected domain corresponds to a whole LLC. Fragmentation tests, strict/prefer
semantics, unknown topology and memory-placement rollback are covered locally;
see MISSION_STATUS for exact executed commands.

Physical multi-NUMA/multi-LLC hardware, migrations/remote-memory counters,
CPU-only versus CPU+memory placement, shared-source first-touch, live hotplug,
and equal-budget/fixed-SLO density comparisons remain **hardware-blocked**.
A passing fixture or CPU-affinity readback does not establish memory locality or
performance. See [design](../../design/actor-cpu-placement.md) and
[benchmark contract](../../benchmarks/README.md).

## AI and submission disclosure

AI-assisted source, tests and draft. A human must review/understand all changes,
author final commit messages and answer reviewers without AI. Local checkpoint
commits are not upstream-ready; no CLA/signature or external submission occurred.
