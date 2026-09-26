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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ModelSpec defines the desired state of Model
type ModelSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// +kubebuilder:validation:Pattern=`^hf://.+`
	ArtifactURI string `json:"artifactURI"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=vllm
	Engine string `json:"engine"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.matches('^.+:[^:@]+$')",message="image must include an explicit tag, e.g. vllm/vllm-openai:v0.11.0 (implicit or explicit latest is not allowed"
	// +kubebuilder:validation:XValidation:rule="!self.endsWith(':latest')",message="tag 'latest' is not allowed, pin a version"
	Image string `json:"image"`

	EngineConfig EngineConfig `json:"engineConfig"`

	Serving Serving `json:"serving"`
}

type EngineConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	MaxModelLen int `json:"maxModelLen"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	MaxNumSeqs int `json:"maxNumSeqs"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	EnablePrefixCaching *bool `json:"enablePrefixCaching,omitempty"`
}

type Serving struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas *int `json:"replicas,omitempty"`

	// GPU memory utilization passed to vLLM as --gpu-memory-utilization, e.g. "0.85".
	// +kubebuilder:validation:XValidation:rule="double(self) > 0.0 && double(self) <= 1.0",message="must be a number between 0 and 1, e.g. \"0.85\""
	// +kubebuilder:default="0.85"
	GPUMemoryUtilization *string `json:"gpuMemoryUtilization,omitempty"`
}

// ModelStatus defines the observed state of Model.
type ModelStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the Model resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Pending;Ready;Failed
	Phase string `json:"phase,omitempty"`
	// +optional
	Endpoints []string `json:"endpoints,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.serving.replicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Model is the Schema for the models API
type Model struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Model
	// +required
	Spec ModelSpec `json:"spec"`

	// status defines the observed state of Model
	// +optional
	Status ModelStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ModelList contains a list of Model
type ModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Model `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Model{}, &ModelList{})
		return nil
	})
}
