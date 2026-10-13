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

package snapshotcache

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCrashLeaseHelper(t *testing.T) {
	root := os.Getenv("SUBSTRATE_SNAPSHOTCACHE_CRASH_ROOT")
	if root == "" {
		return
	}
	c, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	l, err := c.Acquire(context.Background(), "review-scope", os.Getenv("SUBSTRATE_SNAPSHOTCACHE_CRASH_SOURCE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "acquired"), []byte(l.Dir), 0o600); err != nil {
		t.Fatal(err)
	}
	// Bypass every defer and Release, modeling abrupt owner exit.
	os.Exit(23)
}

func TestCrashLeaseSurvivesOwnerExit(t *testing.T) {
	c := cache(t)
	src := source(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashLeaseHelper$", "-test.v")
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "SUBSTRATE_SNAPSHOTCACHE_CRASH_ROOT="+c.root, "SUBSTRATE_SNAPSHOTCACHE_CRASH_SOURCE="+src)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("crash helper exceeded deadline: %v: %s", ctx.Err(), out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("child did not reach abrupt exit: %v: %s", err, out)
	}
	marker, err := os.ReadFile(filepath.Join(c.root, "acquired"))
	if err != nil {
		t.Fatal(err)
	}
	view := strings.TrimSpace(string(marker))
	if filepath.Dir(view) != filepath.Join(c.root, "leases") {
		t.Fatalf("unexpected lease %q", view)
	}
	if err := os.Truncate(filepath.Join(src, memoryFile), 0); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(c.root)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.GC(context.Background()); n != 0 || err != nil {
		t.Fatalf("crash lease collected: %d %v", n, err)
	}
	b, err := os.ReadFile(filepath.Join(view, memoryFile))
	if err != nil || len(b) != 2*os.Getpagesize() || b[0] != 0x31 {
		t.Fatalf("crash lease lost immutable bytes: %d %v", len(b), err)
	}
	if err := (&Lease{Dir: view}).Release(); err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.GC(context.Background()); n != 1 || err != nil {
		t.Fatalf("reconciled lease not collected: %d %v", n, err)
	}
}
