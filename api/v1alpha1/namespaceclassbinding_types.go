/*
Copyright 2026 Akuity.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// NamespaceClassBindingReadyCondition indicates whether resources from the selected classes are reconciled.
	NamespaceClassBindingReadyCondition = "Ready"

	// NamespaceClassBindingDefaultName is the only supported NamespaceClassBinding name.
	NamespaceClassBindingDefaultName = "default"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NamespaceClassBindingSpec defines the desired state of NamespaceClassBinding
type NamespaceClassBindingSpec struct {
	// mappings assigns NamespaceClasses to target namespaces.
	// +kubebuilder:validation:Required
	// +listType=map
	// +listMapKey=namespace
	Mappings []NamespaceClassBindingMapping `json:"mappings"`
}

// NamespaceClassBindingMapping assigns NamespaceClasses to one namespace.
type NamespaceClassBindingMapping struct {
	// namespace is the target namespace to reconcile resources into.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`

	// classNames is the list of NamespaceClasses to apply to the target namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +listType=set
	ClassNames []string `json:"classNames"`
}

// NamespaceClassBindingStatus defines the observed state of NamespaceClassBinding.
type NamespaceClassBindingStatus struct {
	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the NamespaceClassBinding resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// namespaces records the resources last applied by namespace.
	// +listType=map
	// +listMapKey=namespace
	// +optional
	Namespaces []NamespaceClassBindingNamespaceStatus `json:"namespaces,omitempty"`
}

// NamespaceClassBindingNamespaceStatus records applied resources for one namespace.
type NamespaceClassBindingNamespaceStatus struct {
	// namespace is the namespace where resources were applied.
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`

	// resources records the resources last applied in this namespace.
	// +listType=map
	// +listMapKey=apiVersion
	// +listMapKey=kind
	// +listMapKey=name
	// +optional
	Resources []NamespaceClassBindingAppliedResource `json:"resources,omitempty"`
}

// NamespaceClassBindingAppliedResource identifies one resource applied by a binding.
type NamespaceClassBindingAppliedResource struct {
	// apiVersion is the applied resource API version.
	// +kubebuilder:validation:Required
	APIVersion string `json:"apiVersion"`

	// kind is the applied resource kind.
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// name is the applied resource name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// className is the NamespaceClass that supplied this resource.
	// +kubebuilder:validation:Required
	ClassName string `json:"className"`

	// uid is the last observed UID for safer deletion if the object is recreated.
	// +optional
	UID types.UID `json:"uid,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=nsclsbind;nsclsb
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="NamespaceClassBinding must be named default"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Namespaces",type=string,JSONPath=".spec.mappings[*].namespace"
// +kubebuilder:printcolumn:name="Generation",type=integer,JSONPath=".metadata.generation",priority=1
// +kubebuilder:printcolumn:name="Observed Generation",type=integer,JSONPath=".status.observedGeneration",priority=1

// NamespaceClassBinding is the Schema for the namespaceclassbindings API
type NamespaceClassBinding struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NamespaceClassBinding
	// +required
	Spec NamespaceClassBindingSpec `json:"spec"`

	// status defines the observed state of NamespaceClassBinding
	// +optional
	Status NamespaceClassBindingStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NamespaceClassBindingList contains a list of NamespaceClassBinding
type NamespaceClassBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NamespaceClassBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NamespaceClassBinding{}, &NamespaceClassBindingList{})
}
