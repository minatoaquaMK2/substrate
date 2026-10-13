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

package kata

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCleanupSandboxFilesReportsFailures(t *testing.T) {
	for _, failure := range []string{"read", "unmount", "remove", "multiple"} {
		t.Run(failure, func(t *testing.T) {
			id := "cleanup-test"
			shared := filepath.Join("/run/kata-containers/shared/sandboxes", id)
			vm := filepath.Join(vcVMDir, id)
			mount := filepath.Join(shared, "rootfs")
			readErr, unmountErr, removeErr := errors.New("unreadable mountinfo"), errors.New("detach denied"), errors.New("removal denied")
			var removed []string
			ops := sandboxFileOps{
				readMountinfo: func() ([]byte, error) {
					if failure == "read" {
						return nil, readErr
					}
					return []byte("1 2 0:1 / " + mount + " rw - overlay none rw\n"), nil
				},
				unmount: func(path string) error {
					if path != mount {
						t.Fatalf("unexpected unmount %q", path)
					}
					if failure == "unmount" || failure == "multiple" {
						return unmountErr
					}
					return nil
				},
				removeAll: func(path string) error {
					removed = append(removed, path)
					if failure == "remove" || failure == "multiple" {
						return removeErr
					}
					return nil
				},
			}
			err := cleanupSandboxFiles(context.Background(), id, ops)
			switch failure {
			case "read":
				if !errors.Is(err, readErr) || len(removed) != 0 {
					t.Fatalf("read failure: error=%v removals=%v", err, removed)
				}
			case "unmount":
				if !errors.Is(err, unmountErr) || !reflect.DeepEqual(removed, []string{vm}) {
					t.Fatalf("unmount failure: error=%v removals=%v", err, removed)
				}
			case "remove":
				if !errors.Is(err, removeErr) {
					t.Fatalf("removal failure not returned: %v", err)
				}
			case "multiple":
				if !errors.Is(err, unmountErr) || !errors.Is(err, removeErr) || !reflect.DeepEqual(removed, []string{vm}) {
					t.Fatalf("multiple failures: error=%v removals=%v", err, removed)
				}
			}
		})
	}
}

func TestCleanupSandboxFilesDetachesBeforeRemoving(t *testing.T) {
	id := "cleanup-test"
	shared := filepath.Join("/run/kata-containers/shared/sandboxes", id)
	vm := filepath.Join(vcVMDir, id)
	parent := filepath.Join(shared, "rootfs")
	child := filepath.Join(parent, "child")
	var calls []string
	ops := sandboxFileOps{
		readMountinfo: func() ([]byte, error) {
			return []byte(strings.Join([]string{"1 2 0:1 / " + parent + " rw - overlay none rw", "2 3 0:1 / " + child + " rw - none none rw"}, "\n")), nil
		},
		unmount:   func(path string) error { calls = append(calls, "unmount "+path); return nil },
		removeAll: func(path string) error { calls = append(calls, "remove "+path); return nil },
	}
	if err := cleanupSandboxFiles(context.Background(), id, ops); err != nil {
		t.Fatal(err)
	}
	want := []string{"unmount " + child, "unmount " + parent, "remove " + shared, "remove " + vm}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("cleanup ordering=%v want %v", calls, want)
	}
}
