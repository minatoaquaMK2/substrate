// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkerPoolLabelValue is a Kubernetes label value for generated worker
// workloads.
//
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`
type WorkerPoolLabelValue string

// WorkerPoolPodTemplate defines optional metadata, scheduling, and resource
// settings for worker workloads. NodeAffinity is mapped to
// spec.affinity.nodeAffinity on the pod.
type WorkerPoolPodTemplate struct {
	// Labels are added to the generated Deployment and worker pods. Keys in
	// the ate.dev domain and its subdomains are reserved for controllers.
	//
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(key, !key.startsWith('ate.dev/') && !key.contains('.ate.dev/'))",message="ate.dev and its subdomains are reserved"
	// +kubebuilder:validation:XValidation:rule="self.all(key, !format.qualifiedName().validate(key).hasValue())",message="label keys must be valid Kubernetes qualified names"
	Labels map[string]WorkerPoolLabelValue `json:"labels,omitempty"`

	// Annotations are added to the generated Deployment and worker pods. Keys
	// in the ate.dev domain and its subdomains are reserved for controllers.
	//
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(key, !key.startsWith('ate.dev/') && !key.contains('.ate.dev/'))",message="ate.dev and its subdomains are reserved"
	// +kubebuilder:validation:XValidation:rule="self.all(key, !format.qualifiedName().validate(key).hasValue())",message="annotation keys must be valid Kubernetes qualified names"
	Annotations map[string]string `json:"annotations,omitempty"`

	// NodeSelector is a selector which must be true for the pod to fit on a node.
	//
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for the worker pods.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// PriorityClassName for the worker pods.
	//
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// NodeAffinity scheduling rules for the worker pods. Mapped to
	// spec.affinity.nodeAffinity on the pod.
	//
	// +optional
	NodeAffinity *corev1.NodeAffinity `json:"nodeAffinity,omitempty"`

	// Resources are the compute resources allocated for each worker pod.
	//
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ServiceAccountName for the worker pods. The ServiceAccount must exist
	// in the WorkerPool's namespace. When omitted, the pods use the
	// namespace's default ServiceAccount.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	ServiceAccountName *string `json:"serviceAccountName,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.microVMResources) || (has(self.sandboxClasses) && size(self.sandboxClasses) == 1 && self.sandboxClasses[0].name == 'microvm')",message="microVMResources requires the microvm sandbox class"
type WorkerPoolSpec struct {
	// Replicas is the number of worker pods to run.
	// +required
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	// WorkerImage is the ateom container image to deploy as workers.
	// +kubebuilder:validation:MinLength=1
	// +required
	WorkerImage string `json:"workerImage"`

	// Template holds optional metadata, scheduling, and resource settings for worker workloads.
	//
	// +optional
	Template *WorkerPoolPodTemplate `json:"template,omitempty"`

	// MicroVMResources configures opt-in Worker-local CPU placement and memory
	// restoration. It is allowed only for the microvm sandbox class. Omission
	// preserves the runtime defaults and adds no resource-management arguments.
	// These settings do not grant node-wide CPU exclusivity or increase quotas.
	//
	// +optional
	MicroVMResources *MicroVMResourcesSpec `json:"microVMResources,omitempty"`

	// SandboxClasses lists the sandbox runtime families this pool runs. Exactly
	// one entry is allowed today. The names cannot change after creation.
	//
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:XValidation:rule="self.map(c, c.name) == oldSelf.map(c, c.name)",message="sandboxClasses names are immutable"
	// +listType=map
	// +listMapKey=name
	SandboxClasses []WorkerPoolSandboxClass `json:"sandboxClasses,omitempty"`
}

// CPUAllocationPolicy selects logical-CPU sharing among Actors in one Worker.
//
// +kubebuilder:validation:Enum=Off;Shared;NonOverlapping
type CPUAllocationPolicy string

const (
	CPUAllocationPolicyOff            CPUAllocationPolicy = "Off"
	CPUAllocationPolicyShared         CPUAllocationPolicy = "Shared"
	CPUAllocationPolicyNonOverlapping CPUAllocationPolicy = "NonOverlapping"
)

// CPUEnforcementPolicy controls initialization when CPU placement is unavailable.
// Capacity exhaustion and invalid configuration always fail closed.
//
// +kubebuilder:validation:Enum=Required;BestEffort
type CPUEnforcementPolicy string

const (
	CPUEnforcementPolicyRequired   CPUEnforcementPolicy = "Required"
	CPUEnforcementPolicyBestEffort CPUEnforcementPolicy = "BestEffort"
)

// CPUPlacementPolicy chooses a locality requirement or a degradable preference.
//
// +kubebuilder:validation:Enum=Any;PreferLLC;RequireLLC;PreferNUMA;RequireNUMA
type CPUPlacementPolicy string

const (
	CPUPlacementPolicyAny         CPUPlacementPolicy = "Any"
	CPUPlacementPolicyPreferLLC   CPUPlacementPolicy = "PreferLLC"
	CPUPlacementPolicyRequireLLC  CPUPlacementPolicy = "RequireLLC"
	CPUPlacementPolicyPreferNUMA  CPUPlacementPolicy = "PreferNUMA"
	CPUPlacementPolicyRequireNUMA CPUPlacementPolicy = "RequireNUMA"
)

// MemoryPlacementPolicy selects allowed memory nodes for new Actor allocations.
//
// +kubebuilder:validation:Enum=Inherit;Local
type MemoryPlacementPolicy string

const (
	MemoryPlacementPolicyInherit MemoryPlacementPolicy = "Inherit"
	MemoryPlacementPolicyLocal   MemoryPlacementPolicy = "Local"
)

// MemoryRestorePolicy selects the VMM memory restore path. CoW policies still
// require a compatible VMM, snapshot, and private-memory device configuration.
//
// +kubebuilder:validation:Enum=Legacy;CopyOnWritePreferred;CopyOnWriteRequired
type MemoryRestorePolicy string

const (
	MemoryRestorePolicyLegacy               MemoryRestorePolicy = "Legacy"
	MemoryRestorePolicyCopyOnWritePreferred MemoryRestorePolicy = "CopyOnWritePreferred"
	MemoryRestorePolicyCopyOnWriteRequired  MemoryRestorePolicy = "CopyOnWriteRequired"
)

// MicroVMResourcesSpec contains the supported MicroVM resource policy settings.
// CPU IDs are host-specific operator choices and must fit every selected
// Worker's effective parent boundary. Runtime checks remain authoritative for
// topology, controller delegation, occupancy, and restore compatibility.
//
// +kubebuilder:validation:XValidation:rule="(has(self.cpuAllocationPolicy) && self.cpuAllocationPolicy != 'Off') || (!has(self.actorCPUs) && (!has(self.cpuPlacement) || self.cpuPlacement == 'Any') && (!has(self.memoryPlacement) || self.memoryPlacement == 'Inherit'))",message="actorCPUs, topology placement, and local memory placement require cpuAllocationPolicy Shared or NonOverlapping"
// +kubebuilder:validation:XValidation:rule="!has(self.memoryRestorePolicy) || self.memoryRestorePolicy == 'Legacy' || has(self.memorySnapshotCacheDirectory)",message="CoW restoration requires memorySnapshotCacheDirectory"
type MicroVMResourcesSpec struct {
	// CPUAllocationPolicy controls Worker-local logical-CPU sharing. Off keeps
	// the existing unconstrained path unless managementReservedCPUs is set.
	// NonOverlapping retains fractional bandwidth limits and may reduce density.
	// Defaults to Off.
	//
	// +optional
	// +kubebuilder:default=Off
	CPUAllocationPolicy *CPUAllocationPolicy `json:"cpuAllocationPolicy,omitempty"`

	// CPUEnforcement selects Required initialization or an observable
	// BestEffort fallback when support is unavailable. Defaults to Required.
	//
	// +optional
	// +kubebuilder:default=Required
	CPUEnforcement *CPUEnforcementPolicy `json:"cpuEnforcement,omitempty"`

	// ActorCPUs is an optional debug CPU range list, such as "2-3,8". Its width
	// controls the Actor CPU fence without changing guest vCPUs or CPU quota.
	// Omission selects CPUs automatically. Runtime validates range ordering,
	// capacity, reservation, topology, and current effective boundaries.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:Pattern=`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`
	ActorCPUs *string `json:"actorCPUs,omitempty"`

	// ManagementReservedCPUs is a CPU range list excluded from every managed
	// Actor. Management keeps the full parent CPU set. This is not guaranteed
	// CPU time when a shared ancestor quota is exhausted. Omission reserves none.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:Pattern=`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`
	ManagementReservedCPUs *string `json:"managementReservedCPUs,omitempty"`

	// CPUPlacement selects any logical CPUs, one last-level cache (LLC), or
	// one NUMA node. Prefer policies report degradation; Require policies
	// reject allocations that cannot fit a known single domain. Defaults to Any.
	//
	// +optional
	// +kubebuilder:default=Any
	CPUPlacement *CPUPlacementPolicy `json:"cpuPlacement,omitempty"`

	// MemoryPlacement selects inherited memory nodes or the selected CPUs'
	// local nodes. Local requires known topology and a valid parent memory-node
	// boundary; it does not promise migration of existing/shared pages.
	// Defaults to Inherit.
	//
	// +optional
	// +kubebuilder:default=Inherit
	MemoryPlacement *MemoryPlacementPolicy `json:"memoryPlacement,omitempty"`

	// ManagementCPUWeight sets the management leaf's relative cpu.weight.
	// Zero leaves the weight unchanged; 1 through 10000 selects a weight.
	// This does not guarantee CPU time under a shared parent quota. Defaults to 0.
	//
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10000
	ManagementCPUWeight *int32 `json:"managementCPUWeight,omitempty"`

	// StartupConcurrency limits concurrent cold boots and restores per Worker.
	// Zero retains unbounded startup concurrency. Defaults to 0.
	//
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	StartupConcurrency *int32 `json:"startupConcurrency,omitempty"`

	// MemoryRestorePolicy selects Legacy, observable CopyOnWritePreferred fallback, or
	// CopyOnWriteRequired compatibility enforcement. Current virtiofs-backed Actors
	// require shared memory and cannot use private CoW restoration.
	// Defaults to Legacy.
	//
	// +optional
	// +kubebuilder:default=Legacy
	MemoryRestorePolicy *MemoryRestorePolicy `json:"memoryRestorePolicy,omitempty"`

	// MemorySnapshotCacheDirectory is an absolute private runtime-owned cache
	// directory for immutable snapshot leases. It is required for either CoW
	// policy. Path components may contain letters, numbers, dots, underscores,
	// and hyphens, but must not be '.' or '..'. No additional volume is mounted;
	// operators must provide a writable path with the intended lifecycle.
	//
	// +optional
	// +kubebuilder:validation:MinLength=2
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:Pattern=`^/([A-Za-z0-9._-]+/)*[A-Za-z0-9._-]+/?$`
	// +kubebuilder:validation:XValidation:rule="self.split('/').all(part, part != '.' && part != '..')",message="cache directory must not contain dot or parent path components"
	MemorySnapshotCacheDirectory *string `json:"memorySnapshotCacheDirectory,omitempty"`
}

// WorkerPoolSandboxClass is one sandbox runtime family a WorkerPool runs.
type WorkerPoolSandboxClass struct {
	// Name selects the sandbox runtime family, which drives the worker pod
	// shape (KVM/vhost device mounts and node placement). The concrete binary
	// is still selected by WorkerImage. The sandbox binaries themselves come
	// from the SandboxConfig each ActorTemplate names (required).
	//
	// See Also: TODOs in ActorTemplate SandboxClass
	//
	// +required
	// +kubebuilder:validation:Enum=gvisor;microvm
	Name SandboxClass `json:"name,omitempty"`

	// ConfigRef names a cluster-scoped SandboxConfig for this sandbox class.
	// The referenced config's SandboxClass must match Name. When omitted, the
	// pool uses the newest SandboxConfig of this class annotated
	// sandboxconfig.ate.dev/is-class-default: "true". The controller reports
	// the resolved config in status.sandboxClasses.
	//
	// +optional
	ConfigRef *SandboxConfigReference `json:"configRef,omitempty"`
}

// SandboxConfigReference names a cluster-scoped SandboxConfig.
type SandboxConfigReference struct {
	// Name is the SandboxConfig's metadata.name.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
}

// DefaultSandboxClass returns the sandbox class of the pool's first
// SandboxClasses entry, or "" if there is none.
// Currently SandboxClasses is always a list of length 1.
func (s *WorkerPoolSpec) DefaultSandboxClass() SandboxClass {
	if len(s.SandboxClasses) == 0 {
		return ""
	}
	return s.SandboxClasses[0].Name
}

type WorkerPoolStatus struct {
	// Replicas is the total number of worker pods.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas int32 `json:"replicas"`

	// ReadyReplicas is the number of ready worker pods.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Selector is the label selector for the worker pods.
	// +optional
	Selector string `json:"selector,omitempty"`

	// SandboxClasses reports the SandboxConfig each sandbox class of the pool
	// uses and whether the class's spec resolves.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=1
	// +listType=map
	// +listMapKey=name
	SandboxClasses []WorkerPoolSandboxClassStatus `json:"sandboxClasses,omitempty"`
}

// WorkerPoolSandboxClassStatus reports the SandboxConfig and resolution state
// of one sandbox class of a WorkerPool.
type WorkerPoolSandboxClassStatus struct {
	// Name is the sandbox runtime family.
	//
	// +required
	// +kubebuilder:validation:Enum=gvisor;microvm
	Name SandboxClass `json:"name,omitempty"`

	// ConfigRef names the cluster-scoped SandboxConfig in use. That is the
	// config spec.sandboxClasses[].configRef names, or the class default, or
	// the last resolved config while the spec does not resolve. Omitted when
	// the class has never resolved.
	//
	// +optional
	ConfigRef *SandboxConfigReference `json:"configRef,omitempty"`

	// PendingConfigRef names the SandboxConfig the class is switching to
	// while the controller confirms it against the API server. It protects
	// the config from deletion like configRef does. The same reconcile
	// clears it, and configRef either moves to it or stays where it was.
	//
	// +optional
	PendingConfigRef *SandboxConfigReference `json:"pendingConfigRef,omitempty"`

	// Conditions describe the current state of this sandbox class.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const (
	// WorkerPoolSandboxClassConditionResolved is True when the sandbox
	// class's spec resolves to a usable SandboxConfig. While it is False for
	// any class, the controller applies the worker Deployment only if one
	// already exists or every class has a config in use.
	WorkerPoolSandboxClassConditionResolved = "Resolved"

	// WorkerPoolReasonResolved: the sandbox class resolved.
	WorkerPoolReasonResolved = "Resolved"
	// WorkerPoolReasonSandboxConfigNotFound: configRef names a SandboxConfig
	// that does not exist.
	WorkerPoolReasonSandboxConfigNotFound = "SandboxConfigNotFound"
	// WorkerPoolReasonSandboxConfigDeleting: the SandboxConfig configRef
	// names, or the only class default, is being deleted. This is reported
	// whether or not the pool already uses it.
	WorkerPoolReasonSandboxConfigDeleting = "SandboxConfigDeleting"
	// WorkerPoolReasonSandboxClassMismatch: configRef names a SandboxConfig
	// of another sandbox class.
	WorkerPoolReasonSandboxClassMismatch = "SandboxClassMismatch"
	// WorkerPoolReasonDefaultConfigNotFound: configRef is omitted and no
	// SandboxConfig of the class is annotated as the class default.
	WorkerPoolReasonDefaultConfigNotFound = "DefaultConfigNotFound"
)

// WorkerPool is the Schema for the workerpools API
// +genclient
// +kubebuilder:object:generate=true
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=workerpool
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.status.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="SandboxConfig",type=string,JSONPath=`.status.sandboxClasses[*].configRef.name`
// +kubebuilder:printcolumn:name="Resolved",type=string,JSONPath=`.status.sandboxClasses[*].conditions[?(@.type=="Resolved")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type WorkerPool struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of WorkerPool
	// +required
	Spec WorkerPoolSpec `json:"spec"`

	// status is the observed state of WorkerPool
	// +optional
	Status WorkerPoolStatus `json:"status,omitempty"`
}

// WorkerPoolList contains a list of WorkerPools.
// +kubebuilder:object:generate=true
// +kubebuilder:object:root=true
type WorkerPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkerPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkerPool{}, &WorkerPoolList{})
}
