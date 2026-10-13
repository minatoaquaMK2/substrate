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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/agent-substrate/substrate/internal/ateomcgroup"
	"github.com/agent-substrate/substrate/internal/cpuplacement"
)

func TestPreflightReviewUsesExplicitTopologyCandidates(t *testing.T) {
	for _, kind := range []string{"require-llc", "require-numa", "local"} {
		for _, enforcement := range []string{"required", "best-effort"} {
			t.Run(kind+"/"+enforcement, func(t *testing.T) {
				c := defaultResourceConfig()
				c.Policy = "shared"
				c.Enforcement = enforcement
				c.Explicit = "0"
				if kind == "local" {
					c.MemoryPlacement = "local"
				} else {
					c.Placement = kind
				}
				system := testResourceSystem(t)
				system.discoverTopology = func(cpuplacement.Set) (cpuplacement.Topology, error) {
					return cpuplacement.Topology{Online: cpuSet(t, "0-1"), CPUs: map[int]cpuplacement.CPU{0: {ID: 0, NUMA: -1}, 1: {ID: 1, NUMA: 1, LLC: "known-but-not-selected"}}}, nil
				}
				r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
				if enforcement == "required" {
					if err == nil {
						t.Fatal("unrelated CPU topology let explicit unknown CPU publish ready")
					}
				} else if err != nil || r == nil || r.allocator != nil || r.fallback == "" {
					t.Fatalf("explicit unknown topology did not report support fallback: r=%+v err=%v", r, err)
				}
			})
		}
	}
}

func TestPreflightReviewKnownDisallowedMemoryIsNotSupportFallback(t *testing.T) {
	for _, enforcement := range []string{"required", "best-effort"} {
		c := defaultResourceConfig()
		c.Policy = "nonoverlapping"
		c.Enforcement = enforcement
		c.MemoryPlacement = "local"
		system := testResourceSystem(t)
		system.readBoundary = func() (ateomcgroup.CPUBoundary, error) {
			return ateomcgroup.CPUBoundary{CPUs: cpuSet(t, "0-1"), Mems: cpuSet(t, "2")}, nil
		}
		r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
		if err == nil || r != nil {
			t.Fatalf("%s changed capacity failure into unplaced policy: r=%+v err=%v", enforcement, r, err)
		}
	}
}

func TestPreflightReviewCoWWeightRechecksHierarchy(t *testing.T) {
	c := defaultResourceConfig()
	c.TrackExecution = true
	c.ManagementWeight = 100
	system := testResourceSystem(t)
	changed := false
	writes := 0
	sentinel := errors.New("hierarchy changed during boundary read")
	system.captureHierarchy = func() (runtimeHierarchy, error) {
		return testRuntimeHierarchy{verify: func() error {
			if changed {
				return sentinel
			}
			return nil
		}}, nil
	}
	read := system.readBoundary
	system.readBoundary = func() (ateomcgroup.CPUBoundary, error) { changed = true; return read() }
	system.setManagementWeight = func(int) error { writes++; return nil }
	if r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system); !errors.Is(err, sentinel) || r != nil || writes != 0 {
		t.Fatalf("changed CoW hierarchy accepted/mutated: r=%+v err=%v writes=%d", r, err, writes)
	}
}

func TestPreflightReviewReadyAfterManagementMutation(t *testing.T) {
	old := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	c := defaultResourceConfig()
	c.Policy = "shared"
	c.ManagementWeight = 100
	system := testResourceSystem(t)
	sentinel := errors.New("uncertain weight write")
	system.setManagementWeight = func(int) error { return sentinel }
	if _, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system); !errors.Is(err, sentinel) {
		t.Fatalf("weight failure=%v", err)
	}
	if strings.Contains(logs.String(), "Runtime CPU policy ready") {
		t.Fatalf("published actual-ready before failed final mutation: %s", logs.String())
	}
}

func TestPreflightReviewExplicitStrictCrossDomainIsInvalid(t *testing.T) {
	for _, placement := range []string{"require-llc", "require-numa"} {
		for _, enforcement := range []string{"required", "best-effort"} {
			t.Run(placement+"/"+enforcement, func(t *testing.T) {
				c := defaultResourceConfig()
				c.Policy = "shared"
				c.Enforcement = enforcement
				c.Placement = placement
				c.Explicit = "0-1"
				r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, testResourceSystem(t))
				if !errors.Is(err, cpuplacement.ErrPlacement) || r != nil {
					t.Fatalf("fixed cross-domain request did not fail strict placement: r=%+v err=%v", r, err)
				}
			})
		}
	}
}

func TestPreflightReviewKnownExplicitInvalidityPrecedesMissingSupport(t *testing.T) {
	for _, kind := range []string{"local", "require-llc", "require-numa", "local-with-missing-llc", "require-llc-with-missing-numa"} {
		t.Run(kind, func(t *testing.T) {
			c := defaultResourceConfig()
			c.Policy = "shared"
			c.Enforcement = "best-effort"
			c.Explicit = "0-2"
			if strings.HasPrefix(kind, "local") || kind == "require-llc-with-missing-numa" {
				c.MemoryPlacement = "local"
			} else {
				c.Placement = kind
			}
			if strings.Contains(kind, "with-missing") {
				c.Placement = "require-llc"
			}
			system := testResourceSystem(t)
			system.readBoundary = func() (ateomcgroup.CPUBoundary, error) {
				return ateomcgroup.CPUBoundary{CPUs: cpuSet(t, "0-2"), Mems: cpuSet(t, "1")}, nil
			}
			system.discoverTopology = func(cpuplacement.Set) (cpuplacement.Topology, error) {
				topology := cpuplacement.Topology{Online: cpuSet(t, "0-2"), CPUs: map[int]cpuplacement.CPU{
					0: {ID: 0, NUMA: 0, LLC: "llc0"},
					1: {ID: 1, NUMA: 1, LLC: "llc1"},
					2: {ID: 2, NUMA: -1},
				}}
				if kind == "local-with-missing-llc" {
					cpu := topology.CPUs[0]
					cpu.LLC = ""
					topology.CPUs[0] = cpu
				}
				if kind == "require-llc-with-missing-numa" {
					cpu := topology.CPUs[0]
					cpu.NUMA = -1
					topology.CPUs[0] = cpu
				}
				return topology, nil
			}
			r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
			if err == nil || r != nil {
				t.Fatalf("unknown CPU hid known-invalid fixed request: r=%+v err=%v", r, err)
			}
		})
	}
}

func TestPreflightReviewExplicitOfflineIsNotMissingSupport(t *testing.T) {
	for _, placement := range []string{"any", "require-llc", "require-numa"} {
		t.Run(placement, func(t *testing.T) {
			c := defaultResourceConfig()
			c.Policy = "shared"
			c.Enforcement = "best-effort"
			c.Explicit = "0"
			c.Placement = placement
			system := testResourceSystem(t)
			system.discoverTopology = func(cpuplacement.Set) (cpuplacement.Topology, error) {
				return cpuplacement.Topology{Online: cpuSet(t, "1"), CPUs: map[int]cpuplacement.CPU{1: {ID: 1, NUMA: 1, LLC: "llc1"}}}, nil
			}
			r, err := newRuntimeResourcesWithSystem(context.Background(), c, true, system)
			if err == nil || r != nil {
				t.Fatalf("known-offline explicit CPU caused support fallback: r=%+v err=%v", r, err)
			}
		})
	}
}
