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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// RateLimit configures the per-client token bucket for a Route.
type RateLimit struct {
	// +kubebuilder:validation:Minimum=1
	RequestsPerSecond int32 `json:"requestsPerSecond"`

	// +kubebuilder:validation:Minimum=1
	Burst int32 `json:"burst"`
}

// RouteSpec defines the desired state of Route.
type RouteSpec struct {
	// +kubebuilder:validation:Pattern=`^/`
	Prefix string `json:"prefix"`

	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:MinLength=1
	Upstreams []string `json:"upstreams"`

	// +optional
	RateLimit *RateLimit `json:"rateLimit,omitempty"`
}

// RouteStatus defines the observed state of Route.
type RouteStatus struct {
	// ObservedGeneration is the resource generation represented by Conditions.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Prefix",type=string,JSONPath=`.spec.prefix`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`

// Route is the Schema for the routes API
type Route struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Route
	// +required
	Spec RouteSpec `json:"spec"`

	// status defines the observed state of Route
	// +optional
	Status RouteStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RouteList contains a list of Route
type RouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Route `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Route{}, &RouteList{})
		return nil
	})
}
