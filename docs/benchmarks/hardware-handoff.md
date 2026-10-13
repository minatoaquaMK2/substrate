# Bring-up and hardware acceptance handoff

No live MicroVM, physical NUMA/LLC, or production CoW performance was measured
in the development container. The user will supply hardware separately.

## Minimum machines and authority

1. Linux x86_64 or arm64 with cgroup v2, delegated cpu/cpuset controllers,
   readable CPU/node/cache sysfs, permitted clone-into-cgroup, and root
   execution for the repository's privileged-test convention. Provide an
   **existing empty, disposable delegated subtree** for focused tests. Tests
   will not create host delegation or change any parent limits. The runtime's
   production subtree is its own private Worker cgroup namespace
2. A KVM-capable host and an isolated already-provisioned Substrate test cluster
   with the pinned Kata/CH/virtiofsd artifacts and Glutton template. Normal
   test-cluster credentials are sufficient; do not use production Actors
3. For topology performance, hardware with multiple real NUMA nodes and LLC
   domains, or an explicitly labeled virtual topology test. Record SMT,
   sibling visibility, CPU Manager/Topology Manager policies and actual parent
   cpusets. A VM-visible map does not establish physical host locality
4. For the separate private-memory CH CoW experiment, x86_64 KVM, an
   initramfs-compatible Linux kernel, static C compiler and the independently
   patched CH binary. This experiment does not enable production virtiofs CoW

No purchase, privilege grant, BIOS change, global scheduler change or kubelet
checkpoint edit is performed by the supplied scripts.

## Start with functional checks

Build with the repository's pinned Go toolchain. Run broad source tests and
verifiers as an ordinary user; they contain unrelated privileged mount/network
cases and must not be run wholesale as root for this handoff:

```sh
go test -race -count=1 ./...
hack/verify-all.sh
python3 -m unittest discover -s hack/mmap-cow -v
```

The KVM-harness tests use fake transport failures and a native C guest verifier;
no KVM, sockets, cgroup writes or mounts are needed. For actual kernel CPU
placement, provide the authorized delegated subtree and run only the dedicated
`TestCPUPlacementKernelEnforcement` entrypoint through the focused script:

```sh
export CPU_PLACEMENT_CGROUP_ROOT=/absolute/existing/delegated/subtree
export RUNTIME_VALIDATION_OUTPUT=/absolute/new/evidence-directory
hack/runtime-resources/validate.sh
```

The cgroup test is fail-closed with REQUIRE_CPU_PLACEMENT or CI, and inspects
real child-thread effective allowance and output. Read
[cgroup setup and cleanup boundaries](cpu-placement-validation.md) first.
The aggregate script uses `pipefail`, so a failed test cannot become green
because `tee` succeeded. Apiserver-backed API tests need the repository's
pinned envtest assets and permission to run their local sockets/processes.

## Runtime matrix still to execute

Keep the base and patched image/VMM digests, exact manifests, commands and
raw outputs with each run. Begin default-off, then reservation only, shared
CPU allocation, Worker-local nonoverlap, strict/preferred LLC/NUMA, local
memory-node constraint, and combinations with unchanged Actor CPU quota.
Do not call Worker-local nonoverlap node exclusivity.

- Several Actors in one Worker: CPU-heavy, I/O-heavy, restore storm, overload,
  suspend/resume, start failures, canceled requests and cleanup timeouts
- Restart the Worker with live cgroups; verify recovered leases prevent reuse
  until actual termination. Test partial startup, repeated terminate and PID
  reuse using the live cgroup boundary, not process-name matching
- Shrink the permitted CPU set in a **test-owned outer scope**, then attempt new
  allocations: fail closed and preserve existing ownership. Do not alter a
  kubelet-owned cpuset, node root, or another user's process
- Two Workers on one node with externally disjoint sets; separately test
  overlapping sets and verify no node-exclusivity claim is emitted
- Restore onto another node with different CPU numbering; allocation is local
  and source host CPU IDs must not become snapshot state
- Invalid/absent delegated controllers: Required fails before Guest start;
  BestEffort initialization logs actual off mode and reason. Capacity exhaustion
  must never silently change NonOverlapping into Shared
- Finite common ancestor quota: demonstrate and report management starvation
  limits. The CPU-set reservation and weight are not guaranteed CPU time
- NUMA/LLC: noncontiguous CPUs, partial SMT siblings, fragmented capacity,
  strict rejection vs preference degradation, actual `cpuset.mems`, `numa_maps`,
  PSS/private-dirty and remote-access observations where permissions allow

Use [the bounded open-loop suite](README.md) for lifecycle/First Exec, throughput,
errors, resource cost, cold/warm evidence and sparse/dense access. Preserve
regressions. Use a fixed latency/failure target for separate density trials.
Record missing perf/eBPF permissions rather than fabricating counters.

## CoW acceptance boundary

Follow [the independent CH build and guest harness](../upstream/dependencies/README.md).
It verifies a tiny private-memory guest's clone isolation and repeated snapshot
state. Production Substrate uses a shared memfd for virtiofs; `cow-required` must
reject that configuration and `cow-preferred` must record legacy fallback.
Production end-to-end CoW acceptance is still dependency-blocked even after
all private-memory CH tests pass. A safe compatible filesystem/device design,
maintainer approval and its full lifecycle test matrix remain prerequisites.


The private-memory harness checks every byte in its 16 MiB test region after
sparse and dense writes and subsequent checkpoints. Verification itself faults
the whole region, so its PSS samples are functional diagnostics, not a sparse
working-set performance experiment. Serial/API operations have absolute
monotonic deadlines. Serial evidence is capped at 1 MiB per VMM with explicit
truncation failure, and cleanup attempts all VMMs even after an earlier error.
Unresolved process cleanup must be reconciled by the operator. See the
[production feasibility assessment](../research/cow-production-feasibility.md)
for the missing native-filesystem integration; no prototype is claimed.
