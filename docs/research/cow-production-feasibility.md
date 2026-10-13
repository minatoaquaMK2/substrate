# Production CoW restore feasibility

Review date: 2026-10-13. Source-confirmed analysis and proposed integration work,
not a native-filesystem prototype or a live guest demonstration. This document
was reconstructed and source-checked after the development workspace was lost;
historical test results do not validate the reconstructed tree.

## Conclusion

The strict private-memory CH patch cannot serve Actors constructed by Substrate
today. Every Actor uses external virtiofsd for its container root filesystem,
including Actors with no optional volumes. This device transport requires shared
RAM and is outside the patch's private mapping contract.

The most promising semantics-preserving investigation is a native, in-process
virtio-fs device in CH that retains Substrate's existing host filesystem tree and
filesystem migration semantics. The pinned virtiofsd exports library components,
but embedding them safely requires a new device frontend and security review.
No such adapter or device is implemented or demonstrated here.

A standalone private-RAM VM with native block devices is a narrower validation
vehicle. Making that a Substrate Actor backend requires a separate storage and
lifecycle integration. General Actor CoW remains dependency-blocked; live guest
correctness and performance remain unverified without the required hardware.

## Current contract and pins

[`assemble.sh`](../../hack/microvm-assets/assemble.sh) defaults to CH v53.0,
Kata 4.1.0 and virtiofsd 1.14.0. Actual deployments must verify their asset hashes.
The independent patch targets CH commit
`9ed824d6d08df3e96f7d5f50795d9449ac99f431`; its fresh source validation is linked
from the [dependency runbook](../upstream/dependencies/README.md).

[`run.go`](../../cmd/ateom-microvm/run.go) prepares OCI bundles and host overlay
root filesystems, exports them alongside durable, CSI, image and system-info
volumes, and starts virtiofsd. `buildVMConfig` selects shared RAM and `kataShared`.
The read-only `/dev/vda` image is the Kata guest OS, not the container rootfs.
[`overlay_linux.go`](../../cmd/ateom-microvm/internal/kata/overlay_linux.go)
mounts the share in the guest and uses `GuestSharedRootfs` for each container.
Find-paths migration, submount announcement and guest-error handling are part of
that established contract.

[`checkpoint.go`](../../cmd/ateom-microvm/checkpoint.go) captures VM state plus
host rootfs-upper and durable-volume archives. [`restore.go`](../../cmd/ateom-microvm/restore.go)
reconstructs those trees and repoints filesystem sockets. MEMORY fidelity does
not subsume filesystem persistence. [System information](../../cmd/ateom-microvm/systeminfo.go)
also needs regenerated/live content rather than stale snapshotted files.

CH's [filesystem construction](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/9ed824d6d08df3e96f7d5f50795d9449ac99f431/vmm/src/device_manager.rs)
uses vhost-user, and its [configuration](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/9ed824d6d08df3e96f7d5f50795d9449ac99f431/vmm/src/config.rs)
requires shared memory for that device. The
[memory table](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/9ed824d6d08df3e96f7d5f50795d9449ac99f431/virtio-devices/src/vhost_user/vu_common_ctrl.rs)
exports RAM backing descriptors to the backend. Relaxing Substrate's
[compatibility checks](../../cmd/ateom-microvm/internal/ch/cow.go) would not make
private pages coherent between the VMM and an independently mapped daemon.

## Native virtio-fs investigation

A native device must access queues, payloads and completions through the same
GuestMemory mapping used by the VMM. Device and guest writes would then affect
the same Actor-private pages. Running the existing FD-remapping transport in a
thread is insufficient: separate private mappings still have separate dirty
pages.

Kata Dragonball's [queue handler](https://github.com/kata-containers/kata-containers/blob/ddcb1ad8d23cbb4323f86c209f132b89592902df/src/dragonball/crates/dbs_virtio_devices/src/fs/handler.rs)
is an architectural reference for a direct-memory FUSE server. Its
[device snapshot implementation](https://github.com/kata-containers/kata-containers/blob/ddcb1ad8d23cbb4323f86c209f132b89592902df/src/dragonball/crates/dbs_virtio_devices/src/fs/device.rs)
omits backend open handles, inodes and DAX mappings. Device state alone cannot
restore arbitrary Actor filesystem state; a paused vCPU does not erase guest
file references or prove all backend operations have drained.

The [virtiofsd 1.14.0 library](https://gitlab.com/virtio-fs/virtiofsd/-/raw/v1.14.0/src/lib.rs)
exports descriptor, server and passthrough components. Its
[server](https://gitlab.com/virtio-fs/virtiofsd/-/raw/v1.14.0/src/server.rs)
forwards filesystem serialization operations. Reusing this is a plausible way
to preserve find-paths semantics. Matching vm-memory/virtio-queue versions do not
prove compatibility: the [vhost adapter](https://gitlab.com/virtio-fs/virtiofsd/-/raw/v1.14.0/src/vhost_user.rs)
uses `BitmapMmapRegion`, while CH uses `AtomicBitmap`.

Required work:

1. Introduce an explicit native CH backend, queue frontend and capability;
   preserve tag, queue layout and guest device identity, with existing defaults
   unchanged
2. Use one RAM mapping for rings and all payload paths, including zero-copy
   operations; prove device-write dirty accounting. Start with full snapshots
   rather than claiming incremental migration
3. Drain requests before host archives; serialize filesystem handles/inodes and
   queues; restore them before execution
4. Review credential switching, thread-local filesystem state, umask, file
   descriptor limits, capabilities and sandbox policy. The
   [passthrough implementation](https://gitlab.com/virtio-fs/virtiofsd/-/raw/v1.14.0/src/passthrough/mod.rs)
   has process assumptions that need explicit adaptation. Do not disable VMM
   protections to make an embedded backend work
5. Add opt-in Substrate boot integration, private RAM, explicit `thp=false`,
   verified assets, native path rewriting, failure cleanup and lease ownership;
   refine compatibility only for the verified native device
6. Build new compatible golden snapshots. Editing an old snapshot's JSON cannot
   convert shared-memory/external-device state into a different backend

## Alternatives and limits

- Native virtio-blk can support a private-RAM test guest, but a production Actor
  needs OCI-to-block images, writable disk ownership, matching disk snapshots,
  container storage requests and volume semantics. Keeping even one virtio-fs
  mount preserves the shared-RAM requirement. A tmpfs upper additionally needs a
  new ROOTFS persistence mechanism
- Reflinked per-Actor files can preserve coherent shared mappings within each
  VMM/backend pair; different inodes do not provide same-inode RAM page sharing
  across Actors. Shared storage blocks are a different optimization
- UFFD copying populates each VM's memory rather than retaining immutable shared
  source pages. Shared-backing continuation alone does not isolate cloned
  writers. See the inspected [CH UFFD path](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/9ed824d6d08df3e96f7d5f50795d9449ac99f431/vmm/src/uffd.rs)
- A shared I/O window or bounce-buffer proxy would require new validated buffer
  placement/translation, completion, interrupt and snapshot contracts. Turning
  off DAX/cache does not remove descriptor/data access to guest RAM
- Separate MAP_PRIVATE mappings in the VMM and daemon diverge on writes; one
  shared writable mapping across Actors loses isolation. Neither solves the
  requested per-Actor coherence and cross-Actor CoW combination

## Bounded milestones and missing acceptance

First compile a direct-server adapter against CH's memory/bitmap, feed descriptor
chains for a temporary tree and prove private-clone writes and serialized file
reference reopening. That no-KVM experiment has not been performed. Next build
an explicit native device with malformed-request, drain/reset and partial-start
failure tests, initially without DAX.

Before production enablement, use real KVM to exercise A/B filesystem queues
through private writes and repeated checkpoints; include open, renamed and
unlinked files, submounts, refreshed host content and error semantics. Integrate
all Substrate fidelities, multiple containers and volume types. Stress owner
crash, source storage faults, cache GC, clone authorization and restart/cleanup.
Orphan-lease reconciliation and hard cache capacity policy remain operational
requirements.

Measure cold/warm cache, sparse/dense touches and pressure with PSS, private
dirty memory and whole-cgroup increments. Include admission copying/hashing and
filesystem overhead in First Exec. NUMA effects and both supported guest
architectures need real validation. No latency, density or readiness conclusion
follows from source-only tests; current Actors should retain legacy restore.
