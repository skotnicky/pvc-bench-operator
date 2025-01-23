package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PVCBenchmarkSpec defines the desired state of PVCBenchmark
type PVCBenchmarkSpec struct {
	PVC   PVCSpec   `json:"pvc,omitempty"`
	Test  TestSpec  `json:"test,omitempty"`
	Scale ScaleSpec `json:"scale,omitempty"`
}

// PVCSpec holds the PVC configuration details
type PVCSpec struct {
	// Size of the PVC, e.g., "10Gi"
	Size string `json:"size,omitempty"`
	// AccessMode, e.g., "ReadWriteOnce"
	AccessMode string `json:"accessMode,omitempty"`
	// Optional: specify a particular StorageClass
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// TestSpec holds the benchmarking tool and its parameters
type TestSpec struct {
	// Tool, e.g., "fio"
	Tool string `json:"tool,omitempty"`
	// Duration, e.g., "60s"
	Duration string `json:"duration,omitempty"`
	// Parameters is a map of key/value pairs that map to benchmarking tool flags
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ScaleSpec defines how many PVCs (and corresponding Pods) to create
type ScaleSpec struct {
	PVCCount int `json:"pvc_count,omitempty"`
}

// PVCBenchmarkStatus defines the observed state of PVCBenchmark
type PVCBenchmarkStatus struct {
	// Phase can be: "Pending", "Running", "Completed", "Failed"
	Phase string `json:"phase,omitempty"`
	// Results stores performance metrics, logs, or aggregated output from the benchmarking tool
	Results map[string]string `json:"results,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// PVCBenchmark is the Schema for the pvcbenchmarks API
type PVCBenchmark struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PVCBenchmarkSpec   `json:"spec,omitempty"`
	Status PVCBenchmarkStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PVCBenchmarkList contains a list of PVCBenchmark
type PVCBenchmarkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PVCBenchmark `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PVCBenchmark{}, &PVCBenchmarkList{})
}
