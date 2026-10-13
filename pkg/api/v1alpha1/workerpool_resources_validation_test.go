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

package v1alpha1

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestWorkerPoolMicroVMResourceValidation(t *testing.T) {
	for i, tc := range []struct {
		name  string
		class SandboxClass
		c     *MicroVMResourcesSpec
		err   string
	}{
		{"nil gvisor", SandboxClassGvisor, nil, ""},
		{"nil microvm", SandboxClassMicroVM, nil, ""},
		{"empty microvm", SandboxClassMicroVM, &MicroVMResourcesSpec{}, ""},
		{"empty gvisor", SandboxClassGvisor, &MicroVMResourcesSpec{}, "microVMResources requires the microvm sandbox class"},
		{"gvisor policy", SandboxClassGvisor, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicy("Shared"))}, "microVMResources requires the microvm sandbox class"},
		{"shared", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicy("Shared")), CPUPlacement: ptr.To(CPUPlacementPolicy("PreferLLC"))}, ""},
		{"nonoverlap", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicy("NonOverlapping")), CPUPlacement: ptr.To(CPUPlacementPolicy("RequireNUMA")), MemoryPlacement: ptr.To(MemoryPlacementPolicy("Local")), ActorCPUs: ptr.To("2-3,8"), ManagementReservedCPUs: ptr.To("0-1")}, ""},
		{"reservation only", SandboxClassMicroVM, &MicroVMResourcesSpec{ManagementReservedCPUs: ptr.To("0-1")}, ""},
		{"empty policy", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicy(""))}, "cpuAllocationPolicy"},
		{"empty CPU list", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicyShared), ActorCPUs: ptr.To("")}, "actorCPUs"},
		{"empty reservation", SandboxClassMicroVM, &MicroVMResourcesSpec{ManagementReservedCPUs: ptr.To("")}, "managementReservedCPUs"},
		{"empty cache", SandboxClassMicroVM, &MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("")}, "memorySnapshotCacheDirectory"},
		{"lowercase policy", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicy("shared"))}, "cpuAllocationPolicy"},
		{"bad enforcement", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUEnforcement: ptr.To(CPUEnforcementPolicy("Optional"))}, "cpuEnforcement"},
		{"bad placement", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicy("Shared")), CPUPlacement: ptr.To(CPUPlacementPolicy("Socket"))}, "cpuPlacement"},
		{"bad memory placement", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicy("Shared")), MemoryPlacement: ptr.To(MemoryPlacementPolicy("Migrate"))}, "memoryPlacement"},
		{"explicit off", SandboxClassMicroVM, &MicroVMResourcesSpec{ActorCPUs: ptr.To("2")}, "require cpuAllocationPolicy"},
		{"placement off", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUPlacement: ptr.To(CPUPlacementPolicy("RequireLLC"))}, "require cpuAllocationPolicy"},
		{"memory off", SandboxClassMicroVM, &MicroVMResourcesSpec{MemoryPlacement: ptr.To(MemoryPlacementPolicy("Local"))}, "require cpuAllocationPolicy"},
		{"malformed set", SandboxClassMicroVM, &MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(CPUAllocationPolicy("Shared")), ActorCPUs: ptr.To("1,,2")}, "actorCPUs"},
		{"malformed reservation", SandboxClassMicroVM, &MicroVMResourcesSpec{ManagementReservedCPUs: ptr.To("1;2")}, "managementReservedCPUs"},
		{"weight low", SandboxClassMicroVM, &MicroVMResourcesSpec{ManagementCPUWeight: ptr.To(int32(-1))}, "managementCPUWeight"},
		{"weight high", SandboxClassMicroVM, &MicroVMResourcesSpec{ManagementCPUWeight: ptr.To(int32(10001))}, "managementCPUWeight"},
		{"concurrency negative", SandboxClassMicroVM, &MicroVMResourcesSpec{StartupConcurrency: ptr.To(int32(-1))}, "startupConcurrency"},
		{"zero numeric", SandboxClassMicroVM, &MicroVMResourcesSpec{ManagementCPUWeight: ptr.To(int32(0)), StartupConcurrency: ptr.To(int32(0))}, ""},
		{"cow required", SandboxClassMicroVM, &MicroVMResourcesSpec{MemoryRestorePolicy: ptr.To(MemoryRestorePolicy("CopyOnWriteRequired")), MemorySnapshotCacheDirectory: ptr.To("/var/lib/ate/cache")}, ""},
		{"cow preferred", SandboxClassMicroVM, &MicroVMResourcesSpec{MemoryRestorePolicy: ptr.To(MemoryRestorePolicy("CopyOnWritePreferred")), MemorySnapshotCacheDirectory: ptr.To("/var/lib/ate/cache")}, ""},
		{"cow missing cache", SandboxClassMicroVM, &MicroVMResourcesSpec{MemoryRestorePolicy: ptr.To(MemoryRestorePolicy("CopyOnWriteRequired"))}, "CoW restoration requires memorySnapshotCacheDirectory"},
		{"bad restore policy", SandboxClassMicroVM, &MicroVMResourcesSpec{MemoryRestorePolicy: ptr.To(MemoryRestorePolicy("Unknown")), MemorySnapshotCacheDirectory: ptr.To("/tmp/cache")}, "memoryRestorePolicy"},
		{"relative cache", SandboxClassMicroVM, &MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("tmp/cache")}, "memorySnapshotCacheDirectory"},
		{"parent cache", SandboxClassMicroVM, &MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("/tmp/../cache")}, "cache directory must not contain dot or parent path components"},
		{"oversize cache", SandboxClassMicroVM, &MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("/" + strings.Repeat("a", 4096))}, "memorySnapshotCacheDirectory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			wp := &WorkerPool{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("resources-%d", i), Namespace: "default"},
				Spec: WorkerPoolSpec{Replicas: 1, WorkerImage: "ateom:test", SandboxClasses: []WorkerPoolSandboxClass{{Name: tc.class}}, MicroVMResources: tc.c}}
			err := k8sClient.Create(ctx, wp)
			if tc.err != "" {
				if err == nil {
					_ = k8sClient.Delete(ctx, wp)
					t.Fatal("invalid resource settings accepted")
				}
				if !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error %v does not contain %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, wp) })
			if tc.c == nil && wp.Spec.MicroVMResources != nil {
				t.Fatal("API defaulted an absent optional resource block")
			}
			if tc.name == "empty microvm" {
				c := wp.Spec.MicroVMResources
				if ptr.Deref(c.CPUAllocationPolicy, "") != CPUAllocationPolicyOff || ptr.Deref(c.CPUEnforcement, "") != CPUEnforcementPolicyRequired || ptr.Deref(c.CPUPlacement, "") != CPUPlacementPolicyAny || ptr.Deref(c.MemoryPlacement, "") != MemoryPlacementPolicyInherit || ptr.Deref(c.MemoryRestorePolicy, "") != MemoryRestorePolicyLegacy || c.ManagementCPUWeight == nil || *c.ManagementCPUWeight != 0 || c.StartupConcurrency == nil || *c.StartupConcurrency != 0 {
					t.Fatalf("wrong static defaults: %+v", c)
				}
			}
		})
	}
}
