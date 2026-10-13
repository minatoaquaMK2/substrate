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

"""Existing kubectl-ate lifecycle API and Glutton HTTP workload adapter.

No shell execution service is assumed. A command is a Glutton guest operation,
followed by a request-specific echo whose complete protobuf output is checked.
"""

import hashlib
import http.client
import json
import os
import re
import socket
import sys
import signal
import ssl
import struct
import subprocess
import threading
import time
import urllib.parse


class DeadlineExceeded(TimeoutError):
    pass


class RequestRejected(RuntimeError):
    pass


class ActorAlreadyExists(RuntimeError):
    """An explicit collision is not ownership of the existing Actor."""


def remaining(deadline):
    value = deadline - time.monotonic()
    if value <= 0:
        raise DeadlineExceeded("arrival-to-completion deadline exceeded")
    return value


def varint(value):
    if value < 0:
        raise ValueError("only nonnegative protobuf integers are used")
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    result.append(value)
    return bytes(result)


def field(number, value):
    """Encode the small scalar subset used by internal/proto/glutton/glutton.proto."""
    if isinstance(value, str):
        value = value.encode()
    if isinstance(value, bytes):
        return varint(number << 3 | 2) + varint(len(value)) + value
    if isinstance(value, float):
        return varint(number << 3 | 1) + struct.pack("<d", value)
    return varint(number << 3) + varint(value)


def decode(data):
    """Decode scalar fields, rejecting truncation, groups and duplicate fields."""
    pos = 0
    result = {}

    def integer():
        nonlocal pos
        value = 0
        for shift in range(0, 70, 7):
            if pos >= len(data):
                raise ValueError("truncated protobuf varint")
            byte = data[pos]
            pos += 1
            if shift == 63 and byte > 1:
                raise ValueError("protobuf varint overflow")
            value |= (byte & 127) << shift
            if byte < 128:
                return value
        raise ValueError("protobuf varint overflow")

    while pos < len(data):
        tag = integer()
        number, wire = tag >> 3, tag & 7
        if not number or number in result:
            raise ValueError("invalid/duplicate protobuf scalar field")
        if wire == 0:
            value = integer()
        elif wire in (1, 2, 5):
            size = integer() if wire == 2 else {1: 8, 5: 4}[wire]
            if size > len(data) - pos:
                raise ValueError("truncated protobuf field")
            value = data[pos:pos + size]
            pos += size
        else:
            raise ValueError("unsupported protobuf wire type")
        result[number] = value
    return result


def resolve_addresses(host, port):
    """Resolve once, outside measurements; a stuck resolver cannot hang the run."""
    code = ("import json,socket,sys;print(json.dumps(socket.getaddrinfo("
            "sys.argv[1],int(sys.argv[2]),type=socket.SOCK_STREAM)))")
    try:
        result = subprocess.run([sys.executable, "-c", code, host, str(port)],
                                capture_output=True, timeout=10, check=True)
    except subprocess.TimeoutExpired:
        raise DeadlineExceeded("router DNS resolution exceeded 10 seconds") from None
    except subprocess.CalledProcessError as exc:
        raise ValueError("router DNS resolution failed: " + exc.stderr.decode(errors="replace")[:1000]) from exc
    addresses = json.loads(result.stdout)
    if not addresses or len(addresses) > 128:
        raise ValueError("router DNS returned no addresses or more than 128 addresses")
    return addresses


def terminate_and_drain(process, partial_stdout=b"", partial_stderr=b""):
    """Bound drain time even if a detached descendant inherited the output pipe."""
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        stdout, stderr = process.communicate(timeout=.1)
        return stdout, stderr, True
    except subprocess.TimeoutExpired as exc:
        stdout = exc.output if exc.output is not None else partial_stdout
        stderr = exc.stderr if exc.stderr is not None else partial_stderr
        for stream in (process.stdout, process.stderr):
            if stream is not None:
                stream.close()
        try:
            process.wait(timeout=.1)
        except subprocess.TimeoutExpired:
            pass
        return stdout, stderr, False


class SubstrateAdapter:
    synthetic = False

    def __init__(self, cli, atespace, template, router, log, ca=None, cert=None, key=None):
        self.cli, self.atespace, self.template = cli, atespace, template
        self.router = urllib.parse.urlsplit(router)
        if self.router.scheme not in ("http", "https") or not self.router.hostname:
            raise ValueError("router must be an http(s) URL")
        if self.router.username or self.router.password or self.router.query or self.router.fragment:
            raise ValueError("router URL must not contain credentials, query or fragment")
        self.context = ssl.create_default_context(cafile=ca)
        if cert:
            self.context.load_cert_chain(cert, key)
        self.log = log
        self.addresses = resolve_addresses(self.router.hostname, self.router.port or (443 if self.router.scheme == "https" else 80))
        self.log({"type": "router_resolution", "hostname": self.router.hostname, "addresses": self.addresses,
                  "note": "pinned before measurement; original Host header and TLS server name retained"})

    def lifecycle(self, operation, actor, deadline):
        args = self.cli + [operation, "actor", actor, "--atespace", self.atespace]
        if operation == "create":
            args += ["--template", self.template, "--output", "json"]
        elif operation == "delete":
            args += ["--any-state"]
        else:
            args += ["--output", "json"]
        remaining(deadline)  # Never start a side-effecting command after expiry.
        started = time.monotonic()
        process = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   start_new_session=True)
        try:
            stdout, stderr = process.communicate(timeout=remaining(deadline))
        except (subprocess.TimeoutExpired, DeadlineExceeded) as exc:
            stdout, stderr, complete = terminate_and_drain(process, getattr(exc, "output", None) or b"",
                                                           getattr(exc, "stderr", None) or b"")
            self.log({"type": "cli", "actor": actor, "operation": operation,
                      "status": "timeout", "output_complete": complete,
                      "stdout": stdout.decode(errors="replace"), "stderr": stderr.decode(errors="replace")})
            raise DeadlineExceeded(f"{operation} CLI deadline exceeded") from None
        decoded_error = stderr.decode(errors="replace")
        # Classification precedes diagnostic truncation and is independent of logging.
        code = re.search(r"\bcode = (AlreadyExists|ResourceExhausted)\b", decoded_error)
        exception = RuntimeError
        if code and code.group(1) == "AlreadyExists" and operation == "create":
            exception = ActorAlreadyExists
        elif code and code.group(1) == "ResourceExhausted":
            exception = RequestRejected
        try:
            self.log({"type": "cli", "actor": actor, "operation": operation,
                      "status": "ok" if process.returncode == 0 else "error", "output_complete": True,
                      "elapsed_s": time.monotonic() - started, "returncode": process.returncode,
                      "stdout": stdout.decode(errors="replace"), "stderr": decoded_error})
        except Exception:
            if process.returncode and exception is ActorAlreadyExists:
                raise ActorAlreadyExists(f"{operation} explicitly rejected: AlreadyExists") from None
            raise
        if process.returncode:
            raise exception(f"{operation} CLI exit {process.returncode}: " + decoded_error[:2000])
        remaining(deadline)
        return json.loads(stdout) if operation != "delete" else None

    def command(self, actor, route, body, deadline):
        timeout = remaining(deadline)
        cls = http.client.HTTPSConnection if self.router.scheme == "https" else http.client.HTTPConnection
        options = {"context": self.context} if self.router.scheme == "https" else {}
        conn = cls(self.router.hostname, self.router.port, timeout=timeout, **options)

        def connect_pinned(address, timeout=None, source_address=None):
            error = None
            for family, socktype, proto, _, sockaddr in self.addresses:
                remaining(deadline)
                sock = socket.socket(family, socktype, proto)
                try:
                    sock.settimeout(remaining(deadline))
                    if source_address:
                        sock.bind(source_address)
                    sock.connect(tuple(sockaddr))
                    sock.settimeout(remaining(deadline))  # TLS handshake shares the deadline.
                    return sock
                except OSError as exc:
                    error = exc
                    sock.close()
            remaining(deadline)
            raise error or OSError("no pinned router addresses")

        conn._create_connection = connect_pinned
        timer, response = None, None
        expired = threading.Event()
        try:
            conn.connect()
            wire_socket = conn.sock  # Stable even when HTTPConnection drops its reference.

            def expire():
                expired.set()
                try:
                    wire_socket.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

            timer = threading.Timer(remaining(deadline), expire)
            timer.start()
            conn.request("POST", self.router.path.rstrip("/") + route, body=body,
                         headers={"Content-Type": "application/x-protobuf",
                                  "ate-target-actor": f"{self.atespace}/{actor}"})
            response = conn.getresponse()
            lengths = [value for name, value in response.getheaders() if name.lower() == "content-length"]
            if lengths and (len(lengths) != 1 or not lengths[0].isdigit() or response.chunked):
                raise ValueError("invalid or ambiguous guest Content-Length")
            if lengths and int(lengths[0]) > 1024 * 1024:
                raise ValueError("guest command response exceeds 1 MiB")
            chunks, size = [], 0
            while not response.isclosed():
                wire_socket.settimeout(remaining(deadline))
                chunk = response.read1(65536)
                if not chunk:
                    break
                chunks.append(chunk)
                size += len(chunk)
                if size > 1024 * 1024:
                    raise ValueError("guest command response exceeds 1 MiB")
                remaining(deadline)
            remaining(deadline)
            if response.length not in (None, 0) or (lengths and size != int(lengths[0])):
                raise ValueError("truncated guest response Content-Length")
            payload = b"".join(chunks)
            if response.status != 200:
                exception = RequestRejected if response.status == 429 else RuntimeError
                raise exception(f"guest {route}: HTTP {response.status}: {payload[:500]!r}")
            return payload
        except Exception as exc:
            if expired.is_set() or time.monotonic() >= deadline or isinstance(exc, TimeoutError):
                raise DeadlineExceeded(f"guest {route} deadline exceeded") from exc
            raise
        finally:
            if timer is not None:
                timer.cancel()
                timer.join()
            if response is not None:
                response.close()
            conn.close()

    def initialize(self, actor, trial, deadline):
        expected = {}
        if trial["workload"] in ("sparse", "dense"):
            self.command(actor, "/writeram", field(1, "runtime") + field(2, str(trial["memory_bytes"])), deadline)
            for mode, size in (("sparse", min(4096, trial["memory_bytes"])),
                               ("dense", trial["memory_bytes"])):
                value = decode(self.command(actor, "/readram", field(1, "runtime") + field(2, str(size)), deadline))
                if value.get(1, 0) != size:
                    raise ValueError("RAM initialization size mismatch")
                expected[mode] = (size, value.get(2, 0))
        return expected

    def load(self, actor, cores, duty, deadline):
        if type(cores) is not int or not 0 <= cores <= 128:
            raise ValueError("CPU load cores must be an integer in [0,128]")
        if type(duty) not in (int, float) or not 0 <= duty <= 1:
            raise ValueError("CPU load duty must be a finite number in [0,1]")
        body = field(1, cores) + field(2, float(duty)) + field(3, 20) + field(4, 0)
        actual = decode(self.command(actor, "/usecpu", body, deadline)).get(1, 0)
        if actual != cores:
            raise ValueError(f"CPU load requested {cores} goroutines, got {actual}")

    def execute(self, actor, token, trial, expected, deadline):
        workload = trial["workload"]
        verification = {"command": workload}
        if workload == "cpu":
            result = decode(self.command(actor, "/burncpu", field(1, trial["cpu_ms"]) + field(2, 1), deadline))
            iterations = result.get(1, 0)
            if type(iterations) is not int or not 1 <= iterations <= (1 << 63) - 1:
                raise ValueError("BurnCPU iterations must be a positive signed int64")
            verification["iterations"] = result[1]
        elif workload == "io":
            block = b"substrate-runtime-io-v1\n"
            payload = (block * (trial["io_bytes"] // len(block) + 1))[:trial["io_bytes"]]
            digest = hashlib.sha256(payload).digest()
            wrote = decode(self.command(actor, "/ingest", field(1, "runtime") + field(2, payload), deadline))
            read = decode(self.command(actor, "/readdisk", field(1, "runtime") + field(2, 1), deadline))
            if any(v.get(1, 0) != len(payload) or v.get(2) != digest for v in (wrote, read)):
                raise ValueError("guest disk size/digest mismatch")
            verification["sha256"] = digest.hex()
        elif workload in ("sparse", "dense"):
            size, checksum = expected[workload]
            result = decode(self.command(actor, "/readram", field(1, "runtime") + field(2, str(size)), deadline))
            if (result.get(1, 0), result.get(2, 0)) != (size, checksum):
                raise ValueError("restored RAM size/checksum mismatch")
            verification.update(size_bytes=size, checksum=checksum)
        payload = field(1, token)
        if decode(self.command(actor, "/ping", payload, deadline)).get(1) != token.encode():
            raise ValueError("deterministic guest echo output mismatch")
        verification["echo_sha256"] = hashlib.sha256(token.encode()).hexdigest()
        return verification


class SelftestAdapter:
    """Synthetic latency/failure fixture, never evidence of VM performance."""
    synthetic = True
    def __init__(self, delay=0.002, fail_every=0):
        self.delay, self.fail_every = delay, fail_every
        self.actors, self.lock = {}, threading.Lock()

    def lifecycle(self, operation, actor, deadline):
        remaining(deadline)
        with self.lock:
            if operation == "create":
                if actor in self.actors:
                    raise ActorAlreadyExists("duplicate fixture actor")
                self.actors[actor] = "suspended"
            elif operation == "delete":
                self.actors.pop(actor, None)
            elif actor not in self.actors:
                raise ValueError("missing fixture actor")
            else:
                self.actors[actor] = operation
        return {"fixture": True}

    def initialize(self, actor, trial, deadline):
        return {}

    def load(self, actor, cores, duty, deadline):
        remaining(deadline)

    def execute(self, actor, token, trial, expected, deadline):
        duration = min(self.delay, remaining(deadline))
        time.sleep(duration)
        remaining(deadline)
        index = int(token.rsplit("-", 1)[-1])
        if self.fail_every and (index + 1) % self.fail_every == 0:
            raise ValueError("injected self-test verification failure")
        return {"synthetic": True, "echo_sha256": hashlib.sha256(token.encode()).hexdigest()}
