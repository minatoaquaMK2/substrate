# mmap CoW restore: strict private-memory subset

## Status and scope

**Workflow E is dependency-blocked for production Substrate Actors and
hardware-blocked for guest validation. It is not an end-to-end CoW feature or a
performance improvement claim.** The default runtime remains unchanged.

Implemented: independently patched Cloud Hypervisor (CH) v53 real private mmap
restore; strict capability/configuration checks in the runtime restore call;
content-addressed immutable memory objects, durable leases and coordinated GC;
real OS mapping/source tests; a bounded CH API/KVM guest test harness.

Every current Substrate cold boot uses shared memfd guest RAM and external
virtiofsd. That is outside the supported CoW subset. No new public Actor or
backend configuration makes ordinary Substrate Actors eligible. The runtime
integration rejects an explicit requirement and explains preferred-mode
fallback. The generic private-memory CH test is deliberately separate from
Substrate's virtiofs/Kata lifecycle. A maintainer-approved compatible filesystem
backend or new shared-memory architecture is still needed.

## Audited pins and real control path

All observations below are **source-confirmed**, not deployed-binary or guest
measurements. No Cloud Hypervisor, Kata or virtiofsd was originally installed in
this execution environment. `hack/microvm-assets/assemble.sh` defaults to
CH v53.0, Kata 4.1.0 and its bundled virtiofsd 1.14.0; per-architecture asset
SHA256s in SandboxConfig remain authoritative for an actual deployment.

| Source | Audited commit | Ref / role |
| --- | --- | --- |
| Substrate | `be3aa280fad168326e73f75954f746fafc2953c4` | initial main checkout |
| CH pinned | `9ed824d6d08df3e96f7d5f50795d9449ac99f431` | v53.0 tag, upstream default branch main |
| CH upstream current | `c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c` | main |
| Cocoon CH fork | `ce2181f0cbf9252e103b12bdc3d0fc903b04d966` | dev |
| Kata | `ddcb1ad8d23cbb4323f86c209f132b89592902df` | 4.1.0 tag, upstream default branch main |

Cocoon's docs call CoW a v54+ capability; on 2026-10-13 the upstream
`refs/tags/v54.0` lookup returned no such ref. Do not describe that tag as a
verified release. Its installer uses a rolling fork. Its `mmap` configuration
maps to `CopyOnWrite`, and its own configuration logic falls back for shared
RAM/hugepages. No Cocoon implementation was copied. Cocoon is MIT-licensed;
CH's touched source is Apache-2.0, with repository-wide Apache/BSD policies.

Actual CH paths:

1. In pinned [v53 config](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/9ed824d6d08df3e96f7d5f50795d9449ac99f431/vmm/src/config.rs),
   `MemoryRestoreMode` has `Copy` and `OnDemand`, not CoW
2. [Current upstream memory manager](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c/vmm/src/memory_manager.rs)
   `new_from_snapshot` -> `mmap_cow_saved_regions` ->
   `is_restore_cow_compatible` -> `do_mmap_cow_saved_regions` performs
   `mmap(PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_FIXED, snapshot_fd, offset)`
3. That compatibility check rejects regions with a `file_offset`. The upstream
   wrapper silently calls `fill_saved_regions` for an unsuitable layout; an
   accepted API request therefore does not prove CoW happened
4. Substrate `buildVMConfig` sets `Memory.Shared=true`. CH
   `create_ram_region_raw` creates a memfd and maps it `MAP_SHARED`
5. [Vhost-user memory publication](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/9ed824d6d08df3e96f7d5f50795d9449ac99f431/virtio-devices/src/vhost_user/vu_common_ctrl.rs)
   `update_mem_table` sends that backing file descriptor to virtiofsd and rejects
   a region without a file offset
6. Kata's [4.1.0 versions manifest](https://github.com/kata-containers/kata-containers/blob/ddcb1ad8d23cbb4323f86c209f132b89592902df/versions.yaml)
   pins virtiofsd v1.14.0. Substrate drives its own CH process and kata-agent
   RPCs, not the Kata runtime shim

For private memory the new VMM path runs inside `new_from_snapshot`, after
anonymous memory allocation and before `Vm::init` calls
`allocate_address_space` to register KVM memslots. The later private-memory
snapshot path uses `write_volatile_to` on actual guest bytes; it cannot use the
source file as a substitute for the private dirty pages. Source tests exercise
this dense-copy behavior across three subsequent generations. Real guest
checkpoint validation is not run here.

## Why shared virtiofs is not solved by an enum

Replacing the VMM's shared memfd mapping with `MAP_PRIVATE` would give the VMM
private dirty pages while virtiofsd continues accessing the original shared
memfd. Device writes and guest writes would disagree. Replacing its transmitted
FD with the snapshot FD is also wrong: a separate private mapping in each
process does not share their subsequent writes.

Bounded alternatives investigated:

- A separate reflinked file per VM can preserve shared access *within* one VM's
  VMM/backend, but distinct inodes do not establish shared file page cache
  *between* VMs. It does not deliver the requested optimization
- UFFD `COPY` preserves shared memfd/device correctness, but copies faulted
  source bytes into that VM's pages. It is the existing lazy-copy design, not
  cross-VM private mappings of a common inode
- Sharing one writable memfd between Actors breaks isolation. KSM is neither
  enabled nor an acceptable substitute for this design
- In-process virtiofs would keep accesses in the VMM's address space but is a
  new backend and device/snapshot security boundary
- A block-backed guest rootfs could use private RAM with in-process block/net
  devices. Substrate currently relies on merged host overlays, dynamic rootfs
  exports, durable/CSI volumes and frozen guest path identities; replacing them
  is a separate backend/lifecycle project, not a safe restore-time toggle

Accordingly, no shared mapping check is bypassed and no default dependency is
changed to an unpublished fork.

## Operator policy and observable outcome

`--memory-restore-policy`:

- `legacy` (default): exactly the existing version-aware Copy/OnDemand choice
- `cow-preferred`: try the strict compatible subset; log selected mode and an
  explicit `fallback_reason` when retaining legacy behavior
- `cow-required`: reject incompatible memory/devices or missing capability;
  never silently copy

The VMM must advertise `restore-cow-strict-v1` in `vmm.ping.features`. The
independent patch both advertises and implements this strict contract. Neither
a guessed version nor upstream's silently-falling-back enum is sufficient.
The deployment must still pin and verify the actual binary digest through the
existing runtime asset system; the capability is not a cryptographic identity.

Compatible subset: plain private anonymous RAM, explicit `thp=false`, no
hugepages, file-backed zones, NUMA mappings, hotplug, KSM mergeable memory,
vhost-user, VFIO/user-device/VDPA consumers. All saved ranges must be nonempty,
page-aligned, nonoverlapping, wholly contained in a region and within source
EOF. All ranges are validated before any MAP_FIXED operation. An mmap error
aborts the restore; there is no partial fallback. Existing Copy/OnDemand enum
values, formats and defaults are unchanged.

A CoW admission also requires delegated Actor cgroup ownership and
`--memory-snapshot-cache-dir=/absolute/private/path`. Missing ownership is a
required error or an observable preferred fallback. Cache integrity/storage
errors are failures, not a reason to silently trust corrupted data. Logs expose
actual requested/selected modes; a future benchmark must use the selected mode.

## Cache and lifecycle

The cache is runtime-owned mode 0700; it must not be guest-mounted or externally
writable. Objects are named by SHA256(authorization scope) and SHA256(logical
memory bytes). The scope is the already-authorized atespace. Content identity
is not permission to restore or share across atespaces. Mutable config and VM
state are copied into a private view; only `memory-ranges` is hardlinked.

Admission holds a process-shared flock, opens source files with `O_NOFOLLOW`,
requires regular files, hashes all logical bytes, preserves zero chunks as
holes, detects source size/mtime changes, fsyncs data, chmods 0400 and atomically
publishes with `renameat2(RENAME_NOREPLACE)`. Existing objects are rehashed before
reuse; corrupt objects are not overwritten. Directory fsyncs cover publication
and leases. The flock serializes admission I/O; hashing/copying can materially
increase cold-start time and must be measured, not omitted from First Exec.

Every restore view hardlinks the same immutable memory inode. GC takes the same
lock and unlinks only objects with link count one, never truncating an inode.
Live leases and crash-orphan leases therefore survive process restarts without
PID-liveness guesses. Staging files are reclaimed under the same admission
lock, so crashed temporary publication cannot permanently pin an object.
Runtime admission collects unleased objects last admitted over one hour ago;
recent unleased objects remain available for sequential warm reuse. This is
bounded-time retention, not a hard disk-capacity guarantee.

A lease is attached to `runningActor`. Successful teardown and failed startup
release it only after process cleanup and `ActorLeafEmpty` confirms ownership
has stopped. A missing/unreadable delegation or populated cgroup retains the
lease and logs/returns the reason. Release is idempotent. Runtime crashes can
leave orphan leases; automatic PID-based orphan deletion is intentionally not
implemented. An operator/recovery path must prove the Actor cgroup is empty
before deleting those views. Disk exhaustion returns an explicit error.

Source replacement, deletion or truncation after admission affects its old
staging inode, not the cached copy. Internal GC only unlinks: even an already
mapped inode remains valid after unlink. Mode 0400 is **not kernel-enforced
immutability against the trusted runtime owner or root**. A privileged process
can chmod/truncate the cache and cause faults; protect the directory and its
storage. No permission mode promises resilience against a compromised host.

CoW snapshots are self-contained, so `snapshotIsSelfContained` bypasses the
OnDemand sparse-delta merge. The cache source is never a writable checkpoint
merge target. Existing fd passing, network reconstruction, rootfs/volume
restoration and post-resume kata-agent CRNG reseeding remain in place. CoW does
not restore external connections. Existing post-resume entropy reseeding has a
pre-reseed guest execution window; golden snapshots must exclude credentials,
tokens and application-level random identities that cannot safely be cloned.
Sharing clean pages also changes cache timing/side-channel exposure. Scope
partitioning avoids additional cross-atespace deduplication; no KSM is enabled.

CPU affinity does not place shared file pages. Even an eventual compatible CoW
backend can incur remote NUMA access to pages first faulted on another node.
The strict patch rejects explicit memory-zone/THP policies because MAP_FIXED
replacement would otherwise discard VMA policy; NUMA locality/performance
remains unmeasured.

## Verification and outstanding gates

Source-level validation (see the [aggregate summary](../upstream/dependencies/validation.json)):

- Go `-race` cache and policy tests: actual MAP_PRIVATE A/B isolation, three
  checkpoint generations, same-inode reuse, distinct authorization scopes,
  concurrent acquisition/GC/release, source deletion, corrupt source rejection,
  crash staging recovery, cancellation, rollback, and warm-cache retention
- Patched full CH workspace `cargo check --locked --all-targets --features kvm`
- CH VMM source tests (actual production mapping helper), including API/default
  parsing, malformed/shared mappings, source unlink and private dirty pages
- Full changed CH VMM suite: not rerun. Historical missing-KVM failures are
  privately retained and do not validate the changed patch. Selected source
  tests are not guest validation
- The original handoff exercised the missing-prerequisite skip and required-
  failure branches. Final reconstruction uses no-KVM harness regressions;
  actual guest execution remains unrun
- Synthetic guest C compiles statically with `-Wall -Wextra -Werror`;
  Python harness parses/compiles

Run [the dependency patch instructions](../upstream/dependencies/README.md) and
`hack/mmap-cow/check.py` on an authorized x86_64 KVM host. It creates a bounded
128 MiB guest, two concurrent private clones, sparse/dense writes, three
recheckpoint generations per clone, golden SHA256 checks, VMM logs and
`smaps_rollup` observations. It uses a caller-supplied test Linux kernel and
creates its own credential-free initramfs; no network, global `drop_caches`,
paid resources or real user image is used. The real guest path is **unverified**
in this environment. It is not a production virtiofs compatibility test.

Still missing: compatible ordinary Actor memory/device backend; real KVM
restore/recheckpoint runs; production restart/crash and multi-worker/remote-node
integration; source-storage fault injection while guests run; cold/warm cache,
PSS/private-dirty/whole-cgroup density experiments; memory pressure and NUMA
measurements; operator orphan-lease reconciliation and hard capacity policy.
No latency or density target has been established.

Rollback: leave/restore `--memory-restore-policy=legacy`, use unchanged pinned
CH assets, stop any experimental guests before retiring their leases/cache.
Do not unlink a running VM's source as a substitute for lifecycle cleanup.
