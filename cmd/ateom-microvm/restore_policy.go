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

import (
	"fmt"
	"github.com/agent-substrate/substrate/cmd/ateom-microvm/internal/ch"
	"github.com/spf13/pflag"
)

var (
	memoryRestorePolicy    = pflag.String("memory-restore-policy", ch.RestoreLegacy, "Memory restore policy: legacy, cow-preferred (observable fallback), or cow-required (reject incompatible VMM/snapshot). Current virtiofs actors require shared memory and cannot use CoW.")
	memorySnapshotCacheDir = pflag.String("memory-snapshot-cache-dir", "", "Private runtime-owned cache directory for immutable CoW snapshot leases; required when CoW is selected.")
)

// releaseSnapshotLease requires lifecycle proof independent of CPU placement.
// A missing delegation or unreadable cgroup must not turn into permission to GC.
func (s *AteomService) releaseSnapshotLease(actorUID string, lease interface{ Release() error }) error {
	if lease == nil {
		return nil
	}
	if !s.actorCgroups {
		return fmt.Errorf("cannot release snapshot lease without delegated cgroup proof")
	}
	empty, err := s.actorExecutionEmpty(actorUID)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("actor %s still has snapshot consumers", actorUID)
	}
	return lease.Release()
}
