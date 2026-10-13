# Reconstruction and adversarial review record

## Source provenance

The original tested source is `65097aaff066dcb3769a9e38d9fc57734a0f67ab`, incorporating upstream
`c1ab95dea49bacfdeefcb00dd67b66d27372021c`. Earlier implementation work was reconstructed from preserved
source and then revalidated; results from an unavailable earlier tree are not
used as evidence for this checkpoint.

The public history starts from the upstream base and contains implementation,
tests, reproduction instructions and aggregate summaries. Full original
verification evidence is privately retained. Production and test source bytes
are retained from the original tested source, as recorded by
[source hashes](verification/source-hashes.json); documentation and evidence
references differ. The public whole Git tree is not byte-identical to the
original checkpoint, and final publication verification must be recorded
separately rather than relabeling historical results.

The upstream refresh preserves validation before startup-slot acquisition and
retains protobuf field identities. Review checked mount translation, MicroVM
pause-image handling, strict malformed-request rejection and the removed
helper/oneof symbols. The upstream gVisor change is a test fixture, not a new
runtime policy. Upstream validation intentionally rejects specs with multiple
actor_metadata sources even when their paths differ; existing specs of that
shape may need correction before resume. Generated validators assume nonnil Go
request objects, as normal gRPC decoding provides; direct internal callers must
satisfy that precondition.

## Risk-directed review and counterexamples

A green existing test suite was not the stopping criterion. Reviews followed
configuration → controller → flags → process launch → kernel boundary, and
snapshot source → cache lease → VMM mapping → checkpoint → cleanup. New tests
first reproduced defects where practical, followed by fixes and fresh repeats.
No hardware execution or performance numbers are inferred from these tests.

### Benchmark integrity

- Existing-name collisions cannot grant ownership or authorize deleting another
  Actor, even when diagnostic output is longer than the logging prefix
- Delete failures halt further admission; raw-record and observer failures stop
  and drain the run, return errors, and preserve cleanup inventories
- Subprocess timeout cleanup bounds post-kill pipe draining, including a child
  that escaped the original process group
- Absolute HTTP deadlines include response headers and body; truncated declared
  bodies cannot become valid first-exec results
- Integer CPU duties use protobuf doubles; malformed varints and negative CPU
  iterations are rejected; the real native Glutton protocol is exercised
- Golden MEMORY snapshot ownership, template, UUID and URI are checked, and
  distinct-Actor snapshots cannot silently share one source identity
- Repeats are grouped only across comparable configurations; tail claims require
  adequate population and repeat stability; all dropped/error/timeout outcomes
  remain in the raw population

The new adversarial suite is run against recovered first-delivery source as a
separate red test. The aggregate verification summary distinguishes native protocol checks
from synthetic harness trials. They do not represent real Actor performance.
A later repeated-run review found a test-fixture startup race in the escaped
pipe-holder case; the fixture is synchronized explicitly and revalidated rather
than treating the intermittent result as a runtime measurement.

### CPU allocation, initialization and lifecycle

- Local-memory eligibility is filtered before any CPU selection. All five
  topology modes and both sharing policies use the same allocator. Management
  reservations remain governed by the complete parent CPU boundary
- Boundary shrink, recovery collision, failed admission, stale generation and
  release are checked against an independent lifecycle model and fuzzed
- Known invalid effective/online/explicit/reserved sets fail before support
  fallback. Unknown metadata cannot excuse known forbidden memory nodes or
  known conflicting explicit LLC/NUMA domains
- Capacity shortage never changes nonoverlapping allocation to an unplaced or
  shared policy. Required unsupported topology rejects; observable BestEffort
  fallback is limited to unavailable support
- Management weight is changed only after validation, recovery and hierarchy
  verification. An uncertain write fails initialization; ready is logged only
  after success
- Captured cgroup2 device/inode identity is checked before placement/signaling
  and emptiness proof. A missing/replaced root is not evidence of Actor exit
- Restarted live owners retain both CPU ownership and Actor admission capacity,
  including CoW-only tracking without cpu/cpuset controllers. Same-UID retry
  does not double-count its existing capacity
- Retry and restore prove prior execution stopped before network replacement or
  restoring filesystem data, even if the in-memory runningActor was lost
- Real CH/virtiofsd startup options contain the cgroup FD before Start, and
  post-Start readiness failures invoke the bounded cleanup callback. Wait and
  filesystem destruction do not precede strict kernel exit proof
- Strict cleanup bypasses legacy process-name scans. Independent review found
  that filesystem cleanup errors were being logged and swallowed; the helper
  now returns them. Failed strict teardown retains the hosted lease handle for
  a retry. Legacy cleanup order and disabled defaults remain intact

The stable-delegation assumption is explicit: a trusted host manager must not
move live Actor tasks outside the delegation or replace it across restart.
These checks are not isolation from an adversarial privileged host manager.
Uncertain ownership remains quarantined; there is no background reclamation
promise or automatic crash-orphaned cache lease reconciliation.

### Cache and actual VMM path

- Nonblocking opens reject FIFO sources; all memory/config/state descriptors are
  pinned before reading. Named and descriptor device/inode/size/mtime/ctime/mode
  checks prevent ordinary replacement, truncation and mixed-source admission
- Each metadata file is bounded at 16 MiB. Cancellation is checked between reads;
  it cannot interrupt a blocked filesystem syscall
- Cross-review reproduced a self-mutation ordering defect when acquiring from an
  existing cache lease. Source checks now precede cache timestamp/link updates
- An actual bounded subprocess acquires a lease and exits without cleanup; a
  new cache instance and GC cannot destroy it after the original source is
  truncated. No PID-reuse heuristic authorizes deletion
- The CH patch rejects missing memory-manager state, empty/partial/overlapping
  coverage and trailing/truncated memory data before replacing mappings
- The actual private mmap path and recheckpointing of live private bytes are
  source-tested. KVM clone isolation and repeated guest restore are unrun
- The guest harness validates the entire sparse/dense region, uses finite
  serial/API deadlines and bounded diagnostics, and preserves cleanup failures
- The cgroup hardware script runs only the exact delegated enforcement test,
  not unrelated legacy privileged mount/network tests

## Convergence and remaining gates

Independent cross-review covers non-owned code, default behavior, allocation
and lifecycle, cache crash safety, benchmark accounting and hardware command
scope. Final source is frozen before aggregate tests; later source changes must
invalidate/repeat affected evidence. Current source does not claim production
CoW: shared-memfd/external-virtiofs coherence remains dependency-blocked.

See [verification evidence](verification/README.md),
[acceptance gaps](acceptance-review.md), and
[production CoW feasibility](cow-production-feasibility.md).

## Later upstream observation

A read-only publication check observed upstream
`c427a6ae808ca4e5513604de549c8f6f88988151`, two commits after the tested c1ab
base. Its changed paths are README.md, the kind-cluster neighbor-table warning
script, and the credential-provider test fixture. A file-level comparison found
no overlap with this series. The public series remains based on c1ab; no actual
merge-tree execution or runtime validation of the later revision is claimed.
