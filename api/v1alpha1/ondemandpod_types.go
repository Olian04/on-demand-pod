/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// OnDemandPodSpec describes the pod to run and how long it may live.
//
// Immutability is enforced here, on the object, and not on each field. CEL
// transition rules on a field only run when the field exists in both the old
// and new object, so a field-level rule would let a user add or remove an
// optional field after creation. Checking presence and value together at this
// level closes that gap. The cost is that every rule is evaluated on every update.
// +kubebuilder:validation:XValidation:rule="self.image == oldSelf.image",message="image is immutable",fieldPath=".image"
// +kubebuilder:validation:XValidation:rule="has(self.command) ? (has(oldSelf.command) && self.command == oldSelf.command) : !has(oldSelf.command)",message="command is immutable",fieldPath=".command"
// +kubebuilder:validation:XValidation:rule="has(self.args) ? (has(oldSelf.args) && self.args == oldSelf.args) : !has(oldSelf.args)",message="args is immutable",fieldPath=".args"
// +kubebuilder:validation:XValidation:rule="has(self.env) ? (has(oldSelf.env) && self.env == oldSelf.env) : !has(oldSelf.env)",message="env is immutable",fieldPath=".env"
// +kubebuilder:validation:XValidation:rule="has(self.resources) ? (has(oldSelf.resources) && self.resources == oldSelf.resources) : !has(oldSelf.resources)",message="resources is immutable",fieldPath=".resources"
type OnDemandPodSpec struct {
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	Command []string `json:"command,omitempty"`

	Args []string `json:"args,omitempty"`

	Env []corev1.EnvVar `json:"env,omitempty"`

	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// TTL is the total lease length measured from creation. It is the only
	// mutable field. Extending a lease means increasing this value. When
	// empty, the operator's default TTL applies.
	TTL *metav1.Duration `json:"ttl,omitempty"`
}

// OnDemandPodStatus is the observed state of an OnDemandPod.
type OnDemandPodStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	Phase string `json:"phase,omitempty"`

	// ExpiresAt is creationTimestamp + min(spec.ttl, operator max TTL).
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// Conditions: Ready mirrors Pod Ready. TTLClamped is true when
	// spec.ttl exceeds the operator max TTL.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=odp
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.status.expiresAt`

// OnDemandPod is a pod with a lease: the operator runs it and deletes it at expiry.
type OnDemandPod struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec OnDemandPodSpec `json:"spec"`

	// +optional
	Status OnDemandPodStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// OnDemandPodList contains a list of OnDemandPod.
type OnDemandPodList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []OnDemandPod `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &OnDemandPod{}, &OnDemandPodList{})
		return nil
	})
}
