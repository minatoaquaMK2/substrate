#!/usr/bin/env python3
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
"""No-KVM failure tests using fake I/O and native guest corruption fixtures."""
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("cow_check", Path(__file__).with_name("check.py"))
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)

class StreamGuard(Exception):
    pass

class Console:
    def __init__(self, clock, advance, limit):
        self.clock, self.advance, self.limit, self.reads = clock, advance, limit, 0
    def settimeout(self, value):
        self.timeout = value
    def recv(self, count):
        self.reads += 1
        if self.reads > self.limit:
            raise StreamGuard("harness failed to stop continuous output")
        self.clock[0] += self.advance
        return b"x" * min(count, 65536)

class HandoffTests(unittest.TestCase):
    def stream_vm(self, clock, advance, limit):
        vm = check.VM.__new__(check.VM)
        vm.console = Console(clock, advance, limit)
        vm.buffer = b""
        vm.serial_log = io.BytesIO()
        vm.serial_received = 0
        return vm

    def test_continuous_output_has_absolute_deadline(self):
        clock = [0.0]
        vm = self.stream_vm(clock, 15, 4)
        with mock.patch.object(check.time, "monotonic", side_effect=lambda: clock[0]):
            with self.assertRaises(TimeoutError):
                vm.expect("missing-marker")
        self.assertLessEqual(vm.console.reads, 3)

    def test_continuous_output_has_memory_and_log_cap(self):
        vm = self.stream_vm([0.0], 0, 32)
        with mock.patch.object(check.time, "monotonic", return_value=0):
            with self.assertRaisesRegex(RuntimeError, "serial output.*limit"):
                vm.expect("missing-marker")
        self.assertLessEqual(len(vm.buffer), 1 << 20)
        self.assertLessEqual(len(vm.serial_log.getvalue()), 1 << 20)

    def test_guest_error_is_immediate_and_preserved(self):
        vm = self.stream_vm([0.0], 0, 1)
        vm.buffer = b"COW-ERROR:offset=8388608 actual=dc expected=dd\n"
        with self.assertRaisesRegex(RuntimeError, "offset=8388608"):
            vm.expect("COW:dd:dd:verified=16777216")
        self.assertEqual(vm.console.reads, 0)

    def test_http_success_after_absolute_deadline_is_rejected(self):
        # Uses the original method signature so this is a behavioral red on
        # the first-delivery harness, rather than merely a new timeout option.
        clock = [0.0]
        class FakeHTTP:
            status, length = 200, 0
            def __init__(self, *args, **kwargs): self.sock = self
            def connect(self): pass
            def settimeout(self, value): pass
            def request(self, *args): pass
            def getresponse(self): return self
            def read(self, count=None):
                clock[0] = 46.0
                return b"{}"
            def close(self): pass
        vm = check.VM.__new__(check.VM); vm.api = "unused"
        with mock.patch.object(check, "UnixHTTP", FakeHTTP), \
             mock.patch.object(check.time, "monotonic", side_effect=lambda: clock[0]), \
             mock.patch("threading.Timer"):
            with self.assertRaises(TimeoutError):
                vm.request("vmm.ping")

    def test_http_response_deadline_interrupts_a_closing_response(self):
        interrupted = threading.Event()
        closed = []
        class FakeHTTP:
            def __init__(self, *args, **kwargs): self.sock = self
            def connect(self): pass
            def settimeout(self, value): pass
            def shutdown(self, how): interrupted.set()
            def request(self, *args): pass
            def getresponse(self):
                # HTTPConnection clears this handle for Connection: close;
                # HTTPResponse's file still owns the pending body read.
                self.sock = None
                return self
            def read(self, count):
                interrupted.wait(.5)
                return b"{}"
            def close(self): closed.append(True)
        vm = check.VM.__new__(check.VM); vm.api = "unused"
        start = time.monotonic()
        with mock.patch.object(check, "UnixHTTP", FakeHTTP):
            with self.assertRaises(TimeoutError):
                vm.request("vmm.ping", timeout=.03)
        self.assertTrue(interrupted.is_set())
        self.assertLess(time.monotonic() - start, .4)
        self.assertEqual(closed, [True])

    def test_close_attempts_wait_and_closes_logs_after_kill_failure(self):
        vm = check.VM.__new__(check.VM)
        vm.directory = "fixture-vm"; vm.console = io.BytesIO()
        vm.log = io.BytesIO(); vm.serial_log = io.BytesIO()
        vm.process = mock.Mock(pid=123, **{"poll.return_value": None,
            "kill.side_effect": OSError("kill failure"), "wait.side_effect": TimeoutError("wait failure")})
        with self.assertRaisesRegex(RuntimeError, "kill pid 123.*wait pid 123"):
            vm.close()
        vm.process.wait.assert_called_once_with(timeout=10)
        self.assertTrue(vm.log.closed and vm.serial_log.closed and vm.console.closed)

    def test_clone_preserves_restore_and_cleanup_errors(self):
        with tempfile.TemporaryDirectory(prefix="cow-clone-") as tmp:
            base = Path(tmp); snapshot = base / "snapshot"; snapshot.mkdir()
            views = base / "views"; views.mkdir()
            for name, data in (("config.json", b"{}"), ("state.json", b"{}"), ("memory-ranges", b"memory")):
                (snapshot / name).write_bytes(data)
            vm = mock.Mock(**{"request.side_effect": RuntimeError("restore rejected"),
                              "close.side_effect": RuntimeError("cleanup wait failed")})
            with mock.patch.object(check, "VM", return_value=vm):
                with self.assertRaisesRegex(RuntimeError, "restore rejected.*cleanup wait failed"):
                    check.clone("unused", snapshot, base / "clone", views)

    def test_cleanup_attempts_all_vms_and_saves_failure_evidence(self):
        closed = []
        class FakeVM:
            def __init__(self, binary, directory):
                self.directory = directory; directory.mkdir()
                self.serial = str(directory / "serial.sock")
            def request(self, *args, **kwargs): pass
            def connect_serial(self): pass
            def expect(self, *args): pass
            def command(self, *args):
                if self.directory.name == "a": raise RuntimeError("injected command failure")
            def snapshot(self, directory):
                directory.mkdir(); (directory / "memory-ranges").write_bytes(b"golden")
            def close(self):
                closed.append(self.directory.name)
                if self.directory.name == "b": raise TimeoutError("injected VMM wait failure")
        real_open = os.open
        with tempfile.TemporaryDirectory(prefix="cow-run-") as tmp:
            base = Path(tmp); binary = base / "binary"; binary.touch()
            kernel = base / "kernel"; kernel.touch(); out = base / "result"
            argv = ["check.py", "--binary", str(binary), "--kernel", str(kernel), "--output", str(out), "--required"]
            with mock.patch.object(check.sys, "argv", argv), \
                 mock.patch.object(check.platform, "machine", return_value="x86_64"), \
                 mock.patch.object(check.os, "open", side_effect=lambda path, flags: real_open(os.devnull if path == "/dev/kvm" else path, flags)), \
                 mock.patch.object(check.shutil, "which", return_value="cc"), \
                 mock.patch.object(check.subprocess, "run"), mock.patch.object(check, "initramfs"), \
                 mock.patch.object(check, "VM", FakeVM), \
                 mock.patch.object(check, "clone", side_effect=lambda binary, snapshot, directory, views: FakeVM(binary, directory)), \
                 mock.patch("sys.stdout", new_callable=io.StringIO):
                try: status = check.main()
                except (RuntimeError, TimeoutError): status = None
            self.assertIn("a", closed, "cleanup failure skipped another VMM")
            report = json.loads((out / "result.json").read_text())
            self.assertEqual(status, 1)
            self.assertEqual(report["status"], "failed")
            self.assertIn("injected command failure", report["error"])
            self.assertIn("injected VMM wait", str(report["cleanup_errors"]))

    def test_guest_rejects_corrupted_middle_after_sparse_and_dense_writes(self):
        guest = Path(__file__).with_name("guest.c").resolve()
        fixture = r'''
#include <unistd.h>
#include <stdlib.h>
static ssize_t fixture_read(int fd, void *buffer, size_t count);
#define read fixture_read
#define main guest_main
#include "GUEST_SOURCE"
#undef read
#undef main
static char selected;
static int corrupt, calls;
static ssize_t fixture_read(int fd, void *buffer, size_t count) {
    (void)fd; (void)count;
    if (calls++ == 0) { *(char *)buffer = selected; return 1; }
    if (calls == 2) {
        if (corrupt) state[sizeof(state) / 2] ^= 1;
        *(char *)buffer = 'R'; return 1;
    }
    exit(0);
}
int main(int argc, char **argv) {
    selected = argc >= 2 ? argv[1][0] : 'D'; corrupt = argc != 3;
    return guest_main();
}
'''.replace("GUEST_SOURCE", str(guest))
        with tempfile.TemporaryDirectory(prefix="cow-guest-") as tmp:
            path = Path(tmp); source = path / "fixture.c"; source.write_text(fixture)
            binary = path / "fixture"
            subprocess.run(["cc", "-O2", "-Wall", "-Wextra", "-Werror", str(source), "-o", str(binary)], check=True, timeout=30)
            for mode in ("A", "D"):
                with self.subTest(corrupt_mode=mode):
                    result = subprocess.run([str(binary), mode], stdin=subprocess.DEVNULL, capture_output=True, check=True, timeout=10)
                    self.assertIn("COW-ERROR:offset=8388608", result.stdout.decode().splitlines()[-1])
            for mode, expected in (("R", "31:00"), ("A", "a1:a2"), ("B", "b1:b2"), ("D", "dd:dd")):
                with self.subTest(clean_mode=mode):
                    result = subprocess.run([str(binary), mode, "clean"], stdin=subprocess.DEVNULL, capture_output=True, check=True, timeout=10)
                    self.assertEqual(result.stdout.decode().splitlines()[1:], ["COW:" + expected + ":verified=16777216"] * 2)

if __name__ == "__main__":
    unittest.main()
