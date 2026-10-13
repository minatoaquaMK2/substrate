# Runtime resource audit

Audit time: 2026-10-13 UTC. This describes the **unmodified baseline**, not proof that a proposed optimization has run. Implementation and test status belong in `MISSION_STATUS.md` and the benchmark run records.

Evidence labels used throughout:

- **源码确认 / source-confirmed**: inspected implementation at the pinned commit
- **文档声明 / documented**: repository or upstream documentation, including public PR metadata; no independent performance endorsement
- **本机实测 / locally measured**: a command actually executed in this development environment
- **待验证 / unverified**: requires hardware, deployed processes, a dependency, or further testing

## 1. Source, licensing, and contribution baseline

**本机实测.** Substrate remote is `https://github.com/agent-substrate/substrate.git`, default branch `main`, initial HEAD `be3aa280fad168326e73f75954f746fafc2953c4`; the isolated development branch is `runtime-resource-optimizations`. The checkout was clean when this audit began. Cocoon remote is `https://github.com/cocoonstack/cocoon.git`, default branch `master`, initial HEAD `6d76309f8f31faf14f81b5a6d4af3b1309fc1b01`, clean. Do not interpret later working-tree changes by other task stages as original modifications. Default branches were also checked against public GitHub repository metadata on the audit date.

**源码确认 / 文档声明.** Dependency sources checked out for the companion memory audit:

| Repository | Default branch | Inspected ref / SHA | Relationship to baseline |
| --- | --- | --- | --- |
| `cloud-hypervisor/cloud-hypervisor` | `main` | `v53.0`, `9ed824d6d08df3e96f7d5f50795d9449ac99f431` | Asset assembly default |
| `cloud-hypervisor/cloud-hypervisor` | `main` | `c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c` | Current upstream source comparison, not a released v54 binary |
| `cocoonstack/cloud-hypervisor` | `main` | `dev`, `ce2181f0cbf9252e103b12bdc3d0fc903b04d966` | Cocoon fork comparison; `dev` is not repository default |
| `kata-containers/kata-containers` | `main` | `4.1.0`, `ddcb1ad8d23cbb4323f86c209f132b89592902df` | Kernel/image/virtiofsd bundle default |

**源码确认.** [Asset assembly](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/hack/microvm-assets/assemble.sh#L45-L118) defaults to CH `v53.0`, Kata `4.1.0`, bundled virtiofsd `1.14.0`; it verifies the extracted virtiofsd version and writes an asset stamp. Operators can supply different versioned SandboxConfig assets. Thus these source defaults are not evidence of a running cluster's binary. Asset URI, digest, actual `vmm.ping` version/build version, architecture and executable digest must be recorded per experiment. **待验证:** no CH/Kata guest binary was executed in this container.

**源码确认.** Read root `AGENTS.md`, `CONTRIBUTING.md`, `COLLABORATING.md`, `.github/SECURITY.md`, and the `.agents/skills` catalog. No nested instruction file applies to these research documents. The triage skill's read-only discovery applies; its label mutations are not authorized. CRD, flaky-test-publication and pending-review skills do not authorize public writes for this task.

**文档声明.** [Contribution rules](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/CONTRIBUTING.md) require tests, small reviewable changes, discussion before substantial upstream work and a contributor CLA. [Collaboration rules](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/COLLABORATING.md#using-ai) require disclosure of AI assistance, prohibit AI coauthor/trailer credit and AI-generated commit messages, and require a human contributor to understand and review changes and answer reviewers without AI. Local commits produced during implementation are development checkpoints only; the final upstream series needs human-authored commit messages and human review under this policy. A local contribution series is not upstream acceptance; no issue, PR, comment or CLA has been submitted by this audit.

**源码确认.** Substrate and Kata are Apache-2.0; Cocoon is MIT. The source-level comparison is independently written, with no Cocoon implementation copied into Substrate by this audit. If later work copies a substantial MIT portion, preserve its copyright and permission text; Apache-origin dependency changes must retain their existing notices. CH has per-file SPDX licensing and a `LICENSES/` directory; inspect the touched file's SPDX identifier rather than infer licensing from GitHub's repository badge. See [Substrate LICENSE](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/LICENSE) and [Cocoon LICENSE](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/LICENSE).

**文档声明.** Suspected exploitable actor/snapshot isolation defects follow [private vulnerability reporting](https://github.com/agent-substrate/substrate/security/advisories/new), not a public issue/PR draft. This document covers architectural prerequisites and correctness risks; it is not a vulnerability disclosure.

## 2. Actual control and execution path

**源码确认.** The relevant path is:

```text
Client / routed actor request
  -> ateapi lifecycle workflow + persisted Actor/Worker assignment
  -> scheduler selects an ACTIVE Worker with slot/resource capacity
  -> node atelet Run/Restore/Checkpoint RPC
       -> objectstore-plugin for external snapshot transfer
       -> node asset and OCI image preparation
  -> selected Worker Pod's ateom-microvm Unix socket
       -> actor UID lifecycle lock + hosted-actor admission
       -> actor network/tap + host overlay rootfs
       -> virtiofsd host process (one per Actor share)
       -> Cloud Hypervisor host process + REST API
            -> KVM guest kernel + kata-agent
            -> guest OCI containers / workload
```

**源码确认.** Control-plane entrypoints are [`CreateActor`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/controlapi/actor.go#L45), [`ResumeActor` / `ensureWorkerAssigned` / `ensureAteletRestored`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/controlapi/workflow_resume.go), and [`scheduling.Schedule` / `checkRoom`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/scheduling/scheduling.go). Assignment is persisted with optimistic concurrency, and workflows use restartable ensure steps. The scheduler checks sandbox class, ACTIVE state, label constraints, local-snapshot node restriction, actor slots and requested resources; it chooses between two eligible candidates by dominant load. It is not a NUMA allocator.

**源码确认.** [`AteomHerder.Run`, `Restore`, `Checkpoint`, `Terminate`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/atelet/main.go) stage node files, call the selected ateom via `dialAteom`, and transport declared CPU/memory limits. [`snapshot_transfer.go`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/atelet/snapshot_transfer.go) delegates fetch/upload to the node objectstore-plugin through its Unix socket. Atelet is a separate DaemonSet process, not an in-Worker thread.

**源码确认.** [`RunWorkload` / `coldBootActor`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/run.go#L234) and [`RestoreWorkload` / `restoreMemoryFidelity`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/restore.go) launch CH directly. [`LaunchVMM`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/internal/ch/restorefds.go#L59) executes CH with an API socket, deliberately without `exec.CommandContext`, because the VMM must outlive its initiating RPC. Socket readiness honors cancellation and kills/reaps on failure. [`StartVirtiofsd`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/internal/kata/overlay_linux.go#L140) starts the share in find-paths migration mode. Both commands receive the Actor leaf's `SysProcAttr` before `Start`.

**源码确认.** There is **no Kata shim or containerd shim in this MicroVM launch path**. Kata contributes its guest kernel/image, guest agent and imported protocol. [`kata.AgentClient`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/internal/kata/agentclient.go#L88) uses ttrpc over CH's hybrid-vsock socket and calls container/network operations directly. Guest cgroup path `/ateomchv/<container>` is a guest boundary, not the host Actor cgroup.

## 3. Concurrency and the distinct budgets

**源码确认.** A Worker supports multiple simultaneous Actors. [`--max-actors`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/main.go#L78) defaults to **1000**. [`admitActor`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/hosted.go#L55) checks `len(actors)+draining`, retaining a slot during network teardown; a same-UID rehost replaces the entry. Lifecycle locks are keyed by Actor UID, so unrelated Actors can boot, restore, checkpoint and terminate concurrently. There is no baseline Worker-wide restore semaphore. `max-actors=1000` is an admission ceiling, not a statement that a particular node can run 1000 busy VMs.

**源码确认.** Three separate CPU quantities must not be conflated:

1. Worker scheduler budget: [`ateom.FromFiles`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/internal/ateom/register.go#L49) reports downward-API `limits.cpu` (millicores) and memory, once at startup. The [controller](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/atecontroller/internal/controllers/workerpool_apply.go#L188) projects those files. This is Kubernetes configuration, not a measurement of `cpu.max`, effective cpuset or hidden ancestor quota. In-place vertical scaling is explicitly unsupported here
2. Host Actor budget: [`OpenActorLeaf`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/internal/ateomcgroup/actor.go#L44) sets `cpu.max` to `milliCPU*100000/1000`, minimum 1000 microseconds, with 100000-microsecond period; unset is `max`. It does not set cpuset, weight, memory.max or pids.max. If the cpu controller file is absent, baseline code tolerates that absence. CH and virtiofsd both consume this CPU budget
3. Guest shape and container limits: [`SandboxSize.VCPUs`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/internal/sizing/sizing.go#L58) rounds millicores upward to a vCPU count, while guest OCI container quotas are independently written from container limits. `500m` yields one virtual CPU with half-core host-time quota, not one dedicated physical core

**源码确认.** [`guestEnvelope`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/spec.go#L29) explicitly documents no CPU reserve for platform overhead. Memory has a VMM reserve, CPU does not. CH boot/max vCPU counts are equal in [`buildVMConfig`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/run.go#L825); no separate baseline vCPU- or I/O-thread affinity hook is configured.

**源码确认.** Node-side `Restore` overlaps snapshot download with asset/bundle preparation using an errgroup; bundle/image volume work is also parallel. Baseline atelet has no general run/restore concurrency flag. The **stats** sweep alone has a fixed concurrency of 8 in [`statspoller.go`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/atelet/statspoller.go#L63). Objectstore asset staging has an 8-GiB byte budget in the manifest, which is not a CPU or concurrent-restore reservation.

## 4. Host cgroups: traced launch, not inferred solely from YAML

**源码确认.** [`ateomSecurityContext`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/atecontroller/internal/controllers/workerpool_apply.go#L373) now sets **privileged=false** for both runtimes. MicroVM adds the required capability set, AppArmor/seccomp choices, and obtains `/dev/kvm` via the atelet device plugin rather than relying on a privileged hostPath. Earlier assumptions that all MicroVM Workers are privileged are stale at this baseline.

**源码确认.** [`Delegate`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/internal/ateomcgroup/ateomcgroup.go#L58) requires `/proc/self/cgroup` to report `0::/`, treats `/sys/fs/cgroup` as the delegated namespace root, moves root processes to `ateom/`, and enables available subtree controllers one at a time. Failed controller enables warn and continue. If the worker is not at a private cgroup-namespace root, it skips delegation. The predicate is a source assumption; it is not a general proof of mount topology or that this is a Kubernetes Pod cgroup rather than its container scope.

**源码确认.** The private-namespace normal path is:

```text
actual host hierarchy: .../<worker-owned delegated scope>/
  ateom/        ateom management, Go threads, Worker tunnel work
  <actor-uid>/  CH + all its threads, virtiofsd + inherited child processes
```

[`ActorLeaf.SysProcAttr`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/internal/ateomcgroup/actor.go#L101) returns `UseCgroupFD: true`; Linux process creation places the process directly in the leaf before guest execution. This covers new descendant threads/processes by cgroup inheritance. When delegation is skipped, [`actorLeaf`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/hosted.go#L111) is nil and launches inherit ateom's cgroup. CH has no separate cgroup management configured on this path. This fallback must fail closed if an operator asks for required placement.

**待验证.** No actual Worker/CH/virtiofsd PID exists in the development container, so **the concrete deployed host cgroup path is not established**. On a real test node record (from the appropriate namespaces) `/proc/<pid>/cgroup`, cgroup2 mountinfo root/mountpoint, every `/proc/<pid>/task/<tid>/status`, cpuset requested/effective CPUs and mems, cgroup events and parent `cpu.max`. Resolve mount-root offsets; do not concatenate `/sys/fs/cgroup` with a namespaced path blindly. Inspect visible ancestors and separately report ancestors hidden by cgroup namespaces.

**文档声明.** [Linux cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html) is hierarchical: descendants cannot overcome parent limits. CPU weight changes relative contention; a finite ancestor CPU bandwidth budget can throttle management and Actors together. Requested cpus/mems must be checked against effective values. **待验证:** a Worker-only allocator can guarantee non-overlap only among its own leases. Node-exclusive claims require externally non-overlapping Worker CPU allocations, or node-level coordination. [Kubernetes CPU Manager](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/) and [Topology Manager](https://kubernetes.io/docs/tasks/administer-cluster/topology-manager/) remain operator/kubelet concerns; do not edit their checkpoint or cpuset files.

## 5. Existing control-plane protection

| Layer | Baseline protection / gap | Evidence |
| --- | --- | --- |
| Cluster API/controller/router/egress/certificate services | Opt-in `--cordon-control-plane` pins these deployments to a labeled/tainted `ate-control-plane` node pool with best-effort replica spreading | 源码确认: [cordon component](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/manifests/ate-install/components/cordon-control-plane/kustomization.yaml) |
| PostgreSQL | Separate `ate-postgres` pool under the same option; installer checks pool prerequisite. Base PostgreSQL requests 2 CPU and limits 16; size10 uses request 80 CPU, 140 GiB memory, no CPU cap | 源码确认: [base manifest](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/manifests/ate-install/postgres/postgres.yaml#L213), [size10 installer](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ate-setup/internal/steps/postgres.go#L81) |
| Node atelet and objectstore-plugin | Each requests 50m CPU and 128 MiB memory, no CPU/memory limit; atelet Pod priority `ate-node-critical`. It must coexist on Worker nodes | 源码确认: [atelet manifest](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/manifests/ate-install/atelet.yaml#L120) |
| Worker ateom | Dedicated `ateom/` leaf after delegation but no CPU reservation/weight policy; Actor startup orchestration and tunnel servicing share this process | 源码确认: delegation/main/hosted sources above |
| Host kernel/kubelet | No verified current CPU Manager, Topology Manager, system reservation, or node-exclusive CPU assignment in this environment | 待验证 |

**源码确认.** The base API/controller/router deployment manifests do not establish a universal dedicated CPU reservation; install overlays and operator configuration matter. Reuse the existing dedicated-pool mechanism instead of adding another global cluster scheduler. Worker cpuset exclusion does not itself protect atelet from another Worker on the same node. Pod priority affects scheduling/eviction, not a hard CPU-time floor.

## 6. Lifecycle, failure, and restart

**源码确认.** Control-plane lifecycle states and host consequences are separate:

| Operation | Persisted transition / behavior | Host path and lease consequence |
| --- | --- | --- |
| Create | New Actor is SUSPENDED; metadata creation is not guest execution | [actor.go](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/controlapi/actor.go), no CPU lease until activation |
| Resume | SUSPENDED or PAUSED -> RESUMING -> RUNNING; already RUNNING is idempotent | [workflow_resume.go](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/controlapi/workflow_resume.go); assignment, target-node asset preparation, run/restore, then finalization |
| Pause | RUNNING -> PAUSING -> PAUSED, local checkpoint | [workflow_pause.go](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/controlapi/workflow_pause.go); MicroVM checkpoint pauses then snapshots **and tears down**, unlike a permanently resident `vm.pause` |
| Suspend | RUNNING or PAUSED -> SUSPENDING -> SUSPENDED; paused-origin suspend uploads existing local checkpoint | [workflow_suspend.go](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/controlapi/workflow_suspend.go); durable persistence and assignment release |
| Delete | Normally SUSPENDED/CRASHED -> DELETING, with explicit any-state path; terminate, release Worker, detach/delete owned volumes, release snapshots, finalize | [workflow_delete.go](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/controlapi/workflow_delete.go) |
| Worker Pod disappears | Worker drains; bound still-running Actors become CRASHED; already-suspended ones stay resumable | [workflow_worker_delete.go](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateapi/internal/controlapi/workflow_worker_delete.go) |

**源码确认.** [`CheckpointWorkload`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/checkpoint.go#L68) holds the UID lock, deactivates routing, waits for CH, reads final guest stats, pauses the guest, captures VM state/rootfs uppers/durable directories in parallel, lists snapshot files, then calls terminate. A capture error can leave the guest paused; this is not proof of successful suspend. Teardown errors after successful capture are logged best-effort, so a successful checkpoint response alone is not proof all host resources were reclaimed.

**源码确认.** [`teardownActor`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/checkpoint.go#L335) requests CH shutdown with a 10-second timeout, closes guest clients, kills/reaps known CH and virtiofsd processes, then performs cgroup and orphan cleanup. [`KillActorLeaf`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/internal/ateomcgroup/actor.go#L121) writes `cgroup.kill` and waits for `populated 0`; a missing kill file currently returns nil. [`cleanupSandboxState`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/hosted.go#L119) logs kill errors, and `unhostActor` logs failed leaf removal while removing attribution/capacity. A placement allocator must retain/quarantine a lease until independent emptiness/removal evidence succeeds, including kernels without `cgroup.kill`.

**源码确认.** [`kata.CleanupSandboxState`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/internal/kata/cleanup_linux.go#L44) enumerates mounts, lazy-unmounts deepest first, avoids recursive deletion when detach is uncertain, and scans `/proc` for known sandbox processes. This is cleanup fallback, not an adequate new affinity or lease-ownership mechanism. Do not use a transient PID or process-name match as persistent allocator identity.

**源码确认.** Failed Run/Restore has deferred detached-context cleanup; VMM/socket and virtiofsd startup paths kill/reap known partial starts. `endPreviousActivation` stops prior VM state before rehosting. Main installs a child reaper. SIGTERM stops readiness, cancels active startups, allows checkpoint during drain and attempts guest shutdown in [`shutdown.go`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/shutdown.go). Atelet drains gRPC for up to 5 minutes by default. Baseline restart does not reconstruct an in-memory hosted-Actor map or a CPU lease database. `teardownActor` explicitly permits a nil running record after restart. Pending upstream #1473/#1706 below cover related recovery, so new allocator recovery must be tested independently rather than rely on those changes being present.

**待验证.** Required integration cases: cancellations before/after each child launch, lost response then retry, reused UID with old processes, stale empty/nonempty leaf, PID reuse, Worker restart in the same/different cgroup namespace, missing `cgroup.kill`, cgroup removal timeout, CPU hot-unplug/parent shrink and two Workers with overlapping parent sets. A lease must not be released simply on PAUSED/SUSPENDED metadata.

## 7. Snapshot, cache, restore and first execution

**源码确认.** Snapshot fidelities differ: MEMORY captures guest state + memory + rootfs uppers + durable directories; ROOTFS restarts the guest with captured uppers; VOLUMES cold-boots with durable volumes. Only MEMORY is a RAM restore. The baseline's VM files include `config.json`, `state.json`, `memory-ranges`, `base-id`, and applicable tar files. External uploads go through objectstore-plugin; a manifest carries runtime asset records and file lists. Local pause snapshots use node-local snapshot directories and preserve the directory on restore. Current external restore downloads into per-Actor `restore-state`; no shared immutable RAM cache is established by this path.

**源码确认.** [`sandbox_assets.go`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/atelet/sandbox_assets.go) already handles verified asset downloads and atomic publication, and [`internal/filecache`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/atelet/internal/filecache/doc.go) contains keyed immutable artifact-cache primitives (atomic rename, singleflight, read-only data, hardlink or private-copy consumers). These are reusable concepts, **not evidence that snapshot RAM currently flows through a shared cache**. Snapshot metadata such as rewritten sockets must remain per-Actor writable copies even if RAM gets a shared backing inode.

**源码确认.** MEMORY restore reconstructs volumes/rootfs at deterministic virtiofs find-paths locations, rewrites VM socket paths, allocates fresh tap FDs, launches both children inside the leaf, then calls [`RestoreWithNetFDs`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/internal/ch/restorefds.go#L89). Its REST request sends `memory_restore_mode` and tap descriptors via SCM_RIGHTS. CH restores paused; ateom separately calls Resume, dials kata-agent, reseeds guest randomness, waits for configured wakeup probes, activates routing and publishes the running record.

**源码确认.** [`restoreMemMode`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/restore.go#L45) uses [`PrefaultsUnconditionally`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/internal/ch/prefault.go): CH below v53 uses `OnDemand`; v53 onward and unknown versions use `Copy` because of background-prefault/snapshot-gating behavior. The upper bound is currently open. This is a conservative version heuristic, not capability negotiation. Explicit unsupported `CopyOnWrite` must not be added based only on Cocoon's enum spelling.

**源码确认.** OnDemand source RAM stays alive for the VM's lifetime. A later sparse snapshot is merged with the restore base via [`MergeSparseOverlay`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/internal/ch/merge.go) and `mergeOnDemandDelta`; preserved local sources use the preserving merge variant. Eager-copy snapshots are self-contained; [`maybeDropStagedMemoryImage`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/restore.go#L528) removes temporary memory files only after successful Copy restore, except preserved sources. Copy/OnDemand lifecycle logic cannot be reused for mmap without examining subsequent checkpoint semantics.

**源码确认.** Guest memory is explicitly `Shared: true` (memfd) because the Actor uses external virtiofsd. Actual upstream CoW rejects file-backed shared regions: see [upstream memory manager](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c/vmm/src/memory_manager.rs), `new_from_snapshot`, `is_restore_cow_compatible`, `mmap_cow_saved_regions` and `do_mmap_cow_saved_regions`. Vhost-user uses shared region FDs in [vu_common_ctrl.rs](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c/virtio-devices/src/vhost_user/vu_common_ctrl.rs). A private file overlay visible only to CH would not make virtiofsd see the same private writes. **待验证 / dependency blocker:** a newer binary or enum alone cannot deliver mmap CoW for the baseline shared-memory MicroVM; a supported private-memory/device path or real coordinated VMM/backend modification is required.

**源码确认.** Existing timing logs split VMM launch, `vm.restore`, Resume and wakeup-probe stages, with separate node transfer/bundle stages. These are useful but **not identical to First Exec**. A deterministic guest command/endpoint with validated output must finish after the external request arrives, retaining scheduler wait, resource wait, network readiness and demand faults. Golden/prewarm claim, initial cold boot, first actual restore, later Actor restore and command completion require different record types. MicroVM usage stats are [guest-reported](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/stats.go#L94); they exclude host management/virtiofsd cost, so benchmark host cgroups separately.

**待验证.** Golden authorization, guest secrets and entropy, A/B isolation, mmap source integrity/truncate protection, recheckpoint after private writes, multi-round restore and node cache GC require explicit tests. Reseeding exists but follows Resume; do not advertise stronger cross-clone entropy ordering than the implementation provides. No KSM, global cache dropping or global memory migration is authorized by this work.

## 8. Current public work and overlap

**文档声明.** These states were fetched read-only from GitHub PR metadata on 2026-10-13 at approximately 07:32 UTC; `state=closed` alone was not treated as merged. The metadata's `merged` field was checked. Open PR results are context, not benchmark measurements from this task.

| PR | Observed status | Relevance / recommended relationship |
| --- | --- | --- |
| [1836](https://github.com/agent-substrate/substrate/pull/1836) Multi-Actor Workers | Merged, `14c0c136bc3fda4d8e67de3851c087a28a2e754b` | Extend the existing admission/cgroup path |
| [2350](https://github.com/agent-substrate/substrate/pull/2350) Offered-rate resume load | Open | Coordinate benchmark pacing; its fake data plane does not validate guest First Exec |
| [2399](https://github.com/agent-substrate/substrate/pull/2399) Burst resume/suspend | Open draft | Reuse workload concepts; retain failures and distinguish golden from later resume |
| [2495](https://github.com/agent-substrate/substrate/pull/2495) KIND_RESERVED_CPU | Open | Node kubelet/system reservation overlaps deployment prerequisites, not Worker lease allocation |
| [2499](https://github.com/agent-substrate/substrate/pull/2499) Dense-run guidance | Open | Reconcile deployment prerequisites before upstream submission |
| [2030](https://github.com/agent-substrate/substrate/pull/2030) MicroVM page-cache eviction | Open | Coordinate before mmap work; never truncate a live mapping source |
| [2510](https://github.com/agent-substrate/substrate/pull/2510) Node suspend cache | Open | Avoid a competing cache/GC architecture |
| [2545](https://github.com/agent-substrate/substrate/pull/2545) Local+durable/promotion API | Open | Snapshot persistence API is moving; avoid broad proto churn |
| [2529](https://github.com/agent-substrate/substrate/pull/2529) Async checkpoint pruning | Open | New mapping leases must survive response/cleanup overlap |
| [2506](https://github.com/agent-substrate/substrate/pull/2506) Prebound CH API socket | Open | Launch helper conflict, no duplicate socket optimization |
| [2508](https://github.com/agent-substrate/substrate/pull/2508) Core-scheduling cookie | Open | Security scheduling knob is distinct from CPU affinity |
| [2536](https://github.com/agent-substrate/substrate/pull/2536) Runtime-compatible restores | Open | Host CPU IDs are local policy; preserve runtime/guest compatibility separately |
| [1473](https://github.com/agent-substrate/substrate/pull/1473) Recycle restarted ateom | Open | Do not assume restart recovery is already solved |
| [1706](https://github.com/agent-substrate/substrate/pull/1706) Reclaim lost Worker state | Open | Coordinate orphan cgroup/lease cleanup with node cleanup |

**源码确认.** Current local log includes merged #2057 (DurDir benchmarking) and #2540 (objectstore-plugin rename); use actual baseline names. No open PR above was fetched/merged into the development tree by the audit. Search terms included `CPU`, `cgroup`, `snapshot`, `microvm`, all states; this is a relevance survey, not proof there is no unindexed or future overlapping change.

## 9. Environment and what remains unverified

**本机实测.** Initial development environment is a restricted Linux container, observed process CPU allowance `0-8`, effective capabilities zero, no `/dev/kvm`, no accessible `/sys/devices/system/cpu` topology or cgroup2 mount. The initial `go` on PATH was not a usable compiler; the toolchain workstream subsequently provisioned an isolated official Go toolchain and records its verification separately. These observations constrain what can be tested, not the user's deployment.

**待验证.** Physical host CPU count/SMT/core IDs/socket IDs/LLC/NUMA, bare-metal versus virtualized underlay, nested virtualization, CPU Manager policy, actual kernel cgroup delegation and Worker parent quotas, VMM execution and memory placement are not established here. Nine visible allowed logical IDs do not prove nine physical cores. A synthetic sysfs fixture tests parsing/selection, not hardware locality. No CPU-reservation speedup, tail-latency improvement, NUMA advantage, memory-density gain or restored-VM correctness is claimed by this audit.

**待验证.** A real bounded test requires an authorized Linux KVM machine or cluster, delegated cgroup v2 cpu/cpuset, proc/sysfs access, exact runtime assets, sufficient disk/memory, synthetic workloads and existing cluster credentials. For multi-NUMA/LLC claims it additionally requires measured physical topology. Test source can be delivered now; skipped hardware runs remain blockers. Do not request whole-machine CPU policy changes as a substitute for a scoped delegated test.

## 10. Design constraints established by the audit

**源码确认 -> design consequence.** Reuse pre-exec Actor leaves; preserve CPU quota; keep defaults off; fail required requests if effective cpus/mems cannot be verified. Exclude operator-declared management CPUs only inside the valid parent boundary, and keep management broadly schedulable unless an explicit policy says otherwise. Worker-local non-overlap, CPU quota, vCPU count, SMT completeness, memory placement and node exclusivity are independent properties.

**源码确认 -> design consequence.** Persist/reconstruct ownership from durable scoped cgroup evidence, reject overlap on startup, and quarantine uncertain cleanup. Refresh effective topology/boundaries before allocation and react explicitly to shrinkage. Never infer free CPUs from a cleared Actor map or a returned checkpoint RPC. Explicit host IDs are not portable snapshot metadata.

**待验证.** Completion of the five requested workstreams must be tracked separately: source audit is complete only as an audit; real cgroup/KVM/performance acceptance is still dependent on the unavailable environment, and shared-memory mmap needs an actual dependency/runtime solution.
