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

"""Record commands through local stubs; never create a cgroup or run root tests."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class DelegationScopeTests(unittest.TestCase):
    def test_privileged_entrypoint_selects_only_owned_subtree_test(self):
        repo = Path(__file__).resolve().parents[2]
        with tempfile.TemporaryDirectory(prefix="placement-scope-") as tmp:
            root = Path(tmp); binaries = root / "bin"; binaries.mkdir()
            commands = root / "commands"
            for name in ("go", "python3"):
                executable = binaries / name
                executable.write_text('#!/bin/sh\nprintf "%s\\n" "' + name + ' $*" >> "$RECORDED_COMMANDS"\n')
                executable.chmod(0o700)
            env = dict(os.environ, PATH=str(binaries) + os.pathsep + os.environ["PATH"],
                       CPU_PLACEMENT_CGROUP_ROOT=str(root / "operator-subtree"),
                       RUNTIME_VALIDATION_OUTPUT=str(root / "results"),
                       RECORDED_COMMANDS=str(commands))
            result = subprocess.run(["bash", "hack/runtime-resources/validate.sh"], cwd=repo,
                                    env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            tests = [line for line in commands.read_text().splitlines() if line.startswith("go test ")]
            self.assertEqual(len(tests), 1, "ordinary tests must not inherit privileged handoff")
            self.assertIn("./internal/ateomcgroup", tests[0])
            self.assertIn("-run ^TestCPUPlacementKernelEnforcement$", tests[0])
            self.assertIn("-timeout=60s", tests[0])
            self.assertNotIn("./...", tests[0])
            self.assertNotIn("./cmd/", tests[0])
            self.assertFalse(any(line.startswith("python3 ") for line in commands.read_text().splitlines()))


if __name__ == "__main__":
    unittest.main()
