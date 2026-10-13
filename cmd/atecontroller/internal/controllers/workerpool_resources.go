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

package controllers

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/agent-substrate/substrate/internal/cpuplacement"
	atev1alpha1 "github.com/agent-substrate/substrate/pkg/api/v1alpha1"
	"k8s.io/utils/ptr"
)

var (
	resourceCPUListPattern = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)
	resourceCachePattern   = regexp.MustCompile(`^/([A-Za-z0-9._-]+/)*[A-Za-z0-9._-]+/?$`)
)

// microVMResourceArgs validates the typed policy before translating it to the
// runtime's flags. It never passes arbitrary arguments or expands permissions.
// Validation mirrors the CRD and also checks CPU-range ordering/expansion.
func microVMResourceArgs(wp *atev1alpha1.WorkerPool) ([]string, error) {
	c := wp.Spec.MicroVMResources
	if c == nil {
		return nil, nil
	}
	if len(wp.Spec.SandboxClasses) != 1 || wp.Spec.DefaultSandboxClass() != atev1alpha1.SandboxClassMicroVM {
		return nil, fmt.Errorf("microVMResources requires the microvm sandbox class")
	}
	policy := ptr.Deref(c.CPUAllocationPolicy, atev1alpha1.CPUAllocationPolicyOff)
	placement := ptr.Deref(c.CPUPlacement, atev1alpha1.CPUPlacementPolicyAny)
	memoryPlacement := ptr.Deref(c.MemoryPlacement, atev1alpha1.MemoryPlacementPolicyInherit)
	if policy == atev1alpha1.CPUAllocationPolicyOff {
		if c.ActorCPUs != nil || placement != atev1alpha1.CPUPlacementPolicyAny || memoryPlacement != atev1alpha1.MemoryPlacementPolicyInherit {
			return nil, fmt.Errorf("actorCPUs, topology placement, and local memory placement require cpuAllocationPolicy Shared or NonOverlapping")
		}
	}
	if ptr.Deref(c.MemoryRestorePolicy, atev1alpha1.MemoryRestorePolicyLegacy) != atev1alpha1.MemoryRestorePolicyLegacy && c.MemorySnapshotCacheDirectory == nil {
		return nil, fmt.Errorf("CoW restoration requires memorySnapshotCacheDirectory")
	}
	var args []string
	for _, option := range []struct {
		field, flag, value string
		present            bool
		values             map[string]string
	}{
		{"cpuAllocationPolicy", "actor-cpu-policy", string(ptr.Deref(c.CPUAllocationPolicy, "")), c.CPUAllocationPolicy != nil, map[string]string{"Off": "off", "Shared": "shared", "NonOverlapping": "nonoverlapping"}},
		{"cpuEnforcement", "actor-cpu-enforcement", string(ptr.Deref(c.CPUEnforcement, "")), c.CPUEnforcement != nil, map[string]string{"Required": "required", "BestEffort": "best-effort"}},
		{"cpuPlacement", "actor-cpu-placement", string(ptr.Deref(c.CPUPlacement, "")), c.CPUPlacement != nil, map[string]string{"Any": "any", "PreferLLC": "prefer-llc", "RequireLLC": "require-llc", "PreferNUMA": "prefer-numa", "RequireNUMA": "require-numa"}},
		{"memoryPlacement", "actor-memory-placement", string(ptr.Deref(c.MemoryPlacement, "")), c.MemoryPlacement != nil, map[string]string{"Inherit": "inherit", "Local": "local"}},
		{"memoryRestorePolicy", "memory-restore-policy", string(ptr.Deref(c.MemoryRestorePolicy, "")), c.MemoryRestorePolicy != nil, map[string]string{"Legacy": "legacy", "CopyOnWritePreferred": "cow-preferred", "CopyOnWriteRequired": "cow-required"}},
	} {
		if !option.present {
			continue
		}
		value, ok := option.values[option.value]
		if !ok {
			return nil, fmt.Errorf("unsupported %s %q", option.field, option.value)
		}
		args = append(args, "--"+option.flag+"="+value)
	}
	var actorCPUs, reservedCPUs cpuplacement.Set
	for _, option := range []struct {
		field, flag string
		value       *string
		parsed      *cpuplacement.Set
	}{
		{"actorCPUs", "actor-cpus", c.ActorCPUs, &actorCPUs},
		{"managementReservedCPUs", "management-reserved-cpus", c.ManagementReservedCPUs, &reservedCPUs},
	} {
		if option.value == nil {
			continue
		}
		if len(*option.value) > 4096 || !resourceCPUListPattern.MatchString(*option.value) {
			return nil, fmt.Errorf("%s must be a CPU range list of at most 4096 characters", option.field)
		}
		parsed, err := cpuplacement.ParseSet(*option.value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", option.field, err)
		}
		*option.parsed = parsed
		args = append(args, "--"+option.flag+"="+*option.value)
	}
	if actorCPUs.Intersection(reservedCPUs).Len() != 0 {
		return nil, fmt.Errorf("explicit Actor CPUs overlap the management reservation")
	}
	if c.ManagementCPUWeight != nil {
		if *c.ManagementCPUWeight < 0 || *c.ManagementCPUWeight > 10000 {
			return nil, fmt.Errorf("managementCPUWeight must be between 0 and 10000")
		}
		args = append(args, "--management-cpu-weight="+strconv.FormatInt(int64(*c.ManagementCPUWeight), 10))
	}
	if c.StartupConcurrency != nil {
		if *c.StartupConcurrency < 0 {
			return nil, fmt.Errorf("startupConcurrency must be nonnegative")
		}
		args = append(args, "--runtime-startup-concurrency="+strconv.FormatInt(int64(*c.StartupConcurrency), 10))
	}
	if c.MemorySnapshotCacheDirectory != nil {
		path := *c.MemorySnapshotCacheDirectory
		if len(path) > 4096 || !resourceCachePattern.MatchString(path) {
			return nil, fmt.Errorf("memorySnapshotCacheDirectory must be an absolute path of at most 4096 characters using letters, numbers, dots, underscores, and hyphens")
		}
		for _, part := range strings.Split(path, "/") {
			if part == "." || part == ".." {
				return nil, fmt.Errorf("memorySnapshotCacheDirectory must not contain dot or parent path components")
			}
		}
		args = append(args, "--memory-snapshot-cache-dir="+path)
	}
	return args, nil
}
