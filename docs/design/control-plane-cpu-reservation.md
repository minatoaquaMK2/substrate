# Opt-in management CPU protection

## Protection objects

Cluster API, database and routing services already support the `cordon-control-plane` component; retain that operator-owned dedicated-node deployment rather than introducing a second scheduler. Atelet is a separate node-critical DaemonSet process; its pod requests and external node CPU allocation must be configured by the operator. The Worker runtime must not alter that node's cpuset or kubelet checkpoint.

Within a MicroVM Worker, CH and virtiofsd are Actor costs. Ateom and its control, snapshot and recovery goroutines are management costs. An opt-in management reservation excludes an operator-selected CPU set from every Actor leaf before the child starts. Ateom retains the parent CPU set so parallel recovery is not pinned accidentally to one CPU. An optional cgroup `cpu.weight` raises relative scheduling weight; it is not an exclusive or real-time guarantee. An optional startup semaphore bounds concurrent boots/restores without relaxing Actor quota.

## Honest guarantees

A declared reserved CPU is unavailable to managed Actor CH/virtiofsd processes, including descendants and newly created threads. It is not node-exclusive unless an external operator allocation guarantees that. Other Pods and host processes may still use it. Actor milliCPU bandwidth is unchanged; a 500m Actor may occupy a logical CPU set but still has only its 500m quota.

Management and Actors still share the Worker's ancestor quota. A finite or unobservable ancestor can throttle all children, including otherwise idle reserved CPUs. Runtime status must report this limitation, and strict CPU-time protection requires operator-provisioned independent/nonthrottling budgets. The runtime does not disable or raise any parent quota. A CPU-set reservation alone does not advertise protected CPU time.

Invalid sets, empty Actor capacity, undelegated controllers and active leases invalidated by a shrinking effective set fail the required path. Best-effort can keep the existing unpinned path only with a structured reason and only without making a reservation guarantee. CPU-set shrink is checked before each new assignment; existing leases are never silently reassigned.

## Lifecycle

The cgroup tree is the persistent allocation record. Startup reads live Actor leaves before admitting new assignments. Populated leaves with unknown placement block non-overlapping allocation. CPU ownership is not released when an Actor merely changes state, a PID exits, or cleanup starts. It is released only after the relevant cgroup is proven empty and removable. Stale generation tokens cannot release a newer activation's lease.

## Validation required

Unit tests cover flags, quota warnings, invalid reservations, startup cancellation and bounds. Cgroup tests must execute child workloads and inspect all thread CPU allowance and effective sets, with explicit local skips and a strict CI/opt-in precondition failure. Contended management/First Exec comparisons, multiple Workers and restart/shrink under live Guest load require a delegated Linux/KVM environment; this development container cannot establish these measurements.

## Node-management implementation

The optional `manifests/ate-install/components/node-management-reservation`
component raises atelet and objectstore-plugin requests from 50m to 250m each.
It reserves schedulable capacity and increases relative CPU share. It adds no
CPU limit, dedicated cpuset, or global node configuration, and is absent from
all default installations. Operators can adjust this example to measured
management demand. Existing `ate-node-critical` priority is retained.

Atelet's new `--max-concurrent-startups=N` bounds Run/Restore preparation across
all of the node's Workers. Zero preserves the previous unlimited behavior.
The slot includes asset/snapshot/image preparation and the runtime RPC, and is
released on success, failure or context cancellation. Health, termination,
checkpoint and statistics requests do not consume startup slots. Restore total
latency includes the time waiting for a slot; admission logs report resource
waiting separately. This limiter does not reserve CPU time and is independent
of the Worker's `--runtime-startup-concurrency=N`.

These two budgets complement the existing optional dedicated control-plane
and PostgreSQL pools. No kubelet CPU Manager state, Pod ancestor CPU set or
parent quota is changed. Multiple Worker allocators remain independent and
cannot promise node-wide CPU exclusivity.

## Rollback

Omit `microVMResources` from the WorkerPool and roll out the previous/new image
with no resource flags to retain default behavior. Drain active Actors before
changing nonoverlapping CPU ownership or removing a reservation. Remove the
optional node-management component and omit the node/Worker startup flags to
return to the previous requests and concurrency. Never change a live Actor's
CPU set by editing a lease or deleting its cgroup while it still runs.
