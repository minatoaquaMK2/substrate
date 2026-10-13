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

"""Adversarial, local-only integrity regressions; no credentials or VM required."""

import argparse
import copy
import json
from pathlib import Path
import socketserver
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

import adapter
import runner
from test_runtime import trial


def actor_response(name, source="per_actor", uuid=None, uri=None, generation=2, paused=False):
    uuid = uuid or ("golden-uuid" if source == "golden" else name + "-uuid")
    return {"metadata": {"uid": name + "-uid"}, "status": {
        "state": "ACTOR_STATE_PAUSED" if paused else "ACTOR_STATE_SUSPENDED",
        "snapshots": [{"uuid": uuid, "actorTemplateUid": "template-uid", "generation": generation,
                       "fidelity": "SNAPSHOT_FIDELITY_MEMORY",
                       "owner": "SNAPSHOT_OWNER_TAG" if source == "golden" else "SNAPSHOT_OWNER_ACTOR",
                       "locality": "fixture-worker" if paused else "",
                       "durableSnapshot": {"status": "SNAPSHOT_STORAGE_STATUS_COMPLETED",
                                           "object": {"snapshotUri": uri or "fixture://" + uuid}}}]}}


class ProvenanceAdapter(adapter.SelftestAdapter):
    synthetic = False

    def __init__(self, source="per_actor", change=None):
        super().__init__(0)
        self.source, self.change = source, change

    def lifecycle(self, operation, actor, deadline):
        super().lifecycle(operation, actor, deadline)
        value = actor_response(actor, self.source, paused=operation == "pause")
        if self.change:
            self.change(value, actor, operation)
        return value


class TransportIntegrityTests(unittest.TestCase):
    def make_adapter(self, log=lambda row: None):
        return adapter.SubstrateAdapter([], "fixture", "fixture/template", "http://127.0.0.1:1", log)

    def test_cli_already_exists_has_an_ownership_exception(self):
        value = self.make_adapter()
        value.cli = [sys.executable, "-c", "import sys;sys.stderr.write('x'*3000+' rpc error: code = AlreadyExists desc = fixture');sys.exit(1)"]
        try:
            value.lifecycle("create", "existing", time.monotonic() + 2)
        except Exception as exc:
            self.assertEqual(type(exc).__name__, "ActorAlreadyExists")
        else:
            self.fail("collision was accepted")

    def test_expired_cli_does_not_start_a_process(self):
        value = self.make_adapter()
        with mock.patch.object(adapter.subprocess, "Popen", side_effect=AssertionError("expired command started a process")) as popen:
            with self.assertRaises(adapter.DeadlineExceeded):
                value.lifecycle("create", "expired", time.monotonic() - 1)
            popen.assert_not_called()

    def test_detached_cli_output_does_not_extend_deadline(self):
        rows, value = [], self.make_adapter()
        value.log = rows.append
        # Start the finite helper before the measured call. Readiness proves the
        # escaped child actually holds the pipes, independent of build-host load.
        child = ("import pathlib,sys,time; ready,release,done=map(pathlib.Path,sys.argv[1:]); "
                 "ready.write_text('ready'); end=time.monotonic()+5\n"
                 "while not release.exists() and time.monotonic()<end: time.sleep(.005)\n"
                 "done.write_text('done')\n")
        parent = ("import subprocess,sys,time; "
                  f"subprocess.Popen([sys.executable,'-c',{child!r},*sys.argv[1:]],start_new_session=True); "
                  "time.sleep(10)")
        with tempfile.TemporaryDirectory() as temp:
            ready, release, done = [Path(temp) / name for name in ("ready", "release", "done")]
            process = subprocess.Popen([sys.executable, "-c", parent, str(ready), str(release), str(done)],
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            try:
                deadline = time.monotonic() + 5
                while not ready.exists() and time.monotonic() < deadline:
                    time.sleep(.005)
                self.assertTrue(ready.exists(), "detached helper did not become ready")
                # Only process construction is substituted; timeout, killpg,
                # output draining and all monotonic timestamps are real.
                with mock.patch.object(adapter.subprocess, "Popen", return_value=process):
                    start = time.monotonic()
                    with self.assertRaises(adapter.DeadlineExceeded):
                        value.lifecycle("resume", "fixture", start + .08)
                    self.assertLess(time.monotonic() - start, .5)
                self.assertFalse(rows[-1]["output_complete"])
            finally:
                release.write_text("release")
                if process.poll() is None:
                    process.kill()
                process.wait(timeout=2)
                deadline = time.monotonic() + 2
                while not done.exists() and time.monotonic() < deadline:
                    time.sleep(.005)
                for stream in (process.stdout, process.stderr):
                    stream.close()
                self.assertTrue(done.exists(), "detached helper did not acknowledge cleanup")

    def test_negative_signed_cpu_iterations_cannot_verify(self):
        value = self.make_adapter()
        def command(actor, route, body, deadline):
            return adapter.field(1, (1 << 64) - 1) if route == "/burncpu" else body
        value.command = command
        with self.assertRaises(ValueError):
            value.execute("fixture", "echo", trial(workload="cpu"), {}, time.monotonic() + 1)

    def http_fixture(self, responder, check):
        class Handler(socketserver.BaseRequestHandler):
            def handle(self):
                self.request.recv(65536)
                try:
                    responder(self.request)
                except (BrokenPipeError, ConnectionResetError):
                    pass
        class Server(socketserver.ThreadingTCPServer):
            allow_reuse_address = True
            daemon_threads = True
        with Server(("127.0.0.1", 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": .01})
            thread.start()
            try:
                value = adapter.SubstrateAdapter([], "fixture", "unused", f"http://127.0.0.1:{server.server_address[1]}", lambda row: None)
                check(value)
            finally:
                server.shutdown()
                thread.join()

    def test_trickled_headers_cannot_extend_arrival_deadline(self):
        def respond(sock):
            for part in (b"HTTP/1.1 200 OK\r\n", b"X-Delay: ", *([b"x"] * 30), b"\r\nContent-Length: 0\r\n\r\n"):
                sock.sendall(part)
                time.sleep(.007)
        def check(value):
            start = time.monotonic()
            with self.assertRaises(adapter.DeadlineExceeded):
                value.command("fixture", "/ping", b"", start + .04)
            self.assertLess(time.monotonic() - start, .16)
        self.http_fixture(respond, check)

    def test_truncated_content_length_is_not_success(self):
        def respond(sock):
            sock.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 30\r\nConnection: close\r\n\r\n" + adapter.field(1, "echo"))
        def check(value):
            with self.assertRaises((ValueError, __import__("http.client").client.IncompleteRead)):
                value.execute("fixture", "echo", trial(), {}, time.monotonic() + 1)
        self.http_fixture(respond, check)


class TrialIntegrityTests(unittest.TestCase):
    def test_already_existing_actor_is_not_deleted(self):
        value = adapter.SelftestAdapter(0)
        value.actors["fixture-0"] = "suspended"
        run = runner.Trial(value, trial(requests=1), "fixture", lambda row: None, threading.Event())
        result = run.run()
        self.assertEqual(result["status"], "setup_or_run_failed")
        self.assertIn("fixture-0", value.actors)
        self.assertNotIn("fixture-0", run.owned)

    def test_delete_failure_stops_sustained_creation(self):
        class Failing(adapter.SelftestAdapter):
            def lifecycle(self, operation, actor, deadline):
                if operation == "delete":
                    raise RuntimeError("injected deletion failure")
                return super().lifecycle(operation, actor, deadline)
        value, events = Failing(0), []
        config = trial(kind="create_destroy", source="golden", requests=12, concurrency=1, rate=100)
        run = runner.Trial(value, config, "fixture", events.append, threading.Event())
        result = run.run()
        self.assertTrue(run.cleanup_failed)
        self.assertEqual(len(value.actors), 1)
        self.assertEqual(result["counts"]["aborted"], 11)
        self.assertEqual(events[-1]["remaining_actors"], ["fixture-0"])

    def test_recording_failure_stops_and_drains_without_leaking(self):
        # Isolate the expected pre-fix deadlock, keeping the regression itself bounded.
        code = '''import json,threading
from adapter import SelftestAdapter
from runner import Trial
from test_runtime import trial
value=SelftestAdapter(0)
def emit(row):
    if row['type']=='request': raise OSError('injected output failure')
run=Trial(value,trial(requests=20,rate=0,queue_capacity=20),'fixture',emit,threading.Event())
result=run.run()
print(json.dumps({'status':result['status'],'actors':list(value.actors),'rows':len(run.rows),'owned':list(run.owned)}))
'''
        proc = subprocess.run([sys.executable, "-c", code], cwd=Path(runner.__file__).parent,
                              capture_output=True, text=True, timeout=3)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        result = json.loads(proc.stdout)
        self.assertEqual(result, {"status": "recording_failed", "actors": [], "rows": 20, "owned": []})

    def test_observation_failure_cannot_report_a_successful_run(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            (base / "plan.json").write_text(json.dumps({"schema_version": 1, "trials": [trial(requests=1)]}))
            args = argparse.Namespace(plan=str(base / "plan.json"), out=str(base / "out"), adapter="selftest",
                manifest=None, target_host_local=False, pid=[], cgroup=[], sample_interval=.05,
                selftest_delay=0, selftest_fail_every=0)
            with mock.patch.object(runner.observe, "sample", side_effect=OSError("injected observer failure")):
                status = runner.run(args)
            self.assertEqual(status, 1)
            report = json.loads((base / "out/summary.json").read_text())
            self.assertIn("observer failure", report["observation_error"])
            self.assertEqual(report["repeat_variation"], {})
            self.assertIn("injected observer failure", (base / "out/summary.md").read_text())


class SnapshotIntegrityTests(unittest.TestCase):
    def run_fixture(self, source="per_actor", change=None, **config):
        value, events = ProvenanceAdapter(source, change), []
        run = runner.Trial(value, trial(requests=3, rate=100, source=source, **config), "fixture", events.append, threading.Event())
        result = run.run()
        self.assertFalse(value.actors)
        return result, events

    def test_per_actor_cannot_retain_tag_snapshot(self):
        def change(value, actor, operation):
            value["status"]["snapshots"][0]["owner"] = "SNAPSHOT_OWNER_TAG"
        result, _ = self.run_fixture(change=change)
        self.assertEqual(result["status"], "setup_or_run_failed")

    def test_per_actor_volume_only_snapshot_is_not_memory_restore(self):
        def change(value, actor, operation):
            value["status"]["snapshots"][0]["fidelity"] = "SNAPSHOT_FIDELITY_VOLUMES"
        result, _ = self.run_fixture(change=change)
        self.assertEqual(result["status"], "setup_or_run_failed")

    def test_distinct_actors_cannot_reuse_same_snapshot(self):
        for key in ("uuid", "uri"):
            def change(value, actor, operation):
                snap = value["status"]["snapshots"][0]
                if key == "uuid":
                    snap["uuid"] = "reused"
                else:
                    snap["durableSnapshot"]["object"]["snapshotUri"] = "fixture://reused"
            with self.subTest(key=key):
                result, _ = self.run_fixture(change=change)
                self.assertEqual(result["status"], "setup_or_run_failed")

    def test_golden_rotation_cannot_be_reported_as_one_shared_source(self):
        for key in ("uuid", "uri"):
            def change(value, actor, operation):
                snap = value["status"]["snapshots"][0]
                if key == "uuid":
                    snap["uuid"] = actor
                else:
                    snap["durableSnapshot"]["object"]["snapshotUri"] = "fixture://" + actor
            with self.subTest(key=key):
                result, _ = self.run_fixture(source="golden", change=change)
                self.assertEqual(result["status"], "setup_or_run_failed")

    def test_pause_uses_local_snapshot_provenance(self):
        def change(value, actor, operation):
            value["status"]["snapshots"][0].pop("durableSnapshot")
        result, events = self.run_fixture(change=change, kind="pause_resume")
        self.assertEqual(result["counts"], {"ok": 3})
        rows = [e for e in events if e["type"] == "snapshot_source"]
        self.assertEqual(len(rows), 3)
        self.assertTrue(all(e["provenance"]["locality"] and e["provenance"]["uri"] == "" for e in rows))

    def test_valid_shared_and_distinct_sources_are_recorded(self):
        for source in ("golden", "per_actor"):
            with self.subTest(source=source):
                result, events = self.run_fixture(source=source)
                self.assertEqual(result["counts"], {"ok": 3})
                snapshots = [e["provenance"] for e in events if e["type"] == "snapshot_source"]
                self.assertEqual(len(snapshots), 3)
                self.assertEqual(len({s["uuid"] for s in snapshots}), 1 if source == "golden" else 3)
                self.assertTrue(all(s["generation"] == 2 and s["owner"] for s in snapshots))

    def test_latest_usable_generation_cannot_be_bypassed(self):
        def change(value, actor, operation):
            snap = copy.deepcopy(value["status"]["snapshots"][0])
            snap.update(generation=3, fidelity="SNAPSHOT_FIDELITY_VOLUMES")
            value["status"]["snapshots"].append(snap)
        result, _ = self.run_fixture(change=change)
        self.assertEqual(result["status"], "setup_or_run_failed")

    def test_changed_golden_source_blocks_repeated_tail_gate(self):
        rows = []
        for index in range(3):
            def change(value, actor, operation):
                value["status"]["snapshots"][0]["uuid"] = "golden-" + str(index)
            result, _ = self.run_fixture(source="golden", change=change, repeat=index)
            result.update(first_exec=runner.tails([1] * 10000), _p999_candidate_s=1)
            rows.append(result)
        summary = runner.repeat_summary(rows)["test"]
        self.assertIsNone(summary["p999_s_range"])
        self.assertIsNone(summary["p99_s_range"])

    def test_all_tail_ranges_require_distinct_comparable_repeats(self):
        original = [{"status": "finished", "scenario": "a", "repeat": i, "scenario_config_sha256": "same",
                     "first_exec": runner.tails([1, 2, 3])} for i in range(3)]
        for mutation in (lambda r: r.update(repeat=0), lambda r: r.update(scenario_config_sha256="different"),
                         lambda r: r.pop("scenario_config_sha256")):
            rows = copy.deepcopy(original)
            mutation(rows[-1])
            result = runner.repeat_summary(rows)["a"]
            for key in ("p50_s_range", "p95_s_range", "p99_s_range"):
                self.assertIsNone(result[key])


if __name__ == "__main__":
    unittest.main()
