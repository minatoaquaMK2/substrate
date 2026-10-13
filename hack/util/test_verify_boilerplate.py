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

import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("boilerplate", Path(__file__).with_name("verify-boilerplate.py"))
boilerplate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(boilerplate)


class EvidenceClassificationTests(unittest.TestCase):
    def test_exact_data_categories(self):
        for path in ["docs/benchmarks/results/run/raw-results.jsonl",
                     "docs/research/verification/runtime.log",
                     "docs/research/verification/race/results.jsonl",
                     "docs/upstream/dependencies/validation/check.log",
                     "docs/upstream/dependencies/cloud-hypervisor-v53-cow.patch"]:
            self.assertTrue(boilerplate.is_raw_evidence(path), path)

    def test_source_never_exempted_by_evidence_location(self):
        for directory in ["docs/benchmarks/results/", "docs/research/verification/",
                          "docs/upstream/dependencies/validation/", "docs/upstream/dependencies/"]:
            for extension in [".py", ".sh", ".go", ".c", ".rs", ".yaml"]:
                path = directory + "source" + extension
                self.assertFalse(boilerplate.is_raw_evidence(path), path)

    def test_unrelated_logs_are_not_exempt(self):
        for path in ["internal/implementation.log", "docs/research/other/log.jsonl",
                     "docs/research/verification-extra/record.log", "source.patch"]:
            self.assertFalse(boilerplate.is_raw_evidence(path), path)


if __name__ == "__main__":
    unittest.main()
