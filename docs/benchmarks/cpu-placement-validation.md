# CPU placement correctness validation

This checks the Linux enforcement boundary independently of KVM. It does not
measure performance, prove node-wide exclusivity, or test Cloud Hypervisor's
vCPU-to-thread mapping. The implementation constrains every managed Actor
process to a cpuset, including later-created threads.

## Unprivileged fixture tests

From the repository root with its supported Go toolchain:

```sh
go test -race ./internal/ateomcgroup ./internal/cpuplacement -count=1
go test ./internal/ateomcgroup -run '^$' -fuzz '^FuzzParseQuota$' -fuzztime=10s -parallel=2
```

The placement fixtures use regular files to test intersection, validation,
quota parsing/write/readback, effective-set readback, refusal to reconfigure
populated leaves, and reconstruction of live CPU occupancy after a Worker
restart. They cannot establish kernel enforcement or throughput. A visible
unlimited `cpu.max` is not proof of unlimited invisible namespace ancestors.

## Real kernel integration

An operator must first provide an isolated, disposable cgroup v2 subtree. The
parent's `cgroup.subtree_control` must already enable `cpu` and `cpuset`, and
at least two online logical CPUs must remain in the intersection of its
`cpuset.cpus.effective` and the launching process's allowed CPUs. The parent
must have a nonempty `cpuset.mems.effective`, readable `cpu.max`, and permission
to create descendants. Use root inside this delegated test environment to
match the repository root-test convention. A recent kernel supporting
`clone3(CLONE_INTO_CGROUP)` and a sandbox policy permitting it are required.
No KVM, network, package installation, or paid resources are used.

Set `CPU_PLACEMENT_CGROUP_ROOT` to that existing subtree, never the host mount
root. Do not create delegation by changing kubelet-owned files or remounting a
production cgroup hierarchy. The test only creates/removes its unique
`substrate-cpu-placement-*` descendant, enables controllers inside that empty
descendant, and adjusts the Actor leaf it owns. It never moves the test process,
changes global CPU policy, or changes an ancestor's quota/cpuset.

```sh
# Example only: substitute the operator-provided, already-delegated path.
export CPU_PLACEMENT_CGROUP_ROOT=/sys/fs/cgroup/operator-provided-test-scope
export REQUIRE_CPU_PLACEMENT=true

go test -c -o /tmp/substrate-cpu-placement.test ./internal/ateomcgroup
# Run as root within the explicitly delegated environment. Preserve only the
# variables needed by this test when using sudo.
sudo --preserve-env=CPU_PLACEMENT_CGROUP_ROOT,REQUIRE_CPU_PLACEMENT \
  /tmp/substrate-cpu-placement.test \
  -test.run '^TestCPUPlacementKernelEnforcement$' -test.v -test.timeout 60s
```

The kernel test:

1. Selects one actual allowed CPU, excluding at least one other allowed CPU
2. Applies 500m CPU bandwidth and effective CPU/memory-node sets before clone
3. Starts a copy of the test executable directly in the leaf using its cgroup FD
4. Runs bounded, deterministic arithmetic on four distinct, simultaneously live
   OS threads created after startup and checks the result
5. Reads every live `/proc/<pid>/task/<tid>/status` CPU and memory-node mask,
   checks effective cgroup masks and verifies `cgroup.procs` membership
6. Reconstructs allocator ownership from the populated leaf and proves another
   Actor cannot claim its logical CPU
7. Refuses to reconfigure the running Actor and checks that its quota survived
8. Waits for workload exit, proves `populated 0`, removes the cgroup, and only
   then releases/reuses the allocator lease

The entire workload is bounded by 30 seconds, with cleanup on ordinary test
failure. If the runner is forcibly killed, an operator should first establish
that its child test process has exited before removing its named leftover
subtree. Never recursively delete an unrelated cgroup tree.

## Verify the fail-closed gate

Missing prerequisites skip only during an ordinary local run. The named
`cpuPlacementRequired` predicate requires actual execution when either
`CI=true` or `REQUIRE_CPU_PLACEMENT=true`. These guards execute before the
root-test helper can skip an unprivileged run.

```sh
# These must report FAIL with exit status 1, not PASS/SKIP. They do not create
# cgroups or modify the machine.
CI= REQUIRE_CPU_PLACEMENT=true CPU_PLACEMENT_CGROUP_ROOT= \
  go test ./internal/ateomcgroup -run '^TestCPUPlacementKernelEnforcement$' -count=1 -v
CI=true REQUIRE_CPU_PLACEMENT= CPU_PLACEMENT_CGROUP_ROOT= \
  go test ./internal/ateomcgroup -run '^TestCPUPlacementKernelEnforcement$' -count=1 -v
```

On the constrained development executor (2026-10-13), the fixture/race tests
passed. Both strict negative guards were observed failing as intended. The
real kernel test skipped because there is no delegated cgroup root; `/sys`
and cgroupfs are absent. No kernel enforcement success or performance result
is claimed from that environment.

## Hardware results to collect separately

After the kernel test passes, run the runtime benchmark suite in the same
resource budget with placement disabled, shared placement, nonoverlapping
placement, and management reservation. Record both logical CPUs excluded from
Actors and any parent quota; reservation does not guarantee management CPU time
when a shared ancestor is throttled. Measure Actor First Exec, management
latency, throughput, failure rate, CPU PSI and `cpu.stat`, and preserve all raw
samples, including failures and queueing time.

NUMA/LLC claims additionally require real visible topology and separate CPU and
memory-locality measurements. `cpuset.mems` constrains future allocations; it
does not prove that immutable shared mmap pages already reside locally or have
migrated. Multi-Worker isolation and VMM/virtiofsd process coverage remain
separate runtime/KVM acceptance tests. None of these measurements can be
inferred from the fixture tests or the bounded arithmetic checksum.
