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

"""Exercise the real native Glutton workload, without claiming VM validation."""

import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.request

from adapter import SubstrateAdapter
from runner import trial_defaults


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="Glutton built from this checkout")
    parser.add_argument("--log-out", help="new file preserving native server stdout/stderr")
    args = parser.parse_args()
    if args.log_out and Path(args.log_out).exists():
        parser.error("--log-out must not already exist")
    binary = str(Path(args.binary).resolve())
    with tempfile.TemporaryDirectory(prefix="runtime-glutton-") as tmp:
        port, metrics_port = unused_port(), unused_port()
        env = dict(os.environ, OTEL_TRACES_EXPORTER="none", OTEL_METRICS_EXPORTER="none")
        with (Path(tmp) / "server.log").open("w+") as log:
            proc = subprocess.Popen([binary, "--mode=http", f"--grpc-listen-addr=127.0.0.1:{port}",
                                     f"--metrics-listen-addr=127.0.0.1:{metrics_port}", f"--data-dir={tmp}/data"],
                                    stdout=log, stderr=log, env=env)
            try:
                deadline = time.monotonic() + 10
                while True:
                    try:
                        with urllib.request.urlopen(f"http://127.0.0.1:{port}/readyz", timeout=.2) as response:
                            if response.status == 200:
                                break
                    except OSError:
                        if proc.poll() is not None or time.monotonic() >= deadline:
                            log.seek(0)
                            raise RuntimeError("native Glutton startup failed: " + log.read()) from None
                        time.sleep(.05)
                adapter = SubstrateAdapter([], "local-test", "unused", f"http://127.0.0.1:{port}", lambda row: None)
                results = []
                for workload in ("echo", "cpu", "io", "sparse", "dense"):
                    trial = trial_defaults()
                    trial["workload"] = workload
                    deadline = time.monotonic() + 10
                    expected = adapter.initialize("local-test", trial, deadline)
                    verification = adapter.execute("local-test", "native-protocol-0", trial, expected, deadline)
                    results.append({"workload": workload, "verification": verification})
                for cores, duty in ((1, .1), (1, 1), (0, 0)):
                    adapter.load("local-test", cores, duty, time.monotonic() + 1)
                print(json.dumps({"native_glutton_protocol_smoke": "passed", "microvm_test": False,
                                  "lifecycle_test": False, "commands": results,
                                  "cpu_load_cases": [[1, .1], [1, 1], [0, 0]]}, indent=2))
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                if args.log_out:
                    log.seek(0)
                    with Path(args.log_out).open("x") as saved:
                        saved.write(log.read())


if __name__ == "__main__":
    main()
