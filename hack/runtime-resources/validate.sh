#!/usr/bin/env bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Focused hardware handoff. This never creates delegation, changes parent
# budgets, starts a cluster, or changes a global CPU/kernel setting.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"
: "${CPU_PLACEMENT_CGROUP_ROOT:?Set an existing empty delegated cgroup subtree you own; see docs/benchmarks/cpu-placement-validation.md}"
: "${RUNTIME_VALIDATION_OUTPUT:?Set a new absolute evidence directory}"
if [[ "$RUNTIME_VALIDATION_OUTPUT" != /* || -e "$RUNTIME_VALIDATION_OUTPUT" ]]; then
  echo "RUNTIME_VALIDATION_OUTPUT must be an absolute path that does not already exist" >&2
  exit 2
fi
mkdir -p "$RUNTIME_VALIDATION_OUTPUT"
{
  date -u +%FT%TZ
  git rev-parse HEAD
  git status --short
  uname -a
  go version
  printf 'delegated_root=%s\n' "$CPU_PLACEMENT_CGROUP_ROOT"
} > "$RUNTIME_VALIDATION_OUTPUT/environment.txt"
# Other package tests contain legacy root-only mount, namespace, and own-cgroup
# operations. This privileged handoff selects only the operator-owned subtree.
REQUIRE_CPU_PLACEMENT=true go test -race -count=1 -timeout=60s -v ./internal/ateomcgroup \
  -run '^TestCPUPlacementKernelEnforcement$' \
  2>&1 | tee "$RUNTIME_VALIDATION_OUTPUT/cgroup-tests.log"
echo "Scoped cgroup enforcement test passed; no runtime/KVM/NUMA performance claim."
echo "Run ordinary source tests separately without root; see docs/benchmarks/hardware-handoff.md."
