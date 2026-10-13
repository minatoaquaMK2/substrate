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

"""Bounded open-loop lifecycle/verified-guest benchmark using existing tools."""

import argparse
import collections
import datetime
import hashlib
import json
import math
from pathlib import Path
import queue
import random
import shlex
import signal
import sys
import threading
import time
import uuid

from adapter import ActorAlreadyExists, DeadlineExceeded, RequestRejected, SelftestAdapter, SubstrateAdapter, remaining
import observe


KINDS = {"create_destroy", "resume_storm", "warm_exec", "pause_resume", "memory_density"}
WORKLOADS = {"echo", "cpu", "io", "sparse", "dense"}


def trial_defaults():
    return dict(requests=20, rate=5.0, concurrency=4, queue_capacity=8, timeout_s=30.0,
                setup_timeout_s=120.0, cleanup_timeout_s=30.0, kind="resume_storm", workload="echo",
                source="per_actor", competitors=0, competitor_cores=1, competitor_duty=0.0,
                memory_bytes=8 * 1024 * 1024, io_bytes=1024 * 1024, cpu_ms=20,
                hold_s=0.0, snapshot_cache="uncontrolled", file_cache="uncontrolled",
                abort_after_errors=0)


def make_plan(requests, repeats, seed):
    cases = [
        ("activation-golden", {"kind": "create_destroy", "source": "golden"}),
        ("golden-clone-storm", {"rate": 0, "source": "golden"}),
        ("distinct-resume", {}),
        ("pause-resume", {"kind": "pause_resume"}),
        ("warm-command", {"kind": "warm_exec"}),
        ("cpu-low", {"workload": "cpu", "competitors": 1, "competitor_duty": 0.1}),
        ("cpu-medium", {"workload": "cpu", "competitors": 1, "competitor_duty": 0.5}),
        ("cpu-high", {"workload": "cpu", "competitors": 2, "competitor_duty": 1.0}),
        ("overload", {"workload": "cpu", "rate": 1000, "cpu_ms": 100, "queue_capacity": 4}),
        ("io", {"workload": "io"}),
        ("sparse", {"workload": "sparse"}),
        ("dense", {"workload": "dense"}),
        ("density", {"kind": "memory_density", "workload": "dense", "hold_s": 2.0}),
    ]
    rng, trials = random.Random(seed), []
    for repeat in range(repeats):
        order = list(cases)
        rng.shuffle(order)
        for name, changes in order:
            value = trial_defaults()
            value.update(changes, requests=requests, repeat=repeat, scenario=name,
                         id=f"{name}-r{repeat}")
            trials.append(value)
    return {"schema_version": 1, "seed": seed, "purpose": "equal-budget latency",
            "requested_profile": "baseline", "actual_profile": "unverified",
            "resource_budget": {"status": "operator must record worker/actor quotas, CPU sets and reservation cost"},
            "trials": trials}


def make_selftest_plan(requests, repeats, seed):
    plan = make_plan(requests, repeats, seed)
    plan["purpose"] = "synthetic harness validation only"
    for trial in plan["trials"]:
        trial.update(rate=0 if "storm" in trial["scenario"] else 1000,
                     concurrency=2, queue_capacity=2, hold_s=.01)
    for repeat in range(repeats):
        config = trial_defaults()
        config.update(id=f"injected-timeout-r{repeat}", scenario="injected-timeout", repeat=repeat,
                      requests=requests, rate=1000, timeout_s=.001, concurrency=1, queue_capacity=2)
        plan["trials"].append(config)
    random.Random(seed + 1).shuffle(plan["trials"])
    return plan


def validate(plan):
    if not isinstance(plan, dict) or plan.get("schema_version") != 1 or not isinstance(plan.get("trials"), list) or not plan["trials"]:
        raise ValueError("nonempty schema_version=1 plan required")
    if len(plan["trials"]) > 1000:
        raise ValueError("at most 1000 trials per finite invocation")
    ids = set()
    for trial in plan["trials"]:
        if not isinstance(trial, dict) or not isinstance(trial.get("scenario"), str) or not trial["scenario"]:
            raise ValueError("each trial requires a nonempty scenario string")
        if not isinstance(trial.get("id"), str) or not trial["id"] or trial["id"] in ids:
            raise ValueError("trial IDs must be unique strings")
        ids.add(trial["id"])
        if trial.get("kind") not in KINDS or trial.get("workload") not in WORKLOADS:
            raise ValueError("unknown lifecycle/workload")
        if trial.get("source") not in ("golden", "per_actor"):
            raise ValueError("snapshot source must be golden or per_actor")
        for name, lower, upper in (("requests", 1, 100000), ("concurrency", 1, 128),
                                   ("queue_capacity", 0, 1000), ("competitors", 0, 64),
                                   ("competitor_cores", 1, 128), ("memory_bytes", 4096, 1024**3),
                                   ("io_bytes", 1, 16 * 1024**2), ("cpu_ms", 1, 60000),
                                   ("repeat", 0, 10000), ("abort_after_errors", 0, 1000)):
            value = trial.get(name)
            if type(value) is not int or not lower <= value <= upper:
                raise ValueError(f"{name} must be an integer in [{lower}, {upper}]")
        for name, lower, upper in (("rate", 0, 100000), ("timeout_s", 0.001, 3600),
                                   ("setup_timeout_s", 0.001, 3600), ("cleanup_timeout_s", 0.001, 300),
                                   ("hold_s", 0, 300), ("competitor_duty", 0, 1)):
            value = trial.get(name)
            if type(value) not in (int, float) or not math.isfinite(value) or not lower <= value <= upper:
                raise ValueError(f"{name} outside [{lower}, {upper}]")
        if trial["kind"] != "create_destroy" and trial["requests"] > 1000:
            raise ValueError("precreated fixtures are limited to 1000 actors; use repeated trials")
        if trial["rate"] and (trial["requests"] - 1) / trial["rate"] > 3600:
            raise ValueError("arrival window exceeds one hour")
        if trial["source"] == "golden" and (trial["workload"] in ("sparse", "dense") or trial["kind"] in ("pause_resume", "warm_exec")):
            raise ValueError("golden memory/pause/warm fixture is unsupported; use per_actor")
        if trial["kind"] == "create_destroy" and (trial["source"] != "golden" or trial["workload"] in ("sparse", "dense")):
            raise ValueError("create_destroy measures new golden activation without preinitialization")
        for cache in ("snapshot_cache", "file_cache"):
            if trial.get(cache) not in ("uncontrolled", "operator_cold", "operator_warm"):
                raise ValueError(f"invalid {cache}")
            if trial[cache] != "uncontrolled" and not trial.get("cache_evidence"):
                raise ValueError("controlled cache label requires preparation evidence in trial")


class Sink:
    def __init__(self, path):
        self.file = Path(path).open("w")
        self.lock = threading.Lock()

    def __call__(self, row):
        with self.lock:
            self.file.write(json.dumps(row, sort_keys=True) + "\n")
            self.file.flush()

    def close(self):
        self.file.close()


def snapshot_provenance(actor, source, kind):
    """Record the latest usable API snapshot, not proof of actual VMM restore."""
    status = actor.get("status", {})
    paused = kind == "pause_resume"
    state = "ACTOR_STATE_PAUSED" if paused else "ACTOR_STATE_SUSPENDED"
    if status.get("state") != state:
        raise ValueError(f"snapshot fixture must be {state}")
    usable = []
    for snapshot in status.get("snapshots", []):
        durable = snapshot.get("durableSnapshot", {})
        if (bool(snapshot.get("locality")) if paused else durable.get("status") == "SNAPSHOT_STORAGE_STATUS_COMPLETED"):
            generation = snapshot.get("generation")
            if type(generation) is not int or generation < 1:
                raise ValueError("snapshot generation must be a positive integer")
            usable.append(snapshot)
    if not usable:
        raise ValueError("no usable memory snapshot in Actor response")
    latest = max(s.get("generation") for s in usable)
    chosen = [s for s in usable if s["generation"] == latest]
    if len(chosen) != 1:
        raise ValueError("ambiguous latest usable snapshot generation")
    snap = chosen[0]
    owner = "SNAPSHOT_OWNER_TAG" if source == "golden" else "SNAPSHOT_OWNER_ACTOR"
    if snap.get("owner") != owner or snap.get("fidelity") != "SNAPSHOT_FIDELITY_MEMORY":
        raise ValueError("latest usable snapshot has wrong ownership or is not MEMORY fidelity")
    values = {"actor_uid": actor.get("metadata", {}).get("uid"), "uuid": snap.get("uuid"),
              "template_uid": snap.get("actorTemplateUid"),
              "uri": snap.get("durableSnapshot", {}).get("object", {}).get("snapshotUri", "")}
    for name in ("actor_uid", "uuid", "template_uid") + (() if paused else ("uri",)):
        if not isinstance(values[name], str) or not values[name].strip():
            raise ValueError(f"snapshot provenance requires {name}")
    return {**values, "generation": snap["generation"], "owner": owner,
            "fidelity": snap["fidelity"], "locality": snap.get("locality", ""),
            "api_state": state, "source": source, "synthetic": False,
            "evidence_scope": "API fixture provenance; actual restore mode requires server evidence"}


class Trial:
    def __init__(self, adapter, config, prefix, emit, stop):
        self.adapter, self.config, self.prefix, self.emit, self.stop = adapter, config, prefix, emit, stop
        self.owned, self.expected = set(), {}
        self.lock = threading.Lock()
        self.rows = []
        self.errors = 0
        self.cleanup_failed = False
        self.recording_error = None
        self.worker_error = None
        self.snapshot_context = None
        self.provenance = {}
        self.snapshot_owners, self.uri_owners = {}, {}

    def record(self, row):
        row["trial_id"] = self.config["id"]
        with self.lock:
            if row["type"] == "request":
                self.rows.append(row)
                if row["status"] not in ("ok", "aborted"):
                    self.errors += 1
                    if self.config["abort_after_errors"] and self.errors >= self.config["abort_after_errors"]:
                        self.stop.set()
            broken = self.recording_error is not None
        if broken:
            return
        try:
            if self.emit(row) is False:
                raise OSError("raw recording unavailable")
        except Exception as exc:
            with self.lock:
                self.recording_error = self.recording_error or f"{type(exc).__name__}: {exc}"
            self.stop.set()

    def name(self, index):
        return f"{self.prefix}-{index}"

    def capture_snapshot(self, actor, result):
        source = self.config["source"]
        if getattr(self.adapter, "synthetic", False):
            provenance = {"synthetic": True, "source": source, "template_uid": "synthetic-template",
                          "fidelity": "synthetic", "actor_uid": actor,
                          "uuid": "synthetic-golden" if source == "golden" else actor,
                          "uri": "synthetic://golden" if source == "golden" else "synthetic://" + actor,
                          "generation": 1, "owner": source, "locality": "synthetic"}
        else:
            provenance = snapshot_provenance(result, source, self.config["kind"])
        context = {key: provenance[key] for key in ("synthetic", "source", "template_uid", "fidelity")}
        if source == "golden":
            context.update(uuid=provenance["uuid"], uri=provenance["uri"])
        with self.lock:
            if self.snapshot_context is not None and self.snapshot_context != context:
                self.stop.set()
                raise ValueError("snapshot context changed within the trial")
            if source == "per_actor":
                for key, owners in (("uuid", self.snapshot_owners), ("uri", self.uri_owners)):
                    value = provenance[key]
                    if value and value in owners and owners[value] != actor:
                        self.stop.set()
                        raise ValueError(f"distinct Actor snapshots reused the same {key}")
                    if value:
                        owners[value] = actor
            self.snapshot_context = context
            self.provenance[actor] = provenance
        self.record({"type": "snapshot_source", "actor": actor, "provenance": provenance})

    def create(self, actor, deadline, measured=True):
        if self.stop.is_set():
            raise InterruptedError("stopped before Actor creation")
        remaining(deadline)
        # Ambiguous outcomes remain ours to clean; an explicit collision does not.
        with self.lock:
            self.owned.add(actor)
        try:
            result = self.adapter.lifecycle("create", actor, deadline)
        except ActorAlreadyExists:
            with self.lock:
                self.owned.discard(actor)
            self.stop.set()
            raise
        if measured and self.config["source"] == "golden":
            self.capture_snapshot(actor, result)
        return result

    def cleanup(self, actor):
        try:
            self.adapter.lifecycle("delete", actor, time.monotonic() + self.config["cleanup_timeout_s"])
        except Exception as exc:
            self.cleanup_failed = True
            self.stop.set()  # Do not accumulate live Actors after deletion failures.
            self.record({"type": "cleanup", "actor": actor, "status": "error", "error": str(exc)})
        else:
            with self.lock:
                self.owned.discard(actor)
            self.record({"type": "cleanup", "actor": actor, "status": "delete_acknowledged"})

    def prepare(self):
        trial = self.config
        for index in range(trial["competitors"]):
            if self.stop.is_set():
                raise InterruptedError("stopped during preparation")
            actor = self.name(f"load-{index}")
            deadline = time.monotonic() + trial["setup_timeout_s"]
            self.create(actor, deadline, measured=False)
            self.adapter.lifecycle("resume", actor, deadline)
            self.adapter.load(actor, trial["competitor_cores"], trial["competitor_duty"], deadline)
        if trial["kind"] == "create_destroy":
            return
        for index in range(trial["requests"]):
            if self.stop.is_set():
                raise InterruptedError("stopped during preparation")
            actor = self.name(index)
            deadline = time.monotonic() + trial["setup_timeout_s"]
            self.create(actor, deadline)
            if trial["source"] == "per_actor":
                self.adapter.lifecycle("resume", actor, deadline)
                self.expected[actor] = self.adapter.initialize(actor, trial, deadline)
                if trial["kind"] != "warm_exec":
                    operation = "pause" if trial["kind"] == "pause_resume" else "suspend"
                    result = self.adapter.lifecycle(operation, actor, deadline)
                    self.capture_snapshot(actor, result)
            self.record({"type": "setup", "actor": actor, "status": "ready", "source": trial["source"]})

    def request(self, index, scheduled, admitted, origin):
        trial = self.config
        actor = self.name(index)
        started = time.monotonic()
        deadline = scheduled + trial["timeout_s"]
        row = {"type": "request", "index": index, "actor": actor, "status": "error",
               "scheduled_offset_s": scheduled - origin, "admitted_offset_s": admitted - origin,
               "started_offset_s": started - origin, "queue_wait_s": started - admitted,
               "generator_lag_s": admitted - scheduled, "phases_s": {},
               "source": trial["source"], "lifecycle": trial["kind"], "workload": trial["workload"],
               "vmm_restore_api_s": None, "guest_resume_s": None, "prewarmed_claim_s": None}
        phase = "queue"
        try:
            if self.stop.is_set():
                row["status"] = "aborted"
                return
            remaining(deadline)
            if trial["kind"] == "create_destroy":
                phase = "create_api"
                before = time.monotonic()
                self.create(actor, deadline)
                row["phases_s"][phase] = time.monotonic() - before
            if self.stop.is_set():
                raise InterruptedError("stopped before resume")
            if trial["kind"] != "warm_exec":
                phase = "resume_api"
                before = time.monotonic()
                self.adapter.lifecycle("resume", actor, deadline)
                row["phases_s"][phase] = time.monotonic() - before
            if self.stop.is_set():
                raise InterruptedError("stopped before guest command")
            phase = "guest_command"
            row["snapshot_provenance"] = self.provenance.get(actor)
            before = time.monotonic()
            row["guest_request_offset_s"] = before - origin
            row["verification"] = self.adapter.execute(actor, f"{self.prefix}-{index}", trial,
                                                        self.expected.get(actor, {}), deadline)
            row["phases_s"][phase] = time.monotonic() - before
            completed = time.monotonic()
            remaining(deadline)
            row.update(status="ok", first_exec_s=completed - scheduled,
                       service_s=completed - started, completion_offset_s=completed - origin)
        except InterruptedError as exc:
            row.update(status="aborted", error=str(exc), failed_phase=phase)
        except RequestRejected as exc:
            row.update(status="rejected", error=str(exc), failed_phase=phase)
        except (DeadlineExceeded, TimeoutError) as exc:
            row.update(status="timeout", error=str(exc), failed_phase=phase)
        except Exception as exc:
            row.update(status="error", error=str(exc), failed_phase=phase)
        finally:
            row["terminal_offset_s"] = time.monotonic() - origin
            row["elapsed_from_arrival_s"] = time.monotonic() - scheduled
            self.record(row)
            if trial["kind"] == "create_destroy":
                with self.lock:
                    owned = actor in self.owned
                if owned:
                    self.cleanup(actor)

    def run(self):
        origin, result = None, {}
        try:
            self.prepare()
            trial = self.config
            # Admission cap covers both executing and queued requests. The dispatcher
            # never waits for a completion before deciding the next arrival.
            pending = queue.Queue()
            capacity = threading.BoundedSemaphore(trial["concurrency"] + trial["queue_capacity"])
            origin = time.monotonic()

            def worker():
                while True:
                    item = pending.get()
                    if item is None:
                        pending.task_done()
                        return
                    try:
                        self.request(*item, origin)
                    except BaseException as exc:
                        self.worker_error = f"{type(exc).__name__}: {exc}"
                        self.stop.set()
                        self.record({"type": "worker_error", "error": self.worker_error})
                    finally:
                        capacity.release()
                        pending.task_done()

            workers = [threading.Thread(target=worker) for _ in range(trial["concurrency"])]
            for thread in workers:
                thread.start()
            try:
                for index in range(trial["requests"]):
                    scheduled = origin + (index / trial["rate"] if trial["rate"] else 0)
                    self.stop.wait(max(0, scheduled - time.monotonic()))
                    admitted = time.monotonic()
                    status = "aborted" if self.stop.is_set() else None
                    if status is None and not capacity.acquire(blocking=False):
                        status = "dropped"
                    if status:
                        self.record({"type": "request", "index": index, "actor": self.name(index),
                                     "status": status, "scheduled_offset_s": scheduled - origin,
                                     "admitted_offset_s": admitted - origin,
                                     "generator_lag_s": max(0, admitted - scheduled),
                                     "aborted_before_arrival": status == "aborted" and admitted < scheduled,
                                     "reason": "stop requested" if status == "aborted" else "outstanding capacity exhausted"})
                    else:
                        pending.put((index, scheduled, admitted))
            finally:
                for _ in workers:
                    pending.put(None)
                pending.join()
                for thread in workers:
                    thread.join()
            end = time.monotonic()
            if trial["kind"] == "memory_density":
                self.record({"type": "density_hold", "successful_resident_actors": sum(r["status"] == "ok" for r in self.rows),
                             "hold_s": trial["hold_s"], "note": "actor count; physical density requires target-host PSS/private/aggregate observations"})
                self.stop.wait(trial["hold_s"])
            result = summarize(self.rows, max(end - origin, 1e-9), trial, self.snapshot_context)
        except Exception as exc:
            self.record({"type": "trial_error", "error": str(exc), "stage": "setup" if origin is None else "run"})
            self.stop.set()
            result = {"trial_id": self.config["id"], "scenario": self.config["scenario"],
                      "repeat": self.config["repeat"], "status": "setup_or_run_failed", "error": str(exc),
                      "offered": len(self.rows), "planned": self.config["requests"]}
        finally:
            # Bounded cleanup concurrency; failed names remain in the raw recovery list.
            for actor in sorted(self.owned):
                self.cleanup(actor)
            self.record({"type": "cleanup_inventory", "remaining_actors": sorted(self.owned),
                         "note": "DeleteActor acknowledgement is not proof host processes have exited"})
        result.update(cleanup_failed=self.cleanup_failed, remaining_actors=sorted(self.owned))
        if self.recording_error:
            result.update(status="recording_failed", recording_error=self.recording_error)
        elif self.worker_error:
            result.update(status="worker_failed", error=self.worker_error)
        return result


def percentile(values, fraction):
    values = sorted(values)
    return values[max(0, math.ceil(len(values) * fraction) - 1)] if values else None


def tails(values):
    return {"n": len(values), "p50_s": percentile(values, .5), "p95_s": percentile(values, .95),
            "p99_s": percentile(values, .99),
            "p999_s": None,
            "p999_note": "withheld per trial; only the repeated-run sample/stability gate may publish P99.9"}


def summarize(rows, elapsed, config, snapshot_context=None):
    counts = dict(collections.Counter(row["status"] for row in rows))
    successful = [row for row in rows if row["status"] == "ok"]
    canonical = {k: v for k, v in config.items() if k not in ("id", "repeat")}
    canonical["snapshot_context"] = snapshot_context
    result = {"trial_id": config["id"], "scenario": config["scenario"], "repeat": config["repeat"],
              "snapshot_context": snapshot_context,
              "scenario_config_sha256": hashlib.sha256(json.dumps(canonical, sort_keys=True).encode()).hexdigest(),
              "_p999_candidate_s": percentile([row["first_exec_s"] for row in successful], .999) if len(successful) >= 10000 else None,
              "status": "finished", "planned": config["requests"], "offered": sum(not r.get("aborted_before_arrival", False) for r in rows),
              "admitted": sum("started_offset_s" in r for r in rows),
              "counts": counts, "elapsed_s": elapsed, "throughput_success_rps": len(successful) / elapsed,
              "first_exec": tails([row["first_exec_s"] for row in successful]),
              "queue_wait": tails([row["queue_wait_s"] for row in rows if "queue_wait_s" in row]),
              "failure_fraction": 1 - len(successful) / len(rows) if rows else None,
              "latency_population": "successful requests only; failures, timeouts and drops are separately counted",
              "success_by_deadline_fraction": len(successful) / config["requests"]}
    return result


def repeat_summary(summaries):
    grouped = collections.defaultdict(list)
    for summary in summaries:
        if summary.get("status") == "finished" and not summary.get("cleanup_failed") and not summary.get("recording_error"):
            grouped[summary["scenario"]].append(summary)
    result = {}
    for scenario, rows in grouped.items():
        result[scenario] = {"repeats": len(rows), "per_repeat_success_n": [r["first_exec"]["n"] for r in rows]}
        fingerprints = {r.get("scenario_config_sha256") for r in rows}
        comparable = (len(rows) >= 3 and len({r.get("repeat") for r in rows}) == len(rows)
                      and len(fingerprints) == 1 and all(isinstance(f, str) and f for f in fingerprints))
        result[scenario]["comparable_repeats"] = comparable
        for key in ("p50_s", "p95_s", "p99_s"):
            values = [r["first_exec"][key] for r in rows if r["first_exec"][key] is not None]
            result[scenario][key + "_range"] = [min(values), max(values)] if comparable and len(values) >= 3 else None
        candidates = [r.get("_p999_candidate_s") for r in rows]
        gate = "withheld: need at least three distinct repeats, each with 10000 successful samples"
        publish = False
        if len(fingerprints) != 1 or not all(isinstance(f, str) and f for f in fingerprints):
            gate = "withheld: scenario fingerprints differ or are missing across repeats"
        elif (comparable
              and all(r["first_exec"]["n"] >= 10000 for r in rows)
              and all(v is not None and v > 0 for v in candidates)):
            if (max(candidates) - min(candidates)) / percentile(candidates, .5) > .10:
                gate = "withheld: repeat P99.9 spread exceeds the predeclared 10% of median gate"
            else:
                publish = True
                gate = "passed descriptive 10% relative repeat-spread gate; not a confidence guarantee"
        result[scenario]["p999_s_range"] = [min(candidates), max(candidates)] if publish else None
        result[scenario]["p999_repeat_estimates_s"] = candidates if publish else None
        result[scenario]["p999_gate"] = gate
        result[scenario]["note"] = "min/max descriptive repeat variation, not a confidence interval; at least three repeats required"
    return result


def public_summary(summary):
    return {key: value for key, value in summary.items() if not key.startswith("_")}


def run(args):
    plan = json.loads(Path(args.plan).read_text())
    validate(plan)
    if args.adapter == "substrate" and not (args.allow_create_actors and args.atespace and args.template and args.router_url):
        raise ValueError("substrate requires --allow-create-actors, --atespace, --template and --router-url")
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=False)
    (out / "logs").mkdir()
    manifest = json.loads(Path(args.manifest).read_text()) if args.manifest else {"status": "missing"}
    env = observe.environment(args.target_host_local, manifest)
    env.update(adapter=args.adapter, synthetic=args.adapter == "selftest", collected_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
    (out / "environment.json").write_text(json.dumps(env, indent=2) + "\n")
    (out / "plan.json").write_text(json.dumps(plan, indent=2) + "\n")
    (out / "commands.txt").write_text(shlex.join([sys.executable] + sys.argv) + "\n")
    raw, log, observations = Sink(out / "raw-results.jsonl"), Sink(out / "logs" / "adapter.jsonl"), Sink(out / "observations.jsonl")
    stop, sampling_done = threading.Event(), threading.Event()
    integrity_errors, error_lock = {}, threading.Lock()

    def fail(key, exc):
        with error_lock:
            integrity_errors.setdefault(key, f"{type(exc).__name__}: {exc}")
        stop.set()

    def guarded(sink, key):
        def write(row):
            with error_lock:
                if key in integrity_errors:
                    return False
            try:
                sink(row)
                return True
            except Exception as exc:
                fail(key, exc)
                return False
        return write

    sinks = (raw, log, observations)
    raw = guarded(raw, "recording_error")
    log = guarded(log, "adapter_log_error")
    observations = guarded(observations, "observation_error")
    try:
        adapter = SelftestAdapter(args.selftest_delay, args.selftest_fail_every) if args.adapter == "selftest" else SubstrateAdapter(
            shlex.split(args.cli), args.atespace, args.template, args.router_url, log,
            args.ca, args.client_cert, args.client_key)
    except Exception:
        for sink in sinks:
            sink.close()
        raise
    old_handlers = {}
    for signum in (signal.SIGINT, signal.SIGTERM):
        old_handlers[signum] = signal.signal(signum, lambda signum, frame: stop.set())
    scope = "target_host" if args.target_host_local else "load_generator_only"

    def sampler():
        try:
            while True:
                if not observations(observe.sample(args.pid, args.cgroup, scope)):
                    return
                if sampling_done.wait(args.sample_interval):
                    observations(observe.sample(args.pid, args.cgroup, scope))
                    return
        except Exception as exc:
            fail("observation_error", exc)

    thread = threading.Thread(target=sampler)
    thread.start()
    summaries = []
    failed = False
    prefix = "rtb-" + uuid.uuid4().hex[:12]
    try:
        for index, config in enumerate(plan["trials"]):
            if stop.is_set():
                raw({"type": "trial_not_run", "trial_id": config["id"], "reason": "previous failure or stop"})
                continue
            raw({"type": "trial_start", "trial_id": config["id"], "config": config,
                 "requested_profile": plan.get("requested_profile"), "actual_profile": plan.get("actual_profile", "unverified")})
            trial = Trial(adapter, config, f"{prefix}-{index}", raw, stop)
            summary = trial.run()
            summary["cleanup_failed"] = trial.cleanup_failed
            summaries.append(summary)
            raw({"type": "trial_summary", **public_summary(summary)})
            failed |= summary["status"] != "finished" or trial.cleanup_failed or summary.get("failure_fraction", 0) > 0
            if trial.cleanup_failed:
                stop.set()
    finally:
        sampling_done.set()
        thread.join()
        for signum, handler in old_handlers.items():
            signal.signal(signum, handler)
        for sink, key in zip(sinks, ("recording_error", "adapter_log_error", "observation_error")):
            try:
                sink.close()
            except Exception as exc:
                fail(key, exc)
    report = {"synthetic": args.adapter == "selftest", "summaries": [public_summary(s) for s in summaries],
              "repeat_variation": repeat_summary(summaries) if not integrity_errors else {},
              "stopped": stop.is_set(), **integrity_errors}
    (out / "summary.json").write_text(json.dumps(report, indent=2) + "\n")
    lines = ["# Runtime benchmark", "", "SYNTHETIC SELF-TEST ONLY; no microVM performance measurement." if report["synthetic"] else "CLI plus guest-command end-to-end measurements; inspect environment and actual-mode evidence.", "",
             "Latency starts at scheduled arrival, including client queue and dispatch lag. Successful samples only; all terminal outcomes are counted.",
             f"Run failed or stopped: {failed or stop.is_set()}. Integrity errors: {json.dumps(integrity_errors, sort_keys=True)}.", ""]
    for summary in summaries:
        lines.append(f"- {summary['trial_id']}: {json.dumps(public_summary(summary), sort_keys=True)}")
    lines += ["", "VMM restore API, Guest-resume and prewarmed-object-claim durations are unavailable in client records. Use existing server phase logs without mixing clock domains.",
              "P99.9 is withheld unless at least three distinct repeats each have 10,000 successes and their tail spread passes the predeclared 10% gate. No density claim is possible from driver RSS or actor count."]
    (out / "summary.md").write_text("\n".join(lines) + "\n")
    print(json.dumps({"out": str(out), "synthetic": report["synthetic"], "failed_requests_or_trials": failed or bool(integrity_errors), **integrity_errors}))
    return 1 if failed or stop.is_set() else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subs = parser.add_subparsers(dest="command", required=True)
    plan_parser = subs.add_parser("plan", help="write randomized finite scenario plan without running it")
    plan_parser.add_argument("--out", required=True)
    plan_parser.add_argument("--requests", type=int, default=20)
    plan_parser.add_argument("--repeats", type=int, default=3)
    plan_parser.add_argument("--seed", type=int, default=20261013)
    plan_parser.add_argument("--selftest", action="store_true", help="small synthetic overload/timeout fixture")
    run_parser = subs.add_parser("run")
    run_parser.add_argument("--plan", required=True)
    run_parser.add_argument("--out", required=True)
    run_parser.add_argument("--adapter", choices=("selftest", "substrate"), required=True)
    run_parser.add_argument("--allow-create-actors", action="store_true")
    run_parser.add_argument("--cli", default="kubectl-ate")
    run_parser.add_argument("--atespace")
    run_parser.add_argument("--template")
    run_parser.add_argument("--router-url")
    run_parser.add_argument("--ca")
    run_parser.add_argument("--client-cert")
    run_parser.add_argument("--client-key")
    run_parser.add_argument("--manifest", help="JSON environment/budget/actual-mode evidence; no secrets")
    run_parser.add_argument("--target-host-local", action="store_true")
    run_parser.add_argument("--pid", type=int, action="append", default=[])
    run_parser.add_argument("--cgroup", action="append", default=[])
    run_parser.add_argument("--sample-interval", type=float, default=1.0)
    run_parser.add_argument("--selftest-delay", type=float, default=.002)
    run_parser.add_argument("--selftest-fail-every", type=int, default=0)
    args = parser.parse_args()
    if args.command == "plan":
        plan = (make_selftest_plan if args.selftest else make_plan)(args.requests, args.repeats, args.seed)
        validate(plan)
        with Path(args.out).open("x") as file:
            file.write(json.dumps(plan, indent=2) + "\n")
        return 0
    if not 0.05 <= args.sample_interval <= 60 or args.selftest_delay < 0 or args.selftest_fail_every < 0:
        parser.error("invalid sampling interval or self-test injection")
    try:
        return run(args)
    except (OSError, ValueError) as exc:
        parser.error(str(exc))


if __name__ == "__main__":
    sys.exit(main())
