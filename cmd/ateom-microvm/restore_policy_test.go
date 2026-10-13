//go:build linux

// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import "testing"

type countedSnapshotLease struct{ releases int }

func (l *countedSnapshotLease) Release() error { l.releases++; return nil }

func TestSnapshotLeaseRetainedWithoutOwnershipProof(t *testing.T) {
	s := &AteomService{}
	l := &countedSnapshotLease{}
	if err := s.releaseSnapshotLease("actor", l); err == nil {
		t.Fatal("released without delegated cgroup proof")
	}
	if l.releases != 0 {
		t.Fatal("uncertain ownership released immutable source")
	}
	if err := s.releaseSnapshotLease("actor", nil); err != nil {
		t.Fatal(err)
	}
}
