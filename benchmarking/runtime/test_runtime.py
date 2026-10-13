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

"""Dependency-free tests; none require KVM, a cluster or user credentials."""

import hashlib
import http.server
import json
from pathlib import Path
import re
import sys
import tempfile
import threading
import time
import unittest

from adapter import DeadlineExceeded, RequestRejected, SelftestAdapter, SubstrateAdapter, decode, field, varint
import observe
from runner import Trial, make_plan, percentile, repeat_summary, summarize, tails, trial_defaults, validate


def trial(**changes):
    value = trial_defaults()
    value.update(requests=8, rate=100, concurrency=2, queue_capacity=2, timeout_s=.2,
                 id="test-r0", scenario="test", repeat=0)
    value.update(changes)
    return value


class WireTests(unittest.TestCase):
    def test_wire_subset(self):
        self.assertEqual(varint(300), b"\xac\x02")
        self.assertEqual(field(1, "hello"), b"\x0a\x05hello")
        self.assertEqual(decode(field(1, 999) + field(2, b"abc")), {1: 999, 2: b"abc"})
        self.assertEqual(len(decode(field(2, .5))[2]), 8)

    def test_malformed(self):
        for data in (b"\x80", b"\x0a\x05a", b"\x00", b"\x0b", field(1, 1) + field(1, 2), b"\x08" + b"\x80" * 10, b"\x08" + b"\x80" * 9 + b"\x02"):
            with self.subTest(data=data), self.assertRaises(ValueError):
                decode(data)

    def test_glutton_schema_contract(self):
        source = (Path(__file__).resolve().parents[2] / "internal/proto/glutton/glutton.proto").read_text()
        contracts = {"PingRequest": [("string", "message", 1)], "PingResponse": [("string", "message", 1)],
                     "ReadRAMResponse": [("int64", "size", 1), ("uint32", "checksum", 2)],
                     "WriteRAMRequest": [("string", "key", 1), ("string", "size", 2)],
                     "ReadRAMRequest": [("string", "key", 1), ("string", "size", 2)],
                     "BurnCPURequest": [("int64", "duration_ms", 1), ("int32", "parallelism", 2)],
                     "BurnCPUResponse": [("int64", "iterations", 1)],
                     "IngestRequest": [("string", "key", 1), ("bytes", "payload", 2)],
                     "IngestResponse": [("int64", "size", 1), ("bytes", "sha256", 2)],
                     "ReadDiskRequest": [("string", "key", 1), ("ReadMode", "read_mode", 2)],
                     "ReadDiskResponse": [("int64", "size", 1), ("bytes", "sha256", 2)],
                     "UseCPURequest": [("int32", "num_cores", 1), ("double", "duty_cycle", 2),
                                       ("int32", "cycle_length_ms", 3), ("bool", "cap_at_gomaxprocs", 4)],
                     "UseCPUResponse": [("int32", "num_cores", 1)]}
        for message, fields in contracts.items():
            body = re.search(r"message " + message + r"\s*\{([^}]+)\}", source).group(1)
            for typ, name, number in fields:
                self.assertRegex(body, rf"\b{typ}\s+{name}\s*=\s*{number}\s*;")


class AdapterTests(unittest.TestCase):
    def setUp(self):
        self.calls, self.payload, self.corrupt = [], b"", False
        test = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                data = self.rfile.read(int(self.headers["Content-Length"]))
                test.calls.append((self.path, self.headers.get("ate-target-actor"), decode(data)))
                values = decode(data)
                if self.path == "/ping":
                    output = field(1, "wrong") if test.corrupt else data
                elif self.path == "/ingest":
                    test.payload = values[2]
                    output = field(1, len(test.payload)) + field(2, hashlib.sha256(test.payload).digest())
                elif self.path == "/readdisk":
                    output = field(1, len(test.payload)) + field(2, hashlib.sha256(test.payload).digest())
                elif self.path == "/readram":
                    output = field(1, int(values[2])) + field(2, 37)
                elif self.path == "/burncpu":
                    output = field(1, 10)
                elif self.path == "/usecpu":
                    output = field(1, values[1])
                else:
                    output = b""
                self.send_response(200)
                self.send_header("Content-Length", str(len(output)))
                self.end_headers()
                self.wfile.write(output)

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        self.adapter = SubstrateAdapter([], "bench", "bench/glutton", f"http://127.0.0.1:{self.server.server_port}", lambda row: None)

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def test_guest_commands_and_verification(self):
        for workload in ("echo", "cpu", "io", "sparse", "dense"):
            config = trial(workload=workload)
            deadline = time.monotonic() + 2
            expected = self.adapter.initialize("actor", config, deadline)
            result = self.adapter.execute("actor", "token-1", config, expected, deadline)
            self.assertEqual(result["echo_sha256"], hashlib.sha256(b"token-1").hexdigest())
        self.assertEqual(len(self.payload), 1024 * 1024)
        self.adapter.load("actor", 3, .5, time.monotonic() + 2)
        self.assertTrue(all(call[1] == "bench/actor" for call in self.calls))
        self.assertEqual(self.calls[-1][2][4], 0)

    def test_integer_cpu_duty_uses_schema_double(self):
        self.adapter.load("actor", 1, 1, time.monotonic() + 1)
        self.assertEqual(self.calls[-1][2][2], __import__("struct").pack("<d", 1.0))

    def test_corrupt_echo_fails(self):
        self.corrupt = True
        with self.assertRaisesRegex(ValueError, "echo"):
            self.adapter.execute("actor", "token-1", trial(), {}, time.monotonic() + 1)

    def test_lifecycle_cli_arguments(self):
        with tempfile.TemporaryDirectory() as tmp:
            script, calls = Path(tmp) / "cli.py", Path(tmp) / "calls.jsonl"
            script.write_text("import json,sys\nwith open(sys.argv[1], 'a') as f: f.write(json.dumps(sys.argv[2:])+'\\n')\nprint('{}')\n")
            self.adapter.cli = [sys.executable, str(script), str(calls)]
            for operation in ("create", "resume", "suspend", "pause", "delete"):
                self.adapter.lifecycle(operation, "fixture", time.monotonic() + 2)
            args = [json.loads(line) for line in calls.read_text().splitlines()]
            self.assertEqual(args[0], ["create", "actor", "fixture", "--atespace", "bench", "--template", "bench/glutton", "--output", "json"])
            self.assertEqual(args[-1][-1], "--any-state")

    def test_cli_deadline(self):
        self.adapter.cli = [sys.executable, "-c", "import time;time.sleep(10)"]
        start = time.monotonic()
        with self.assertRaises(DeadlineExceeded):
            self.adapter.lifecycle("resume", "fixture", start + .02)
        self.assertLess(time.monotonic() - start, 1)


class SchedulerTests(unittest.TestCase):
    def execute(self, config, adapter=None):
        adapter = adapter or SelftestAdapter()
        events, stop = [], threading.Event()
        run = Trial(adapter, config, "rtb-test", events.append, stop)
        result = run.run()
        self.assertFalse(adapter.actors, "fixture actors leaked")
        self.assertEqual(len([e for e in events if e["type"] == "request"]), config["requests"])
        return result, events

    def test_all_lifecycles_cleanup(self):
        for kind in ("create_destroy", "resume_storm", "pause_resume", "warm_exec", "memory_density"):
            config = trial(kind=kind, source="golden" if kind == "create_destroy" else "per_actor", rate=100)
            summary, events = self.execute(config)
            self.assertEqual(summary["counts"], {"ok": config["requests"]})
            if kind == "warm_exec":
                self.assertTrue(all("resume_api" not in row["phases_s"] for row in events if row["type"] == "request"))

    def test_burst_keeps_dropped_arrivals(self):
        summary, events = self.execute(trial(requests=50, rate=0, concurrency=1, queue_capacity=1), SelftestAdapter(.02))
        self.assertGreater(summary["counts"]["dropped"], 0)
        self.assertEqual(sum(summary["counts"].values()), 50)
        self.assertTrue(all(row["scheduled_offset_s"] == 0 for row in events if row["type"] == "request"))

    def test_deadline_includes_queue(self):
        summary, events = self.execute(trial(requests=4, rate=0, concurrency=1, queue_capacity=4, timeout_s=.015), SelftestAdapter(.01))
        self.assertGreater(summary["counts"]["timeout"], 0)
        self.assertTrue(any(row.get("failed_phase") == "queue" for row in events))
        self.assertEqual(summary["first_exec"]["n"], summary["counts"].get("ok", 0))

    def test_failures_are_not_success_samples(self):
        summary, events = self.execute(trial(), SelftestAdapter(.001, fail_every=2))
        self.assertEqual(summary["counts"], {"ok": 4, "error": 4})
        self.assertEqual(summary["first_exec"]["n"], 4)
        self.assertEqual(summary["failure_fraction"], .5)

    def test_rejections_are_counted_separately(self):
        class Rejecting(SelftestAdapter):
            def execute(self, *args):
                raise RequestRejected("injected resource exhaustion")
        summary, events = self.execute(trial(), Rejecting())
        self.assertEqual(summary["counts"], {"rejected": 8})
        self.assertEqual(summary["first_exec"]["n"], 0)

    def test_error_limit_aborts_and_cleans(self):
        summary, events = self.execute(trial(abort_after_errors=1, requests=30, rate=100), SelftestAdapter(.001, fail_every=1))
        self.assertIn("aborted", summary["counts"])

    def test_setup_failure_cleans_partial_creation(self):
        class Failing(SelftestAdapter):
            def initialize(self, actor, config, deadline):
                raise RuntimeError("fixture setup failed")
        adapter, events = Failing(), []
        result = Trial(adapter, trial(), "rtb-test", events.append, threading.Event()).run()
        self.assertEqual(result["status"], "setup_or_run_failed")
        self.assertFalse(adapter.actors)

    def test_cleanup_failure_is_preserved(self):
        class Failing(SelftestAdapter):
            def lifecycle(self, operation, actor, deadline):
                if operation == "delete":
                    raise RuntimeError("injected deletion failure")
                return super().lifecycle(operation, actor, deadline)
        adapter, events = Failing(), []
        run = Trial(adapter, trial(requests=1), "rtb-test", events.append, threading.Event())
        run.run()
        self.assertTrue(run.cleanup_failed)
        self.assertEqual(events[-1]["remaining_actors"], ["rtb-test-0"])


class PlanAndReportTests(unittest.TestCase):
    def test_randomization_reproducible(self):
        a = make_plan(4, 3, 7)
        validate(a)
        self.assertEqual(a, make_plan(4, 3, 7))
        self.assertNotEqual(a, make_plan(4, 3, 8))
        self.assertEqual(len(a["trials"]), 39)
        self.assertNotEqual([t["scenario"] for t in a["trials"][:13]], [t["scenario"] for t in a["trials"][-13:]])

    def test_invalid_plans_fail_before_execution(self):
        for key, value in (("rate", -1), ("timeout_s", float("nan")), ("queue_capacity", -1),
                           ("snapshot_cache", "operator_cold"), ("concurrency", 0), ("requests", 100001)):
            plan = make_plan(4, 3, 7)
            plan["trials"][0][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                validate(plan)

    def test_small_samples_never_claim_p999(self):
        self.assertEqual(tails([1, 2, 3])["n"], 3)
        self.assertIsNone(tails(list(range(9999)))["p999_s"])
        self.assertIsNone(tails(list(range(10000)))["p999_s"])
        self.assertEqual(percentile([1, 2, 3, 4], .5), 2)
        self.assertIsNone(percentile([], .99))

    def test_repeat_ranges_need_three(self):
        rows = [{"status": "finished", "scenario": "a", "repeat": n,
                 "scenario_config_sha256": "same", "first_exec": tails([n, n + 1])} for n in (1, 2, 3)]
        self.assertIsNone(repeat_summary(rows[:2])["a"]["p99_s_range"])
        self.assertEqual(repeat_summary(rows)["a"]["p99_s_range"], [2, 4])
        self.assertIsNone(repeat_summary(rows)["a"]["p999_s_range"])

    def test_p999_requires_samples_repeats_and_stability(self):
        rows = [{"status": "finished", "scenario": "a", "repeat": i,
                 "scenario_config_sha256": "same", "first_exec": tails([1] * 10000),
                 "_p999_candidate_s": n} for i, n in enumerate((1.0, 1.02, 1.01))]
        self.assertIsNone(repeat_summary(rows[:2])["a"]["p999_s_range"])
        self.assertEqual(repeat_summary(rows)["a"]["p999_s_range"], [1., 1.02])
        rows[-1]["_p999_candidate_s"] = 2.
        self.assertIsNone(repeat_summary(rows)["a"]["p999_s_range"])
        rows[-1]["_p999_candidate_s"] = 1.
        rows[-1]["first_exec"]["n"] = 9999
        self.assertIsNone(repeat_summary(rows)["a"]["p999_s_range"])
        rows[-1]["first_exec"]["n"] = 10000
        rows[-1]["scenario_config_sha256"] = "changed"
        self.assertIsNone(repeat_summary(rows)["a"]["p999_s_range"])

    def test_unavailable_observations_are_explicit(self):
        value = observe.sample([999999999], ["/definitely-absent-cgroup"])
        self.assertIn("unavailable", value["processes"]["999999999"]["smaps_rollup"])
        self.assertFalse(value["processes"]["999999999"]["identity_stable"])
        self.assertIn("unavailable", value["cgroups"]["/definitely-absent-cgroup"]["memory.current"])


if __name__ == "__main__":
    unittest.main()
