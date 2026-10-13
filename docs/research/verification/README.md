# Runtime verification summary and reproduction

## Provenance and limits

The results below belong to original tested source
**`65097aaff066dcb3769a9e38d9fc57734a0f67ab`**, after integrating upstream
`c1ab95dea49bacfdeefcb00dd67b66d27372021c`. The public branch preserves production/test source bytes and the
independent CH patch, while replacing documentation and evidence references.
[Source hashes](source-hashes.json) identify the changed source files for
comparison. This does not assert byte-identical whole-tree publication or
transfer a historical result to a new commit without final-candidate checks.

Full original evidence is privately retained. Public contents are implementation,
tests, aggregate summaries and reproduction instructions. No captured host or
environment records, raw benchmark rows, raw test output, private artifact
locations or original evidence ancestors are published. A new run produces its
own private evidence and may differ in scheduler-dependent counts.

## Aggregate results

- Full `hack/verify-all.sh`: **passed**, with clean original source before and
  after; code generation produced no tracked changes
- Root-module race inventory: **214 packages**, 213 successful package commands
  and one failed command; 138 packages executed tests, 76 executed none
- Top-level tests: **2,453 passed, 1 failed and 344 skipped**
- **58 named AF_UNIX test families explicitly excluded** after socket-operation
  denials; they were not retried or counted as passing
- Only failure: `internal/tarutil/TestExtractAbsoluteNameStaysInside`, an
  ownership-restoration failure also reproduced on pristine `c1ab95dea49bacfdeefcb00dd67b66d27372021c` using
  matching normalized process settings
- Four Linux amd64 entrypoint builds and full Linux arm64 root-module cross-build:
  **passed**
- Python: **41 benchmark, 9 CoW handoff, 1 delegation-scope and 3 evidence-
  classifier tests passed**; native Go Glutton protocol smoke **passed**
- Benchmark timeout fixture: 100 repeated checks passed; the old adapter still
  failed the bounded-drain regression

The current-upstream baseline recheck covered its 10 changed package directories
plus tarutil: 493 passing tests, one tar failure, 127 skips and six explicit
AF_UNIX exclusions. This was an affected-package comparison, not a fresh full
current-upstream test run. Earlier full-baseline and tracing/color normalization
results remain private and are not relabeled as current-upstream full coverage.

See the [machine-readable aggregate summary](validation-summary.json) and
[name-only exclusion inventory](af-unix-exclusions.json). Failures, skips and
exclusions are distinct. These results are **not an unrestricted green
`go test -race ./...`**, hardware acceptance, or a performance claim.

## Source-only reproduction

Run from a clean checkout on a suitably provisioned Linux host. Use the project's
normal dependency/tool setup; the original Go verification used Go 1.27.0. Keep
logs and generated run outputs in an operator-selected private location. None of
these commands provisions a cluster or authorizes privileged hardware work.

```sh
# Record the new checkout identity separately from the original result.
git rev-parse HEAD
git status --short

# Process-only normalization used by the original Go validation.
unset NO_COLOR OTEL_TRACES_SAMPLER_ARG
export OTEL_TRACES_SAMPLER=always_on TZ=UTC GOMAXPROCS=4
hack/verify-all.sh

# On a host with the normal test prerequisites, prefer unfiltered coverage.
go test -race -p 2 -count=1 -timeout=5m ./...

python3 -m unittest discover -s benchmarking/runtime -p 'test_*.py' -v
python3 -m unittest discover -s hack/mmap-cow -p 'test_*.py' -v
python3 -m unittest discover -s hack/runtime-resources -p 'test_*.py' -v
python3 -m unittest discover -s hack/util -p 'test_verify_boilerplate.py' -v

GOOS=linux GOARCH=amd64 go build -p 2 \
  ./cmd/atecontroller ./cmd/atelet ./cmd/ateom-microvm ./cmd/ateom-gvisor
GOOS=linux GOARCH=arm64 go build -p 2 ./...

# Private temporary output for native protocol smoke, without a VM.
SMOKE_DIR="$(mktemp -d)"
go build -o "$SMOKE_DIR/glutton" ./cmd/benchmarking/glutton
python3 benchmarking/runtime/smoke_glutton.py --binary "$SMOKE_DIR/glutton"
```

The smoke directory is left for the operator to inspect and remove. Native smoke
exercises the workload protocol; it does not establish Actor lifecycle, guest
correctness or runtime performance.

### Reproduce the historical restricted test selection

The original restricted run used package-specific anchored exclusions rather
than a broad name filter. To generate equivalent package commands, use the
name-only inventory below. Do not use it to mask unrelated failures on a capable
host; run excluded tests there and report their outcomes separately.

```sh
python3 - <<'PYTHON'
import json, re, shlex, subprocess
from collections import defaultdict
from pathlib import Path

inventory = json.loads(Path("docs/research/verification/af-unix-exclusions.json").read_text())
exclusions = defaultdict(list)
for entry in inventory["exclusions"]:
    exclusions[entry["package"]].append(entry["test"])
for package in subprocess.check_output(["go", "list", "./..."], text=True).splitlines():
    command = ["env", "-u", "NO_COLOR", "-u", "OTEL_TRACES_SAMPLER_ARG",
               "OTEL_TRACES_SAMPLER=always_on", "TZ=UTC", "GOMAXPROCS=4",
               "go", "test", "-race", "-json", "-p", "2", "-count=1", "-timeout=5m"]
    if package in exclusions:
        command += ["-skip", "^(" + "|".join(re.escape(name) for name in exclusions[package]) + ")$"]
    command.append(package)
    print(shlex.join(command))
PYTHON
```

This prints commands for inspection; it does not execute them. Record each
command's exit code and full output when running the inventory. The original
run allowed two package commands concurrently. Package counts can change with
new source, build tags or toolchain settings; never silently reuse the original
counts. Nested tool modules are covered only where verifier scripts invoke them.

## Independent VMM and hardware boundaries

The [CH dependency instructions](../../upstream/dependencies/README.md) pin the
patch and supply source-build and guest-harness commands. Eleven CoW source
tests, all-target source check and debug binary build passed. Strict Clippy and
formatting retain baseline failures; a named Clippy-exception run passed.
Full VMM guest-suite execution was not rerun.

Production shared-memfd/external-virtiofs CoW remains **dependency-blocked**.
KVM guest validation, real delegated-cgroup enforcement, physical NUMA/LLC,
live-cluster comparisons, cache experiments and fixed-SLO density acceptance
are all **unrun**. Use the [hardware handoff](../../benchmarks/hardware-handoff.md)
on separately authorized hardware. Required prerequisite branches must fail
closed; a skip never proves acceptance. No speedup or isolation claim follows
from source tests, a cross-build or native protocol smoke.
