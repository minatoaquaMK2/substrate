# Draft: Make node and Worker management protection opt-in and explicit

Unsubmitted upstream proposal. **No hard CPU-time or node-exclusive guarantee.**

## Summary

Keep the existing dedicated cluster-service/PostgreSQL node-pool mechanism.
Treat the node atelet/objectstore-plugin and Worker ateom as separate protection
scopes. Add bounded startup admission at node and Worker levels; add an optional
node component increasing the existing atelet and native objectstore-plugin
sidecar CPU requests from 50m to 250m each without adding CPU limits.

Worker management reservation excludes declared CPUs from managed Actor leaves.
Ateom retains the parent set; optional `cpu.weight` changes relative share only.
Parent CPU quota is neither removed nor expanded. Finite and hidden ancestor
budgets remain explicit limitations. Startup limit zero and omitted resource
configuration preserve default concurrency and behavior.

The node limiter covers Run/Restore preparation across Workers and their runtime
RPCs. Checkpoint, termination, health and statistics do not require a startup
slot. Context cancellation and all return paths release admitted slots. Worker
placement/fencing requires the shared allocator and cgroup integration stages;
split node-independent changes if maintainers prefer a smaller first PR.

## Validation and operator impact

Targeted Worker resource/startup and node startup race tests passed locally,
including already-canceled contexts with available capacity. The component was
rendered against the actual versioned atelet DaemonSet; it modifies the existing
normal container and native initContainer, preserving names and images.
Real contended management latency, First Exec, throughput, restart and reserved
CPU cost remain **hardware-blocked**. Raising requests consumes schedulable node
capacity and can affect placement; it is not free performance.

Coordinate [KIND reservations #2495](https://github.com/agent-substrate/substrate/pull/2495)
and [dense prerequisites #2499](https://github.com/agent-substrate/substrate/pull/2499).
No global CPU Manager/Topology Manager settings are modified. Roll back by
removing the optional component/flags and draining before changing live Worker
ownership. See [design](../../design/control-plane-cpu-reservation.md).

## AI and submission disclosure

AI-assisted code, tests and draft. Human review/understanding, human-authored
final commit messages, independent human review replies and applicable CLA
requirements remain prerequisites. Local checkpoints are not upstream-ready;
no external submission or legal signature occurred.
