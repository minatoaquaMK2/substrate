# Cocoon and Substrate: source-level runtime comparison

Audit date: 2026-10-13 UTC. Substrate baseline `be3aa280fad168326e73f75954f746fafc2953c4`; Cocoon baseline `6d76309f8f31faf14f81b5a6d4af3b1309fc1b01`. See [runtime resource audit](runtime-resource-audit.md) for Substrate's full paths, environment and open upstream work.

Labels: **源码确认** = inspected pinned source; **文档声明** = documentation/PR metadata only; **本机实测** = executed observation; **待验证** = not independently exercised. This is not a head-to-head performance result.

## 1. What Cocoon actually implements

**源码确认.** Cocoon's default branch is `master`, not `main`; remote `https://github.com/cocoonstack/cocoon.git`, clean initial checkout. It has Cloud Hypervisor and Firecracker backends with a common host VM lifecycle/metadata layer. Substrate's Worker/Actor/Kubernetes budget hierarchy has no direct one-to-one counterpart in Cocoon's standalone host VM scopes.

| Property | Cocoon pinned implementation | Substrate pinned implementation |
| --- | --- | --- |
| Host scope | `cocoon.slice/vm-<id>.scope` by default; configurable parent | Delegated Worker namespace, `ateom/` management leaf and `<actor-uid>/` leaves |
| Child launch | `CLONE_INTO_CGROUP` through `SysProcAttr` | Same pre-exec cgroup mechanism already exists for both CH and virtiofsd |
| Whole-VM CPU fence | Parent `cgroup_cpus` plus per-VM `--cpuset-cpus` | No baseline CPU-set policy |
| Host CPU quota | vCPU-derived quota, weight and one-period burst by default | Actor millicore-derived `cpu.max`; Worker ancestor budget remains |
| Automatic CPU placement | Whole least-loaded LLC domain for `auto`; queue threads prefer distinct physical cores before SMT siblings | No baseline host topology placement |
| vCPU thread affinity | Whole-VM cpuset constrains all threads; the audited optimization is not per-vCPU one-to-one affinity | No separate per-vCPU affinity fields in configured CH CPU model |
| I/O affinity | Writable virtio-blk queue affinity passed in CH config, multi-vCPU only | External virtiofsd share; no corresponding queue affinity configured |
| Restore quota | Child quota temporarily `max`, armed before Resume | Actor child quota enforced before launch and throughout restore |
| mmap restore | `mmap` -> `CopyOnWrite`, eligible plain private guest memory | `Copy` / `OnDemand`, shared memfd guest memory with virtiofsd |
| Restore source inode | Memory linked, writable disks privately copied/reflinked; metadata copied | External snapshot RAM staged per Actor; local snapshot preserved in place |
| Placement persistence | Metadata placement rows across backend namespaces and lifecycle convergence | In-memory Actor map; no baseline CPU leases |

All rows are **源码确认**. Sources: [Cocoon cgroup](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/cgroup/cgroup.go), [launch](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/start.go), [placement](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/placement.go), [Substrate actor leaves](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/internal/ateomcgroup/actor.go), [Substrate memory restore](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/restore.go).

## 2. CPU scope, quota, fence and reservation are different mechanisms

**源码确认.** Cocoon [`ResolveKnobs`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/cgroup/cgroup.go#L68) sets weight to vCPU count, quota to count times period, default period 100000 microseconds, burst to one quota period; `-1` disables burst. It validates kernel ranges. `Prepare` enables controllers, configures scope placement/weight/quota and returns an open directory FD, which the common launch path assigns before `Cmd.Start`. Threads and descendants inherit that scope.

**源码确认.** The parent fence and per-VM placement are separate. [`reconcileFence` / `placeScope` / `checkSubset`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/cgroup/cgroup.go#L291) compare parsed CPU sets against the effective parent; fence shrink checks conflict with running VM placements, and clearing the fence resets stale parent state. Those checks prevent treating accepted writes as proof the kernel granted the requested set. They are good design input, but their fixed host root and parent-management assumptions cannot be copied over kubelet-owned ancestors.

**文档声明.** [Cocoon VM documentation](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/docs/vm.md#cpu-isolation-cgroup-v2) calls its defaults “Guaranteed at N” and separately warns that VMM/virtio work consumes the same quota and parent bandwidth is not fairly partitioned by child weight. This is a quota/share policy name, not proof of Kubernetes Guaranteed QoS, exclusive host cores or real-time latency. No numerical performance statements in that document were reproduced here.

**文档声明.** Its control-plane recipe combines a VM fence with an operator-configured invoking-service `CPUWeight=1000`; it avoids confining all clone/restore work to a single reserved CPU. **源码确认.** Cocoon does not automatically carve out Substrate's ateapi/atelet/ateom layers. Substrate already has optional dedicated control-plane/PostgreSQL node pools, a separate atelet DaemonSet and a Worker management leaf. The intended transfer is the idea of workload exclusion plus truthful protection scope, not a new global host policy.

**源码确认.** [`Prepare(..., deferQuota)`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/cgroup/cgroup.go#L157) writes `cpu.max=max` during clone/restore. [`restoreAfterExtract`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/cloudhypervisor/restore.go#L67) performs restore then `ArmCPUQuota` before Resume. The guest does not intentionally execute uncapped, but host loading is exempt from its child cap. In Substrate, the parent Pod quota would still apply even if the child cap were removed, and platform/tenant attribution would change. This audit recommends **retaining existing caps**, not importing this exemption. A future bounded restore budget needs its own design and measurements.

**待验证.** A management CPU exclusion only removes competition from the allocator's own Actors. It does not prevent other Worker Pods, host services or SMT siblings outside that scope from competing. High weight cannot overcome a throttled ancestor. A fair comparison must report CPU reservations and any loss of density alongside latency.

## 3. LLC placement and queue affinity, with important limits

**源码确认.** [`ReadTopology`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/cgroup/topology.go) intersects online CPU IDs with a fence, groups by last-level-cache shared lists, and builds physical-core sibling groups. Missing cache sysfs forms a single domain. `Place` chooses the domain with the least sum of placement load; within it queue pins rank distinct physical cores before additional SMT siblings, then by load and CPU ID. If queues exceed available threads, fewer pins are returned for later round-robin application.

**源码确认.** [`placeRecord`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/placement.go#L16) performs read/choose/write inside a metadata write transaction. Placement counts include peer backend namespaces. `CPUSet=auto` stores the entire chosen LLC domain, while QueueCPUs may be a narrower subset. Explicit CPUSet bypasses automatic selection. A VM without queue pins contributes its CPUSet to load. This is locality-aware load spreading; it allows overlap and is **not an exclusive CPU lease allocator**.

**源码确认.** The audited topology reader derives the cache index name from one CPU then reads that index on others, and caches topology with `topoOnce`. A heterogeneous/partial/hotplug environment needs additional per-CPU cache-identity and boundary refresh handling. The requested Substrate allocator must not assume the same index denotes the same cache level/type everywhere or ignore a later effective-parent shrink. NUMA node and memory policy are not implied by LLC grouping.

**源码确认.** CH queue placement is a stable configuration interface, not one-shot `/proc` thread-name scanning. [`api.go`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/cloudhypervisor/api.go), [`args.go`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/cloudhypervisor/args.go) and [`patch.go`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/cloudhypervisor/patch.go) carry `queue_affinity`; writable virtio-blk queues on multi-vCPU VMs use it on start/clone/restore and supported disk hot-add. Read-only disks and single-vCPU cases omit it. Host placement is rederived on launch, and restore config discards source queue affinity. Substrate's main data path is virtiofsd, so the exact disk-queue optimization is not a drop-in virtiofsd policy.

**源码确认.** [State convergence](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/state.go) clears placement when leaving running state, while stop tests specifically retain placements when stopping a still-live VMM fails. [`cgroup.Remove`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/cgroup/cgroup.go#L201) is called only after VMM death is established, kills scope leftovers and waits for removal. Substrate needs equivalent lease retention on uncertain cleanup, including auxiliary virtiofsd descendants.

**待验证.** Neither audited source alone establishes NUMA locality of pages or performance on this environment. CPU affinity is not page migration. Shared mmap source pages may reside on a different NUMA node due to the first faulting process. Report CPU-node placement, memory policy and page residence separately, and measure remote accesses where permitted.

## 4. The real mmap path and why Substrate cannot simply enable it

**源码确认.** Cocoon [`utils.go`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/cloudhypervisor/utils.go#L34) maps user `ondemand` to CH `OnDemand`, and user `mmap` to CH `CopyOnWrite`. This names an API value, not its implementation. [`resolveRestoreMode`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/cloudhypervisor/restore.go#L122) defaults eligible plain private snapshots to mmap; hugepages or shared snapshots default to Copy and explicit mmap downgrades to Copy with a warning. This fallback behavior is weaker than the requested Substrate required-mode semantics, which must reject an explicitly unsupported mode.

**源码确认.** Cocoon's [`doctor/check.sh`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/doctor/check.sh#L22) installs a rolling `dev` release from **cocoonstack/cloud-hypervisor**, verified against its SHA256SUMS, with minimum major 54; ch-remote comes separately from upstream v53.0. Repository default for that fork is `main`, but the comparison checked out `dev` at `ce2181f0cbf9252e103b12bdc3d0fc903b04d966`. This is a fork dependency, not a lock to a released upstream v54 binary.

**本机实测 / 待验证.** The companion dependency audit's read-only fetch for upstream tag `v54.0` returned no such ref on 2026-10-13. Current upstream `main` at `c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c` contains CoW source; that does not make v54 a released asset. The baseline Substrate asset assembly pins CH v53.0 at source commit `9ed824d6d08df3e96f7d5f50795d9449ac99f431` and does not provide this CoW mode. Record the exact source commit, binary digest and feature contract, not merely `major >= 54`.

**源码确认.** In current [upstream CH memory_manager.rs](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c/vmm/src/memory_manager.rs), restore reaches `new_from_snapshot`, `mmap_cow_saved_regions`, compatibility checking and `do_mmap_cow_saved_regions`; the latter maps saved file extents with `MAP_PRIVATE | MAP_FIXED`. This is a real private mapping implementation. Compatibility rejects hugepages and existing file-backed shared regions (`file_offset().is_some()`). The [fork implementation](https://github.com/cocoonstack/cloud-hypervisor/blob/ce2181f0cbf9252e103b12bdc3d0fc903b04d966/vmm/src/memory_manager.rs) must also be assessed at its own pin; fork diff-snapshot behavior is not automatically the upstream API contract.

**源码确认.** Substrate [`buildVMConfig`](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/cmd/ateom-microvm/run.go#L825) sets Shared=true. CH uses a memfd/shared mapping so external virtiofsd can access guest memory through vhost-user region FDs. [`update_mem_table`](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c/virtio-devices/src/vhost_user/vu_common_ctrl.rs) relies on file-backed region offsets/FDs. Turning the CH side into a private file mapping alone would separate its writes from the backend's view. Clearing `shared` or pretending an eager fallback was mmap is invalid.

**待验证 / dependency blocker.** The supported private-memory CoW subset can be tested using a VMM/device layout that supports it, but **it does not fulfill production shared-memory Substrate mmap restore**. That requires a genuinely coherent VMM/virtiofs solution or a deliberately redesigned compatible device path. The task forbids treating enum/config/test scaffolding as completion. A backport for compatible private-anonymous VMs should remain an independent dependency patch; default Substrate behavior stays unchanged and shared-memory requests must report the actual reason for refusal/fallback.

## 5. Immutable sources, sharing and GC

**源码确认.** Cocoon [`cloneSnapshotFiles`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/cloudhypervisor/direct.go#L54) classifies memory separately from writable disks and metadata. [`CloneSnapshotFiles`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/hypervisor/utils.go#L350) hard-links memory files, falls back to a symlink on EXDEV, privately copies metadata, and copies/reflinks writable disk files. Same-inode memory mappings can share file-page cache; separate reflinked disk inodes are not evidence of shared RAM pages. The symlink fallback also has a different deletion lifetime from a hard link and requires source reference protection.

**源码确认.** [`snapshot/localfile.DataDir`](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/snapshot/localfile/localfile.go#L88) acquires a shared file lease and rechecks existence; delete/GC require an exclusive lease. Builds hold their own lease before publication. [GC](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/snapshot/localfile/gc.go) revalidates candidates after locking, skips leased snapshots and reclaims stale transient lease files; [teardown](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/snapshot/localfile/teardown.go) records/reconciles deletion phases. These are stronger lifecycle concepts than simply counting live API requests.

**源码确认 -> design consequence.** A Substrate snapshot RAM cache should have a verified immutable identity, atomic publication, read ownership retained until all dependent VM execution stops, and deletion coordination surviving crashes. RAM and writable runtime metadata must be separated. Neither file permissions alone against a privileged process, nor a hash of a mutable URI, proves immutable contents. Verify expected content digests and never truncate or replace a live mapped inode. A hard link protects against unlink, not against writes through another link.

**文档声明.** [mmap semantics](https://man7.org/linux/man-pages/man2/mmap.2.html) support private writes without modifying the backing file; truncating a mapped file can still fault with SIGBUS. **待验证.** Real A/B clone tests must show equal initial contents, isolated writes, intact source, independent resnapshot/restore over multiple rounds, concurrent GC, process crash, bad/truncated input and allocation failures. PSS/private dirty/system or cgroup memory deltas are needed; summing RSS overcounts shared pages.

**源码确认 / 待验证.** Substrate already reseeds the guest CRNG through kata-agent on restore, but after Resume. Cross-tenant golden authorization and credential/random-state review remain required. No additional KSM or host-wide dedup is justified by mmap sharing. Cocoon's opt-in KSM facility is a separate mechanism and is not copied into this work.

## 6. Public contribution history and licensing

**文档声明.** Read-only GitHub metadata on 2026-10-13 confirmed these Cocoon PRs are **merged**:

| PR | Topic | Merge commit |
| --- | --- | --- |
| [159](https://github.com/cocoonstack/cocoon/pull/159) | Default clone mmap path | `36f3d572fbe4852d041bcb9e4e77b339ee0d734d` |
| [163](https://github.com/cocoonstack/cocoon/pull/163) | Default restore mmap path | `e37b718bd5c1a73a125d1a3ed2b904c678474797` |
| [185](https://github.com/cocoonstack/cocoon/pull/185) | VM CPU fence | `3542c8a1d2b7b7f2f2586ce7725135c76af5622b` |
| [187](https://github.com/cocoonstack/cocoon/pull/187) | Restore child quota exemption | `985a0705676960c10078d4496d3c01d4f0bd424b` |
| [215](https://github.com/cocoonstack/cocoon/pull/215) | Install fork builds | `4112a307c1ec12580d233353f9677037a0f78ea3` |
| [235](https://github.com/cocoonstack/cocoon/pull/235) | LLC/queue affinity | `0ac19dcf644d365a7b47105d39c62b07b819b2df` |
| [236](https://github.com/cocoonstack/cocoon/pull/236) | Placement row table | `aa5d60a0dd3105395650fdd3676aa15f79f420ca` |
| [253](https://github.com/cocoonstack/cocoon/pull/253) | Exit/lease convergence | `811870e89b069818437bc47aa083ec3cd324771d` |

**源码确认.** [Cocoon MIT license](https://github.com/cocoonstack/cocoon/blob/6d76309f8f31faf14f81b5a6d4af3b1309fc1b01/LICENSE) requires its copyright and permission notice in copies/substantial portions. This audit is an independent explanation linked to evidence. Later copied code must carry attribution; conceptual inspiration alone does not justify pasting a foreign allocator into Substrate. Substrate's separate [AI contribution policy](https://github.com/agent-substrate/substrate/blob/be3aa280fad168326e73f75954f746fafc2953c4/COLLABORATING.md#using-ai) and CLA still apply. No external issue/PR/comment was published.

## 7. What to adopt, adapt, and measure

- **源码确认 -> design:** adopt pre-exec cgroup placement using Substrate's existing Actor leaf; verify effective boundaries and controller availability rather than inventing CPUs from `runtime.NumCPU()`
- **源码确认 -> design:** adapt locality selection to one transactional allocator with explicit shared/non-overlapping semantics, per-CPU sysfs identity, current parent/online intersection, strict/preferred locality and independently constrained NUMA memory policy
- **源码确认 -> design:** preserve existing tenant and ancestor quotas; keep default behavior opt-in; expose effective mode and downgrade reasons; reject required placement when delegation or constraints cannot be verified
- **源码确认 -> design:** retain CPU ownership until CH, virtiofsd and descendants have stopped; reconstruct/quarantine stale leases after restart; never infer exclusivity beyond the Worker without external proof
- **源码确认 -> design:** reuse snapshot/cache lifecycle concepts, but do not claim shared-memfd CoW until an actual coherent VMM/backend path exists
- **待验证:** benchmark cold/hot file cache, one shared golden versus distinct Actors, open-loop contention, restore storms, sparse/dense retouch and fixed-budget/fixed-SLO density; randomize order and retain timeouts/failures
- **待验证:** no Cocoon-vs-Substrate speed, density or latency ratio was measured here. A claim that either system is faster is unsupported by this audit
