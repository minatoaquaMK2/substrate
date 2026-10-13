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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-substrate/substrate/internal/ateomcgroup"
)

func TestControllerFallbackDoesNotHideEffectiveConfigError(t *testing.T) {
	for _, field := range []string{"explicit", "reserved"} {
		t.Run(field, func(t *testing.T) {
			c := defaultResourceConfig()
			c.Policy, c.Enforcement = "shared", "best-effort"
			if field == "explicit" {
				c.Explicit = "5"
			} else {
				c.Reserved = "5"
			}
			system := testResourceSystem(t) // effective CPUs are 0-1
			controllerCalls, weightWrites := 0, 0
			system.requireControllers = func() error { controllerCalls++; return errors.New("child cpuset unavailable") }
			system.setManagementWeight = func(int) error { weightWrites++; return nil }
			c.ManagementWeight = 100
			if _, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system); err == nil {
				t.Fatal("invalid effective CPU configuration silently fell back")
			}
			if controllerCalls != 0 || weightWrites != 0 {
				t.Fatalf("invalid configuration reached support check or mutation: controllers=%d weight=%d", controllerCalls, weightWrites)
			}
		})
	}
}

func TestControllerFallbackAfterValidEffectiveBoundary(t *testing.T) {
	c := defaultResourceConfig()
	c.Policy, c.Enforcement, c.Explicit = "shared", "best-effort", "1"
	system := testResourceSystem(t)
	controllerErr := errors.New("child cpuset unavailable")
	system.requireControllers = func() error { return controllerErr }
	r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
	if err != nil || r == nil || r.allocator != nil || !strings.Contains(r.fallback, controllerErr.Error()) {
		t.Fatalf("valid unavailable policy did not report off fallback: resources=%+v err=%v", r, err)
	}
}

func TestUnreadableBoundaryUsesSupportFallback(t *testing.T) {
	c := defaultResourceConfig()
	c.Policy, c.Enforcement = "shared", "best-effort"
	system := testResourceSystem(t)
	boundaryErr := errors.New("effective boundary unavailable")
	system.readBoundary = func() (ateomcgroup.CPUBoundary, error) { return ateomcgroup.CPUBoundary{}, boundaryErr }
	system.requireControllers = func() error { t.Fatal("controller preflight preceded unreadable boundary"); return nil }
	r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
	if err != nil || r == nil || r.allocator != nil || !strings.Contains(r.fallback, boundaryErr.Error()) {
		t.Fatalf("unreadable boundary did not report support fallback: resources=%+v err=%v", r, err)
	}
}
