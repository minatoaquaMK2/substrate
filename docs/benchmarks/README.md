# Runtime resource benchmarks

## Status and scope

`benchmarking/runtime/` adds a finite **open-loop** driver to the existing
benchmark stack. It reuses `kubectl-ate` for real Actor lifecycle operations,
the existing HTTP-mode Glutton guest workload for verified commands, and
`benchmarking/analysis/` for server-side phases. It neither deploys infrastructure
nor invokes a paid LLM. Python tests require only the standard library.

The benchmark harness has 41 passing Python tests and a passing native Glutton
protocol smoke on original tested source
`65097aaff066dcb3769a9e38d9fc57734a0f67ab`. Synthetic harness validation is
**not microVM performance data**. Full original evidence is privately retained;
this publication provides [aggregate results and reproduction instructions](../research/verification/README.md),
without raw trial records, captured environment files or tool logs.

Real microVM lifecycle, optimization comparisons, physical memory density,
NUMA/LLC locality, and cold/hot target-host cache experiments remain
**hardware-blocked**. Local HTTP and fake-CLI tests establish the protocol
contract. Native Glutton smoke verifies the real workload binary without
exercising a VM or cluster lifecycle. A successful self-test is not runtime
acceptance.

Existing Locust/boomer workloads remain useful for their workload-specific and
cluster telemetry coverage. The narrow missing pieces addressed here are a
bounded arrival schedule independent of completion, per-request raw outcomes,
and checked guest command completion. In-flight upstream burst/paced workloads
may provide a future home for this driver; avoid maintaining two load engines
once those changes land.

## What is measured

Each request keeps distinct timestamps/durations:

- **New activation:** `create_destroy` includes CreateActor, ResumeActor and the
  complete verified guest command; deletion follows the measured interval
- **Existing restore:** `resume_storm` first prepares separate suspended actors;
  measured work is ResumeActor plus the verified guest command
- **Golden clones:** separate actors borrow the same template's golden tag.
  Creation output must identify the same completed, tag-owned MEMORY snapshot
  UUID, durable URI and template UID throughout the trial; rotation fails
- **Pause/resume:** node-local paused actors, kept separate from durable suspend
- **Warm command:** existing running actors; no resume timing is emitted
- **First Exec:** scheduled offered arrival to the completed, checked Glutton
  guest operation and request-specific echo. This includes generator delay,
  client queueing, API/worker scheduling, restoration and page faults
- **Resume API:** CLI process start through successful ResumeActor CLI return.
  Includes CLI startup/transport overhead and is not a VMM API duration
- **Guest-command service:** HTTP operations after ResumeActor; includes transport
  and verification. It is a predefined guest API command, **not a shell exec**

The CLI has no shell-exec endpoint. Glutton Ping returns the exact supplied
message; the driver verifies a request-specific echo after every operation.
CPU work verifies positive signed-int64 hash iterations. Disk work ingests fixed bytes then
verifies size and SHA-256 on a guest disk read. Sparse/dense RAM work compares
size and checksum with values saved before suspension. The RAM checksum is the
existing XOR-of-page-samples API, a benchmark correctness guard rather than a
cryptographic whole-memory integrity check.

`vmm_restore_api_s`, `guest_resume_s` and `prewarmed_claim_s` are null in client
records. They cannot be inferred from a fast request. In particular, a warm
command or golden clone is not a measured prewarmed-object claim. Use server
phase logs for VMM restore/resume intervals. Do not subtract timestamps from
different hosts; this driver measures its durations with one monotonic clock.

## Finite self-test

From the repository root:

```sh
python3 -m unittest discover -s benchmarking/runtime -v
python3 benchmarking/runtime/runner.py plan \
  --out /tmp/runtime-plan.json --requests 8 --repeats 3 --seed 20261013
python3 benchmarking/runtime/runner.py run \
  --adapter selftest --plan /tmp/runtime-plan.json --out /tmp/runtime-selftest
```

A destination must not already exist, so a rerun cannot overwrite prior data.
Overload and burst scenarios intentionally record drops when capacity is full.
Exit status 1 means at least one request/trial/cleanup failed or the run stopped;
raw data and summaries are still saved when storage permits. Observer or
recording failures stop admission, drain queued work, attempt cleanup and return
nonzero; affected runs cannot publish repeat ranges. Exit status 2 means invalid
input or an early setup/output error.
`--selftest-delay` and `--selftest-fail-every` inject synthetic service time and
verification failures. They never operate Actors or represent host performance.

Optional real-workload protocol smoke, still without a VM or cluster:

```sh
go build -o /tmp/runtime-glutton ./cmd/benchmarking/glutton
python3 benchmarking/runtime/smoke_glutton.py --binary /tmp/runtime-glutton
```

This launches Glutton on loopback, verifies all five command types and CPU-load
start/stop, and removes its temporary files/process. The binary must be built
from this checkout. To reproduce the synthetic stress fixture, add `--selftest`
to `plan` with `--requests 12 --repeats 3 --seed 20261013`, then run with
`--selftest-delay 0.004 --selftest-fail-every 5 --sample-interval 0.1`.
Injected failures and overload are expected; the runner exits 1 after saving
all results. Exact drop/success counts vary with the load generator scheduler.

## Run against an existing isolated test cluster

Prerequisites:

1. An already provisioned Linux/KVM cluster running the measured Substrate
   checkout and supported microVM VMM. Follow
   [microVM setup](../dev/microvm-local.md) separately. Provisioning, modifying
   global CPU policy, paid infrastructure, and production testing are outside
   this driver's actions
2. An HTTP-mode Glutton template with a completed golden snapshot and memory
   snapshot fidelity. Use the existing
   [benchmark workloads](../../benchmarking/README.md), sized to fit the run.
   Record the template and image digests, sandbox class/config, golden snapshot
   ID/size, worker count, pod/node budgets and topology in an environment manifest
3. An existing dedicated test atespace, enough test workers and storage, an
   accessible router, and `kubectl-ate` built from this checkout. It uses the
   operator's normal test-cluster access or a scoped test service account.
   No real-user credentials are required. Use credential file paths, never
   inline secrets; command lines and CLI responses are saved
4. A trusted TLS CA/client certificate where the deployment requires mTLS.
   Certificate verification is enabled. Use HTTP only for a controlled local
   port-forward or a trusted isolated test network

Generate a small plan, inspect it, and use a new result directory:

```sh
go build -o /tmp/kubectl-ate ./cmd/kubectl-ate
python3 benchmarking/runtime/runner.py plan \
  --out /tmp/runtime-plan.json --requests 4 --repeats 3 --seed 42
python3 benchmarking/runtime/runner.py run \
  --adapter substrate --allow-create-actors \
  --plan /tmp/runtime-plan.json --out /tmp/runtime-real-run \
  --cli '/tmp/kubectl-ate --context YOUR_TEST_CONTEXT' \
  --atespace runtime-benchmark --template benchmark-workloads/glutton \
  --router-url http://127.0.0.1:8080 \
  --manifest /tmp/runtime-environment.json
```

Create the dedicated atespace separately with
`kubectl-ate create atespace runtime-benchmark` if authorized and needed. The
runner never creates or deletes templates, namespaces, atespaces, or pools.
Add `--ca /path/ca.pem --client-cert /path/client.pem --client-key /path/key.pem`
for HTTPS/mTLS. The configured CLI prefix can use `--endpoint` to reuse an
existing connection route rather than repeatedly creating port-forwards;
measure and disclose CLI overhead either way.

The generated ladder includes idle, 10% single-core low, 50% single-core
medium, and two full-core high competitor load. CPU competitors are additional
real Actors running Glutton `UseCPU` with a
specified goroutine count/duty cycle. They stay resident through preparation
and measurement and are deleted afterward. Their worker/memory/CPU cost is part
of the scenario. A trial with N prepared actors and C competitors can own N+C
actors; whether all can run at once depends on the deployed Worker model and
available workers. Keep this within the operator's resource budget. Preparation
is serial and is recorded outside the timed population.

The plan is JSON and intended to be reviewed/edited. `plan` produces three
randomized repeat blocks by default; the seed and exact order are saved.
Supported fields and conservative bounds are validated before creating actors:

- `kind`: `create_destroy`, `resume_storm`, `pause_resume`, `warm_exec`,
  `memory_density`; `source`: `golden` or `per_actor`
- `workload`: `echo`, `cpu`, `io`, `sparse`, `dense`
- `requests`, `rate` (0 is a simultaneous burst), `concurrency`, `queue_capacity`,
  `timeout_s`, `setup_timeout_s`, `cleanup_timeout_s`, `abort_after_errors`
- `competitors`, `competitor_cores`, `competitor_duty`, `cpu_ms`, `memory_bytes`,
  `io_bytes`, `hold_s`, `snapshot_cache`, `file_cache`, `cache_evidence`

Per-actor snapshot cases initialize, suspend, and restore their own distinct
snapshots. The latest usable generation must have MEMORY fidelity and Actor
ownership, with distinct UUIDs and distinct nonempty durable URIs. Actor UID,
template UID, generation, owner, URI and locality are retained in raw provenance.
A durable source must be completed; a paused source must have local provenance.
Missing identity or a newer usable volume-only snapshot fails preparation rather
than selecting an older convenient snapshot. Golden cases retain tag ownership
and do not suspend before the measurement.

This is API fixture provenance, not proof of the restore path actually used.
Template mutation or runtime compatibility policy can still downgrade fidelity
between inspection and resume. Keep templates immutable for the experiment and
attach runtime restore-mode/fidelity logs before making a memory-restore claim. RAM cases require per-actor state; a golden template with a large
initialized heap is not fabricated by this driver. Sparse touches one 4 KiB
page; dense touches one byte on every 4 KiB page across `memory_bytes`, forcing
all pages resident. Dense does not mean every byte is scanned or dirtied.

## Load, tails, and comparisons

The dispatcher schedules `origin + index/rate`, without waiting for completions.
Admission covers executing plus queued requests and is bounded by
`concurrency + queue_capacity`. Full capacity creates a raw `dropped` outcome;
it never slows the offered schedule to make overload disappear. Each deadline
starts at scheduled arrival, including time waiting in the local queue.
Scheduler lag, queue wait, failures and errors remain in the raw records.
Router DNS is resolved once before measurement in a bounded subprocess; pinned
addresses are logged while HTTP Host and TLS SNI retain the original hostname.
Connect attempts, headers, chunk framing and body reads share one absolute
request deadline. Truncated Content-Length responses cannot verify successfully.
Expired CLI operations never spawn a subprocess. CLI timeout kills the process
group and bounds output draining even if a detached descendant retains pipes;
`output_complete: false` means the logged output is partial.
Explicit gRPC ResourceExhausted or HTTP 429 rejections have their own `rejected`
status; other failures preserve their CLI/HTTP error text.
If the load generator cannot keep up, inspect `generator_lag_s` before drawing
server conclusions. SIGINT/SIGTERM stop further work and initiate cleanup.

P50/P95/P99 use nearest rank on **successful First Exec samples**, accompanied
by exact n, all outcome counts, failure fraction and successful throughput.
A failed request does not become a zero or vanish. P99.9 is always withheld per trial. It is published only as repeated-run
estimates/range when at least three distinct repeats each have 10,000 successes,
all scenario configurations match, and `(maximum - minimum) / median` of their
P99.9 estimates is at most 10%. This predeclared descriptive stability gate is
not a universal confidence guarantee; unstable or undersampled tails stay null
while all samples remain in the raw data. Other repeated-run min/max ranges
are descriptive variation, not confidence intervals, and need three distinct
repeat IDs and the same nonempty configuration fingerprint. That fingerprint
includes exact golden snapshot identity, so snapshot rotation also invalidates
comparisons between repeats. Small-sample P99 is also
unstable: inspect n and repeat spread. Prepared fixtures are capped at 1,000
actors per trial; sustained `create_destroy` can collect up to 100,000 requests
with bounded live ownership, allowing larger tail samples when authorized.

Run baseline, reservation-only, pinning-shared, pinning-worker-nonoverlap,
NUMA/LLC-only, restore-mode-only, and combination profiles as separately
operator-configured deployments. The harness **does not turn features on**.
Save requested settings plus actual runtime status/logs and fallback reasons.
Requested `CopyOnWrite` is not evidence of private file-backed sharing: shared
memfd/virtiofs configurations may use an upstream copy fallback or reject the
mode. Label by the mode actually observed; leave `actual_profile` unverified
without that evidence. Never call Worker-local nonoverlap node-exclusive.

Keep CPU quotas, host capacity, worker and actor count, memory limits, template,
load schedule and snapshot data constant for latency comparisons. Record
management-reserved CPUs and competitor costs explicitly. To randomize profile
order as well as scenario order, use a seeded shuffled profile list and let the
operator reconfigure the isolated deployment between runs; attach that order
and deployment manifests. Changing a profile string alone does not implement a
comparison. Density is a separate experiment: increase resident actor count
within an approved bound while holding a stated latency/failure target fixed.
`memory_density` holds successful residents for `hold_s` so external host
observations can capture them; actor count is not physical-memory density.

## Cache preparation and observation

Snapshot-object cache and Linux file-page cache are different axes. Defaults
are `uncontrolled` for both. An `operator_cold`/`operator_warm` label requires
`cache_evidence` describing the exact target nodes, objects/inodes, preparation
method, time, and validation. These remain operator declarations, not automatic
proof. Warm once on the actual target placement and record residency if
available. For cold trials use a disposable isolated test node or fresh
non-shared fixture and explain why it is cold; fresh inodes change the sharing
experiment. Do not run global `drop_caches` or evict a shared machine's cache.
The driver never clears caches or modifies sysfs/cgroup settings.

`environment.json` captures driver kernel/architecture, source SHA, dirty diff
hash, suite source hashes, mount/cgroup/CPU visibility, and an optional operator
manifest. Host topology in a VM is only guest-visible topology. Supply base SHA
and patch SHA, VMM binary version/hash, host/Pod configuration, CPU quota and
reservation, snapshot size, placement and requested/actual feature evidence in
the manifest. Missing evidence remains explicit.

`observations.jsonl` stores best-effort host `meminfo`, `vmstat`, PSI; requested
process `stat`, `schedstat`, `sched`, affinity, I/O, `smaps_rollup`, `numa_maps`;
and requested cgroup CPU/PSI/quota/cpuset/memory/I/O files. Missing permissions or
files produce an `unavailable` value. PID starttime checks detect reuse during a
sample. Fixed PID lists do not discover all new VMM threads/processes after a
restart; refresh the collector with actual runtime-owned PIDs and preserve the
mapping to Actor UID and cgroup. Never equate a Pod's cgroup with the actual
privileged VMM process location.

By default observations describe **the load generator**, not the target host.
Use `--target-host-local --pid PID --cgroup PATH` only when truly co-located;
include each Actor cgroup and its effective parents. Or run this read-only
collector on each authorized target node, with actual PID/cgroup paths:

```sh
python3 benchmarking/runtime/observe.py --out /tmp/node-observations.jsonl \
  --duration 60 --interval 0.5 --pid VMM_PID --pid VIRTIOFSD_PID \
  --cgroup /actual/actor/cgroup --cgroup /actual/parent/cgroup
```

Use PSS and private dirty memory together with baseline-subtracted target-host
or parent-cgroup aggregate memory. Do not sum RSS and claim physical sharing.
Control competing allocations and retained file cache; preserve raw samples
before/after and during the density hold. Perf/eBPF migration/scheduling counters
require a separately authorized collector and are unavailable otherwise.

Reuse server phase tooling while the worker pods still exist:

```sh
benchmarking/analysis/collect_logs.sh --dest /tmp/runtime-real-run/logs \
  --since-time RUN_START_RFC3339
python3 benchmarking/analysis/phase_report.py /tmp/runtime-real-run/logs/*.log \
  --csv /tmp/runtime-real-run/phases
```

The existing phase report excludes failed operations from phase percentiles;
retain its input logs and this driver's failure records. It is not the
all-outcome First Exec report.

## Artifacts and cleanup

Each run writes `plan.json`, `environment.json`, `commands.txt`,
`raw-results.jsonl`, `observations.jsonl`, `summary.json`, `summary.md`, and
`logs/adapter.jsonl`. Keep raw records, including regressions. Record any
preparation/deployment commands beside them. Attach target-node collectors,
Pod specifications and existing server logs before making runtime conclusions.
Keep captured artifacts private until their exact contents and destination are
reviewed for publication. This branch publishes only aggregate validation
summaries; it includes no raw run or captured environment artifacts.

Actors have a random run prefix and are recorded as owned **before** CreateActor
so ambiguous timeouts do not silently escape cleanup. An explicit gRPC
AlreadyExists collision removes the name from owned cleanup and stops the run;
a pre-existing Actor is never adopted or deleted. Every owned actor gets bounded
`delete actor --any-state` cleanup, including partial setup and failed requests.
Cleanup errors remain in `cleanup_inventory.remaining_actors`; the run exits
nonzero and stops further admission immediately, including within sustained
create/destroy trials. If raw output fails, cleanup still updates ownership and
the final summary retains unresolved names where storage remains writable.
Deletion acknowledgement is not proof that
all host processes have exited. After a timeout, server-side work may outlive
the client; inspect the exact run-prefixed Actor names in the raw records and
actual cgroups after cleanup. A timed-out create/delete race or a killed driver
requires this reconciliation before another benchmark uses the same capacity.
Never bulk-delete unrelated actors. No success/performance result excuses an
unresolved cleanup inventory.
