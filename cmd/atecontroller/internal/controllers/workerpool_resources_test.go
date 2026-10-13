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
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/agent-substrate/substrate/internal/installdefaults"
	atev1alpha1 "github.com/agent-substrate/substrate/pkg/api/v1alpha1"
	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func resourceWorkerPool(class atev1alpha1.SandboxClass, resources *atev1alpha1.MicroVMResourcesSpec) *atev1alpha1.WorkerPool {
	return &atev1alpha1.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "resources", Namespace: "default"},
		Spec: atev1alpha1.WorkerPoolSpec{Replicas: 1, WorkerImage: "ateom:test",
			SandboxClasses: []atev1alpha1.WorkerPoolSandboxClass{{Name: class}}, MicroVMResources: resources},
	}
}

func TestMicroVMResourceArgsComplete(t *testing.T) {
	c := &atev1alpha1.MicroVMResourcesSpec{
		CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicyNonOverlapping),
		CPUEnforcement:      ptr.To(atev1alpha1.CPUEnforcementPolicyRequired),
		ActorCPUs:           ptr.To("2-3,8"), ManagementReservedCPUs: ptr.To("0-1"),
		CPUPlacement:        ptr.To(atev1alpha1.CPUPlacementPolicyRequireNUMA),
		MemoryPlacement:     ptr.To(atev1alpha1.MemoryPlacementPolicyLocal),
		ManagementCPUWeight: ptr.To(int32(200)), StartupConcurrency: ptr.To(int32(3)),
		MemoryRestorePolicy:          ptr.To(atev1alpha1.MemoryRestorePolicyCopyOnWritePreferred),
		MemorySnapshotCacheDirectory: ptr.To("/var/lib/ate/snapshot-cache"),
	}
	wp := resourceWorkerPool(atev1alpha1.SandboxClassMicroVM, c)
	want := []string{
		"--actor-cpu-policy=nonoverlapping", "--actor-cpu-enforcement=required",
		"--actor-cpu-placement=require-numa", "--actor-memory-placement=local",
		"--memory-restore-policy=cow-preferred", "--actor-cpus=2-3,8",
		"--management-reserved-cpus=0-1", "--management-cpu-weight=200",
		"--runtime-startup-concurrency=3", "--memory-snapshot-cache-dir=/var/lib/ate/snapshot-cache",
	}
	args, err := microVMResourceArgs(wp)
	if err != nil || !slices.Equal(args, want) {
		t.Fatalf("args=%v err=%v want=%v", args, err, want)
	}
	got := buildDeploymentApplyConfig(wp, ateomOTelSettings{}, installdefaults.SystemNamespace, installdefaults.AteletServiceAccount, installdefaults.RouterServiceAccount)
	base := buildDeploymentApplyConfig(resourceWorkerPool(atev1alpha1.SandboxClassMicroVM, nil), ateomOTelSettings{}, installdefaults.SystemNamespace, installdefaults.AteletServiceAccount, installdefaults.RouterServiceAccount)
	base.Spec.Template.Spec.Containers[0].Args = append(base.Spec.Template.Spec.Containers[0].Args, want...)
	if diff := cmp.Diff(base, got); diff != "" {
		t.Fatalf("resource policy changed fields besides typed args (-want +got):\n%s", diff)
	}
	copy := wp.DeepCopy()
	*copy.Spec.MicroVMResources.CPUAllocationPolicy = atev1alpha1.CPUAllocationPolicyOff
	*copy.Spec.MicroVMResources.CPUEnforcement = atev1alpha1.CPUEnforcementPolicyBestEffort
	*copy.Spec.MicroVMResources.ActorCPUs = "9"
	*copy.Spec.MicroVMResources.ManagementReservedCPUs = "10"
	*copy.Spec.MicroVMResources.CPUPlacement = atev1alpha1.CPUPlacementPolicyAny
	*copy.Spec.MicroVMResources.MemoryPlacement = atev1alpha1.MemoryPlacementPolicyInherit
	*copy.Spec.MicroVMResources.MemoryRestorePolicy = atev1alpha1.MemoryRestorePolicyLegacy
	*copy.Spec.MicroVMResources.MemorySnapshotCacheDirectory = "/tmp/changed"
	*copy.Spec.MicroVMResources.ManagementCPUWeight = 999
	*copy.Spec.MicroVMResources.StartupConcurrency = 999
	if *c.ManagementCPUWeight != 200 || *c.StartupConcurrency != 3 || *c.CPUAllocationPolicy != atev1alpha1.CPUAllocationPolicyNonOverlapping || *c.CPUEnforcement != atev1alpha1.CPUEnforcementPolicyRequired || *c.ActorCPUs != "2-3,8" || *c.ManagementReservedCPUs != "0-1" || *c.CPUPlacement != atev1alpha1.CPUPlacementPolicyRequireNUMA || *c.MemoryPlacement != atev1alpha1.MemoryPlacementPolicyLocal || *c.MemoryRestorePolicy != atev1alpha1.MemoryRestorePolicyCopyOnWritePreferred || *c.MemorySnapshotCacheDirectory != "/var/lib/ate/snapshot-cache" {
		t.Fatal("generated deepcopy aliases resource policy pointers")
	}
}

func TestMicroVMResourceArgsNilAndDefaults(t *testing.T) {
	for _, class := range []atev1alpha1.SandboxClass{"", atev1alpha1.SandboxClassGvisor, atev1alpha1.SandboxClassMicroVM} {
		wp := resourceWorkerPool(class, nil)
		args, err := microVMResourceArgs(wp)
		if err != nil || len(args) != 0 {
			t.Fatalf("class %s nil resource settings changed defaults: %v %v", class, args, err)
		}
	}
	for _, c := range []*atev1alpha1.MicroVMResourcesSpec{{}, {
		CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicyOff),
		CPUEnforcement:      ptr.To(atev1alpha1.CPUEnforcementPolicyBestEffort),
		CPUPlacement:        ptr.To(atev1alpha1.CPUPlacementPolicyAny), MemoryPlacement: ptr.To(atev1alpha1.MemoryPlacementPolicyInherit),
		MemoryRestorePolicy: ptr.To(atev1alpha1.MemoryRestorePolicyLegacy),
		ManagementCPUWeight: ptr.To(int32(0)), StartupConcurrency: ptr.To(int32(0)),
	}} {
		args, err := microVMResourceArgs(resourceWorkerPool(atev1alpha1.SandboxClassMicroVM, c))
		if err != nil {
			t.Fatal(err)
		}
		if c.CPUAllocationPolicy == nil && len(args) != 0 {
			t.Fatal("zero settings added arguments")
		}
		if c.CPUAllocationPolicy != nil && (!slices.Contains(args, "--management-cpu-weight=0") || !slices.Contains(args, "--runtime-startup-concurrency=0")) {
			t.Fatal("explicit zero intent lost")
		}
	}
}

func TestMicroVMResourceArgsRejectInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    atev1alpha1.MicroVMResourcesSpec
	}{
		{"empty policy", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy(""))}},
		{"empty enforcement", atev1alpha1.MicroVMResourcesSpec{CPUEnforcement: ptr.To(atev1alpha1.CPUEnforcementPolicy(""))}},
		{"empty CPU list", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicyShared), ActorCPUs: ptr.To("")}},
		{"empty reservation", atev1alpha1.MicroVMResourcesSpec{ManagementReservedCPUs: ptr.To("")}},
		{"empty cache", atev1alpha1.MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("")}},
		{"policy", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy("shared"))}},
		{"enforcement", atev1alpha1.MicroVMResourcesSpec{CPUEnforcement: ptr.To(atev1alpha1.CPUEnforcementPolicy("SilentlyIgnore"))}},
		{"placement", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy("Shared")), CPUPlacement: ptr.To(atev1alpha1.CPUPlacementPolicy("socket"))}},
		{"memory", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy("Shared")), MemoryPlacement: ptr.To(atev1alpha1.MemoryPlacementPolicy("MigrateAll"))}},
		{"restore", atev1alpha1.MicroVMResourcesSpec{MemoryRestorePolicy: ptr.To(atev1alpha1.MemoryRestorePolicy("Cow")), MemorySnapshotCacheDirectory: ptr.To("/tmp/cache")}},
		{"cpu off", atev1alpha1.MicroVMResourcesSpec{ActorCPUs: ptr.To("2")}},
		{"locality off", atev1alpha1.MicroVMResourcesSpec{CPUPlacement: ptr.To(atev1alpha1.CPUPlacementPolicy("RequireLLC"))}},
		{"memory off", atev1alpha1.MicroVMResourcesSpec{MemoryPlacement: ptr.To(atev1alpha1.MemoryPlacementPolicy("Local"))}},
		{"CPU format", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy("Shared")), ActorCPUs: ptr.To("2;--privileged")}},
		{"CPU whitespace", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy("Shared")), ActorCPUs: ptr.To(" 2")}},
		{"CPU ordering", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy("Shared")), ActorCPUs: ptr.To("4-2")}},
		{"CPU expansion", atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy("Shared")), ActorCPUs: ptr.To("0-999999999")}},
		{"reservation format", atev1alpha1.MicroVMResourcesSpec{ManagementReservedCPUs: ptr.To("two")}},
		{"reservation length", atev1alpha1.MicroVMResourcesSpec{ManagementReservedCPUs: ptr.To(strings.Repeat("1,", 2048) + "2")}},
		{"negative weight", atev1alpha1.MicroVMResourcesSpec{ManagementCPUWeight: ptr.To(int32(-1))}},
		{"excessive weight", atev1alpha1.MicroVMResourcesSpec{ManagementCPUWeight: ptr.To(int32(10001))}},
		{"negative startups", atev1alpha1.MicroVMResourcesSpec{StartupConcurrency: ptr.To(int32(-1))}},
		{"missing cache", atev1alpha1.MicroVMResourcesSpec{MemoryRestorePolicy: ptr.To(atev1alpha1.MemoryRestorePolicy("CopyOnWriteRequired"))}},
		{"relative cache", atev1alpha1.MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("tmp/cache")}},
		{"cache root", atev1alpha1.MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("/")}},
		{"cache parent", atev1alpha1.MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("/tmp/../cache")}},
		{"cache dot", atev1alpha1.MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("/tmp/./cache")}},
		{"cache shell", atev1alpha1.MicroVMResourcesSpec{MemorySnapshotCacheDirectory: ptr.To("/tmp/$(TOKEN)")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := microVMResourceArgs(resourceWorkerPool(atev1alpha1.SandboxClassMicroVM, &tc.c))
			if err == nil || len(args) != 0 {
				t.Fatalf("invalid configuration emitted arguments: %v %v", args, err)
			}
		})
	}
	for _, class := range []atev1alpha1.SandboxClass{"", atev1alpha1.SandboxClassGvisor} {
		wp := resourceWorkerPool(class, &atev1alpha1.MicroVMResourcesSpec{})
		if _, err := microVMResourceArgs(wp); err == nil {
			t.Fatal("resource block accepted for non-MicroVM pool")
		}
		// A nil client proves validation occurs before any external write.
		if err := (&WorkerPoolReconciler{}).applyDeployment(context.Background(), wp); err == nil {
			t.Fatal("invalid gVisor policy reached Deployment apply")
		}
	}
}

func TestMicroVMResourceArgsAllLocalityEnums(t *testing.T) {
	for policy, value := range map[atev1alpha1.CPUPlacementPolicy]string{
		"Any": "any", "PreferLLC": "prefer-llc", "RequireLLC": "require-llc", "PreferNUMA": "prefer-numa", "RequireNUMA": "require-numa",
	} {
		args, err := microVMResourceArgs(resourceWorkerPool(atev1alpha1.SandboxClassMicroVM, &atev1alpha1.MicroVMResourcesSpec{CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicy("Shared")), CPUPlacement: ptr.To(policy)}))
		if err != nil || !slices.Contains(args, "--actor-cpu-placement="+value) {
			t.Fatalf("%s: %v %v", policy, args, err)
		}
	}
}

func TestMicroVMResourceArgsRejectOverlappingReservation(t *testing.T) {
	for _, enforcement := range []atev1alpha1.CPUEnforcementPolicy{atev1alpha1.CPUEnforcementPolicyRequired, atev1alpha1.CPUEnforcementPolicyBestEffort} {
		t.Run(string(enforcement), func(t *testing.T) {
			wp := resourceWorkerPool(atev1alpha1.SandboxClassMicroVM, &atev1alpha1.MicroVMResourcesSpec{
				CPUAllocationPolicy: ptr.To(atev1alpha1.CPUAllocationPolicyShared), CPUEnforcement: ptr.To(enforcement),
				ActorCPUs: ptr.To("1-3"), ManagementReservedCPUs: ptr.To("3-4"),
			})
			if args, err := microVMResourceArgs(wp); err == nil || len(args) != 0 {
				t.Fatalf("overlapping explicit/reserved CPUs reached translation: args=%v err=%v", args, err)
			}
			if err := (&WorkerPoolReconciler{}).applyDeployment(context.Background(), wp); err == nil {
				t.Fatal("overlap reached Deployment apply")
			}
		})
	}
}
