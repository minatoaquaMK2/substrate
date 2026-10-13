# Strict Cloud Hypervisor v53 CoW experiment

This directory contains an independent local dependency patch. It does **not**
make Substrate's shared-memfd/virtiofs Actors compatible with private mmap RAM.
Read [the design and unresolved production boundary](../../design/mmap-cow-restore.md).

Cloud Hypervisor v53 base: `9ed824d6d08df3e96f7d5f50795d9449ac99f431`.
Patch SHA-256: `4aa9b1cd5518750ed99ceafda8f56664b63341ddff0bab6524d8e738a1e22121`.
The [aggregate validation manifest](validation.json) records source-level
outcomes and portable commands. Full original evidence is privately retained;
raw build/test logs and captured host/environment data are not published.
The patch bytes are unchanged from original tested Substrate source
`65097aaff066dcb3769a9e38d9fc57734a0f67ab`. Documentation and evidence packaging differ; this is not a
claim of byte-identical whole-tree publication.

The fresh no-KVM suite passes 11 tests; all-targets check and debug build pass.
The full VMM suite and real guests were not rerun. Strict Clippy/fmt retain
unrelated baseline failures; the explicit Clippy exception run is reported
separately, never as a clean strict pass. The harness has nine passing no-KVM
failure/protocol tests, including full-region guest corruption negatives.

## Reproduce the source build

Use an isolated clone, Linux x86_64, Rust 1.89.0 or compatible, a C toolchain and
the normal CH build dependencies. No KVM privilege is needed for source tests.
The source lock is CH v53.0 commit
`9ed824d6d08df3e96f7d5f50795d9449ac99f431`.

```sh
git clone https://github.com/cloud-hypervisor/cloud-hypervisor.git ch-cow
cd ch-cow
git checkout -b experiment/strict-cow 9ed824d6d08df3e96f7d5f50795d9449ac99f431
git apply --check /path/to/substrate/docs/upstream/dependencies/cloud-hypervisor-v53-cow.patch
git apply /path/to/substrate/docs/upstream/dependencies/cloud-hypervisor-v53-cow.patch
cargo check --locked --all-targets --features kvm
cargo test --locked -p vmm --features kvm cow_
CH_EXTRA_VERSION=experimental-strict-cow-reconstructed cargo build --locked -p cloud-hypervisor --bin cloud-hypervisor
./target/debug/cloud-hypervisor --version
sha256sum ./target/debug/cloud-hypervisor
```

Release binaries for a real operator trial must be built/reviewed separately,
using the normal CH release target/options and verified SHA256 through
Substrate's existing SandboxConfig asset mechanism. Do not label the changed
binary an official v53 release. `vmm.ping` advertises
`restore-cow-strict-v1`; tests/build checks are necessary but not a substitute
for trusted artifact distribution. Default Substrate asset pins are untouched.

The patch is independent of Cocoon and does not copy its implementation.
It adds actual `MAP_PRIVATE|MAP_FIXED` memory restoration, preflight range
validation, a strict capability marker, and correctness source tests. Shared
file memory, zones, hugepages, THP, mergeable RAM and hotplug are rejected;
the entire range table must exactly cover guest RAM and source bytes, with
alignment/overlap checks before replacing any VMA. Missing memory-manager
snapshot state is rejected before hypervisor creation. Copy/OnDemand paths
retain their default behavior.
Snapshot serialization remains the existing complete private-memory byte path.
The patch does not add diff snapshots or change external device state.

## Bounded guest test

On an authorized x86_64 Linux host with accessible `/dev/kvm`, an initramfs-capable
Linux kernel with serial console/ttyS0 support, a static C compiler and the
patched binary:

```sh
python3 /path/to/substrate/hack/mmap-cow/check.py \
  --required --binary "$PWD/target/debug/cloud-hypervisor" \
  --kernel /absolute/path/to/test-vmlinux --output /tmp/cow-run
```

The output path must be new and short enough for Unix sockets. The harness
compiles a small synthetic PID 1, packs a newc initramfs, boots a 128 MiB VM,
takes a golden snapshot, restores two clones through the real CH API, checks
sparse/dense private writes and three recheckpoint generations, and retains
JSON status, source/binary digests, logs and PSS/private-dirty observations.
Every acknowledgement verifies all 16 MiB of deterministic guest state, and
middle-byte corruption is tested. The verification touches every page, so PSS
samples are functional diagnostics rather than sparse-density evidence. API
and serial waits have absolute monotonic deadlines and serial logs have a
1 MiB cap with explicit truncation failure. Cleanup attempts every VM and
records all failures. Processes are killed/waited on exit; no host CPU/cgroup policy, networking,
private credentials or global cache policy is modified. Disk artifacts remain
for inspection. It is a functional test, not a representative Actor benchmark.

Without KVM/kernel/binary the script emits explicit `skipped`; `--required` or
`CI=true` instead fails. The original delivery exercised both
precondition branches; the final verification uses no-KVM tests only. Actual guest
execution remains unverified and requires separately authorized hardware.

## Contribution boundary

The patch is provided for reproducibility, not as an upstream-ready submission.
CH requires a human contributor, DCO Signed-off-by, project-specific AI
disclosure and human ownership/review. No DCO, CLA, upstream issue/PR or CH-fork push is performed by this work.
The patch is included only in the authorized Substrate fork publication. Before any upstream submission a human must review the
code, rewrite/approve contribution text and satisfy the project's current
contribution policy. Recheck current upstream
CoW work before proposing a change; this pinned backport must not be presented
as a novel general-purpose CoW implementation.

Full production Substrate CoW remains blocked by its shared-memory filesystem
backend. This patch is a tested source-level compatible subset, not a resolution
of that dependency boundary.
