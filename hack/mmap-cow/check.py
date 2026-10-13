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
"""Bounded real CH API/KVM CoW clone and recheckpoint test, x86_64 only.

Needs a Linux ELF/bzImage kernel with initramfs and ttyS0 support. Builds a tiny
static PID 1 locally; no guest image, network, credentials or services needed.
Missing hardware skips locally and fails with --required or CI=true.
"""
import argparse
import hashlib
import http.client
import json
import os
import pathlib
import platform
import shutil
import socket
import stat
import subprocess
import sys
import time
import threading

CAPABILITY = "restore-cow-strict-v1"
SERIAL_LIMIT = 1 << 20


def remaining(deadline):
    value = deadline - time.monotonic()
    if value <= 0:
        raise TimeoutError("absolute VMM operation deadline expired")
    return value


def required(args):
    return args.required or os.environ.get("CI", "").lower() in ("1", "true", "yes")


def digest(path):
    with open(path, "rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def initramfs(executable, destination):
    image = bytearray()
    def add(name, mode, data=b"", major=0, minor=0):
        encoded = name.encode() + b"\0"
        values = [1, mode, 0, 0, 1, 0, len(data), 0, 0, major, minor, len(encoded), 0]
        image.extend(b"070701" + b"".join(f"{value:08x}".encode() for value in values))
        image.extend(encoded)
        image.extend(b"\0" * (-len(image) % 4))
        image.extend(data)
        image.extend(b"\0" * (-len(image) % 4))
    add("dev", stat.S_IFDIR | 0o755)
    add("dev/console", stat.S_IFCHR | 0o600, major=5, minor=1)
    add("init", stat.S_IFREG | 0o755, executable.read_bytes())
    add("TRAILER!!!", 0)
    destination.write_bytes(image)


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.host)


class VM:
    def __init__(self, binary, directory):
        self.directory = directory
        directory.mkdir()
        self.api = str(directory / "api.sock")
        self.serial = str(directory / "serial.sock")
        self.log = self.serial_log = self.process = None
        self.console = None
        self.buffer = b""
        self.serial_received = 0
        try:
            self.log = open(directory / "vmm.log", "wb")
            self.serial_log = open(directory / "serial-output.log", "wb")
            self.process = subprocess.Popen([binary, "--api-socket", self.api], stdout=self.log, stderr=self.log)
            deadline = time.monotonic() + 20
            while True:
                try:
                    info = self.request("vmm.ping", method="GET", timeout=remaining(deadline))
                    if CAPABILITY not in info.get("features", []):
                        raise RuntimeError("VMM lacks strict CoW capability")
                    break
                except (OSError, http.client.HTTPException):
                    if self.process.poll() is not None or time.monotonic() > deadline:
                        raise RuntimeError("VMM did not become ready")
                    time.sleep(.05)
        except BaseException as error:
            try:
                self.close()
            except Exception as cleanup_error:
                raise RuntimeError(f"{error}; startup cleanup: {cleanup_error}") from error
            raise

    def request(self, route, body=None, method="PUT", timeout=45):
        deadline = time.monotonic() + timeout
        client = UnixHTTP(self.api, timeout=remaining(deadline))
        watchdog = None
        connected = None
        def interrupt():
            try:
                connected.shutdown(socket.SHUT_RDWR)
            except (AttributeError, OSError):
                pass
        try:
            client.connect()
            # Closing HTTP responses clear client.sock while their file owns
            # the read; keep the actual connected socket for deadline shutdown.
            connected = client.sock
            connected.settimeout(remaining(deadline))
            watchdog = threading.Timer(remaining(deadline), interrupt)
            watchdog.start()
            payload = json.dumps(body).encode() if body is not None else None
            client.request(method, "/api/v1/" + route, payload, {"Content-Type": "application/json"})
            response = client.getresponse()
            data = response.read((1 << 20) + 1)
            remaining(deadline)
            if len(data) > 1 << 20:
                raise RuntimeError(f"{route}: API response exceeds 1 MiB limit")
            if response.length not in (None, 0):
                raise RuntimeError(f"{route}: incomplete API response")
            if response.status >= 300:
                raise RuntimeError(f"{route}: {response.status} {data.decode()}")
            return json.loads(data) if data else None
        finally:
            if watchdog:
                watchdog.cancel()
                watchdog.join()
            client.close()

    def connect_serial(self):
        self.console = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.console.settimeout(45)
        self.console.connect(self.serial)

    def expect(self, marker):
        wanted = marker.encode()
        deadline = time.monotonic() + 45
        while True:
            if b"COW-ERROR:" in self.buffer:
                raise RuntimeError("guest memory verification failed: " + self.buffer[-2048:].decode(errors="replace"))
            if wanted in self.buffer:
                break
            self.console.settimeout(remaining(deadline))
            if self.serial_received >= SERIAL_LIMIT:
                raise RuntimeError("serial output reached 1 MiB limit; serial-output.log is truncated")
            piece = self.console.recv(min(65536, SERIAL_LIMIT - self.serial_received))
            if not piece:
                raise RuntimeError("serial console closed")
            self.serial_received += len(piece)
            self.serial_log.write(piece)
            self.serial_log.flush()
            self.buffer += piece
            remaining(deadline)
        self.buffer = self.buffer.split(wanted, 1)[1]

    def command(self, command, expected):
        self.console.sendall(command.encode())
        self.expect("COW:" + expected + ":verified=16777216")

    def snapshot(self, destination):
        destination.mkdir()
        self.request("vm.pause")
        self.request("vm.snapshot", {"destination_url": destination.as_uri()})

    def close(self):
        errors = []
        try:
            if self.console:
                self.console.close()
        except Exception as error:
            errors.append(f"console: {error}")
        if self.process:
            try:
                if self.process.poll() is None:
                    self.process.kill()
            except Exception as error:
                errors.append(f"kill pid {self.process.pid}: {error}")
            try:
                self.process.wait(timeout=10)
            except Exception as error:
                errors.append(f"wait pid {self.process.pid}: {error}")
        for stream in (self.log, self.serial_log):
            try:
                if stream:
                    stream.close()
            except Exception as error:
                errors.append(f"log close: {error}")
        if errors:
            raise RuntimeError(f"{self.directory}: " + "; ".join(errors))


def clone(binary, snapshot, directory, views):
    view = views / directory.name
    view.mkdir()
    config = json.loads((snapshot / "config.json").read_text())
    config["serial"] = {"mode": "Socket", "socket": str(directory / "serial.sock")}
    (view / "config.json").write_text(json.dumps(config))
    shutil.copyfile(snapshot / "state.json", view / "state.json")
    os.link(snapshot / "memory-ranges", view / "memory-ranges")
    vm = VM(binary, directory)
    try:
        vm.request("vm.restore", {"source_url": view.as_uri(), "memory_restore_mode": "CopyOnWrite"})
        vm.connect_serial()
        vm.request("vm.resume")
        return vm
    except BaseException as error:
        try:
            vm.close()
        except Exception as cleanup_error:
            raise RuntimeError(f"{error}; restore cleanup: {cleanup_error}") from error
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default=os.environ.get("CH_BINARY"))
    parser.add_argument("--kernel", default=os.environ.get("CH_TEST_KERNEL"))
    parser.add_argument("--output", required=True)
    parser.add_argument("--required", action="store_true")
    args = parser.parse_args()
    out = pathlib.Path(args.output).resolve()
    out.mkdir(parents=True, exist_ok=False)
    reasons = []
    if len(str(out).encode()) > 75: reasons.append("--output path too long for Unix sockets; use a short path such as /tmp/cow-run")
    if platform.machine() != "x86_64": reasons.append("test guest currently supports x86_64 only")
    try:
        fd = os.open("/dev/kvm", os.O_RDWR | os.O_CLOEXEC)
        os.close(fd)
    except OSError as error:
        reasons.append(f"KVM unavailable: {error}")
    for name, value in (("binary", args.binary), ("kernel", args.kernel)):
        if not value or not pathlib.Path(value).is_file(): reasons.append(f"missing --{name}")
    if not shutil.which("cc"): reasons.append("missing static C compiler")
    report = {"kernel": platform.release(), "architecture": platform.machine(), "status": "not_run", "reasons": reasons}
    if reasons:
        report["status"] = "failed_precondition" if required(args) else "skipped"
        (out / "result.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report))
        return 1 if required(args) else 0
    binary = str(pathlib.Path(args.binary).resolve())
    kernel = str(pathlib.Path(args.kernel).resolve())
    report.update(binary_sha256=digest(binary), kernel_sha256=digest(kernel))
    vms = []
    try:
        init = out / "init"
        subprocess.run(["cc", "-static", "-O2", str(pathlib.Path(__file__).with_name("guest.c")), "-o", str(init)], check=True, timeout=60)
        ramfs = out / "initramfs.cpio"
        initramfs(init, ramfs)
        views = out / "views"; views.mkdir()
        golden = VM(binary, out / "golden-vm"); vms.append(golden)
        golden.request("vm.create", {"cpus": {"boot_vcpus": 1, "max_vcpus": 1}, "memory": {"size": 128 << 20, "shared": False, "thp": False}, "payload": {"kernel": kernel, "initramfs": str(ramfs), "cmdline": "console=ttyS0 rdinit=/init panic=-1"}, "serial": {"mode": "Socket", "socket": golden.serial}, "console": {"mode": "Off"}})
        golden.connect_serial(); golden.request("vm.boot"); golden.expect("COW-READY")
        golden.command("R", "31:00")
        snapshot = out / "golden-snapshot"; golden.snapshot(snapshot); golden.close(); vms.remove(golden)
        memory = snapshot / "memory-ranges"; memory.chmod(0o400); before = digest(memory)
        a = clone(binary, snapshot, out / "a", views); vms.append(a)
        b = clone(binary, snapshot, out / "b", views); vms.append(b)
        a.command("R", "31:00"); b.command("R", "31:00")
        a.command("A", "a1:a2"); b.command("R", "31:00"); b.command("B", "b1:b2"); a.command("R", "a1:a2")
        report["memory_observations"] = {}
        for name, vm in (("a", a), ("b", b)):
            report["memory_observations"][name] = pathlib.Path(f"/proc/{vm.process.pid}/smaps_rollup").read_text()
        for label, vm, expected in (("a", a, "a1:a2"), ("b", b, "b1:b2")):
            for iteration in range(3):
                next_snapshot = out / f"{label}-snapshot-{iteration}"
                vm.snapshot(next_snapshot); vm.close(); vms.remove(vm)
                vm = clone(binary, next_snapshot, out / f"{label}-restore-{iteration}", views); vms.append(vm)
                vm.command("R", expected)
            vm.command("D", "dd:dd")
            dense = out / f"{label}-dense-snapshot"; vm.snapshot(dense); vm.close(); vms.remove(vm)
            vm = clone(binary, dense, out / f"{label}-dense-restore", views); vms.append(vm); vm.command("R", "dd:dd")
        if digest(memory) != before:
            raise RuntimeError("golden memory source changed")
        report["status"] = "passed"
        report["scope"] = "private-anonymous CH guest memory; excludes Substrate virtiofs and end-to-end actor lifecycle"
    except BaseException as error:
        report.update(status="failed", error=f"{type(error).__name__}: {error}")
    finally:
        cleanup_errors = []
        for vm in reversed(vms):
            try:
                vm.close()
            except Exception as error:
                cleanup_errors.append(str(error))
        if cleanup_errors:
            report.update(status="failed", cleanup_errors=cleanup_errors)
        try:
            (out / "result.json").write_text(json.dumps(report, indent=2) + "\n")
        except OSError as error:
            report.update(status="failed", recording_error=str(error))
    print(json.dumps(report))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
