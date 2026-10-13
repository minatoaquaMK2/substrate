# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Read-only best-effort local host observations; unavailable is never zero."""

import hashlib
import os
from pathlib import Path
import platform
import subprocess
import time


def read(path):
    try:
        return {"value": Path(path).read_text()}
    except (OSError, ValueError) as exc:
        return {"unavailable": str(exc)}


def capture(argv):
    try:
        result = subprocess.run(argv, capture_output=True, text=True, timeout=5, check=False)
        return {"value": result.stdout.strip(), "stderr": result.stderr.strip(), "exit_code": result.returncode}
    except (OSError, subprocess.TimeoutExpired) as exc:
        return {"unavailable": str(exc)}


def environment(target_local, manifest):
    root = Path(__file__).resolve().parents[2]
    return {"schema_version": 1, "kernel": platform.release(), "architecture": platform.machine(),
            "python": platform.python_version(), "hostname": platform.node(),
            "observation_scope": "target_host" if target_local else "load_generator_only",
            "virtualization": "unverified; visible topology is not proof of physical host topology",
            "kvm": {"exists": Path("/dev/kvm").exists(), "accessible": os.access("/dev/kvm", os.R_OK | os.W_OK)},
            "git_head": capture(["git", "-C", str(root), "rev-parse", "HEAD"]),
            "git_status": capture(["git", "-C", str(root), "status", "--short"]),
            "git_diff_sha256": hashlib.sha256(capture(["git", "-C", str(root), "diff"]).get("value", "").encode()).hexdigest(),
            "suite_source_sha256": {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in Path(__file__).parent.glob("*.py")},
            "cpu_allowed": read("/proc/self/status"), "cpuinfo": read("/proc/cpuinfo"),
            "mountinfo": read("/proc/self/mountinfo"), "self_cgroup": read("/proc/self/cgroup"),
            "cpu_online": read("/sys/devices/system/cpu/online"),
            "numa_online": read("/sys/devices/system/node/online"),
            "lscpu": capture(["lscpu", "--json"]),
            "operator_manifest": manifest,
            "unverified": ["VMM build and runtime modes unless manifest and server logs prove them",
                           "Kubernetes CPU/Topology Manager and pod budgets unless supplied in manifest",
                           "perf/eBPF signals, snapshot size and remote host metrics unless collected separately"]}


def sample(pids=(), cgroups=(), scope="load_generator_only"):
    result = {"type": "observation", "monotonic_s": time.monotonic(), "scope": scope,
              "host": {name: read("/proc/" + name) for name in ("meminfo", "vmstat", "pressure/cpu", "pressure/memory", "pressure/io")},
              "processes": {}, "cgroups": {}}
    for pid in pids:
        base = Path("/proc") / str(pid)
        before = read(base / "stat")
        proc = {name: read(base / name) for name in ("stat", "status", "schedstat", "io", "smaps_rollup", "numa_maps", "cgroup")}
        try:
            proc["threads"] = {p.name: {name: read(p / name) for name in ("status", "schedstat", "sched")}
                               for p in (base / "task").iterdir()}
        except OSError as exc:
            proc["threads"] = {"unavailable": str(exc)}
        after = read(base / "stat")
        # /proc/PID/stat field 22 is starttime; comm can contain spaces and ')'.
        def identity(value):
            try:
                return value["value"].rsplit(")", 1)[1].split()[19]
            except (KeyError, IndexError):
                return None
        proc["starttime_ticks"] = identity(before)
        proc["identity_stable"] = identity(before) is not None and identity(before) == identity(after)
        result["processes"][str(pid)] = proc
    for path in cgroups:
        result["cgroups"][path] = {name: read(Path(path) / name) for name in (
            "cpu.stat", "cpu.max", "cpu.weight", "cpu.pressure", "cpuset.cpus.effective",
            "cpuset.mems.effective", "memory.current", "memory.stat", "memory.pressure", "io.stat")}
    return result


def main():
    import argparse
    import json

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True)
    parser.add_argument("--duration", type=float, default=60)
    parser.add_argument("--interval", type=float, default=1)
    parser.add_argument("--pid", type=int, action="append", default=[])
    parser.add_argument("--cgroup", action="append", default=[])
    args = parser.parse_args()
    if not 0 < args.duration <= 3600 or not .05 <= args.interval <= 60:
        parser.error("duration must be in (0,3600], interval in [.05,60]")
    deadline = time.monotonic() + args.duration
    with Path(args.out).open("x") as file:
        while True:
            file.write(json.dumps(sample(args.pid, args.cgroup, "target_host_operator_selected"), sort_keys=True) + "\n")
            file.flush()
            if time.monotonic() >= deadline:
                break
            time.sleep(min(args.interval, max(0, deadline - time.monotonic())))


if __name__ == "__main__":
    main()
