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
	// e.g., "10Gi"
	Size string `json:"size,omitempty"`
	// e.g., "ReadWriteOnce"
	AccessMode string `json:"accessMode,omitempty"`
	// Optional StorageClass
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// TestSpec holds the benchmarking tool and its parameters
type TestSpec struct {
	// e.g., "fio"
	Tool string `json:"tool,omitempty"`
	// e.g., "60s" (run for 60 seconds)
	Duration string `json:"duration,omitempty"`
	// e.g., {rw: write, bs: 1m, size: 1Gi, ioengine: libaio, direct: "1"}
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ScaleSpec defines how many PVCs (and Pods) to create
type ScaleSpec struct {
	PVCCount int `json:"pvc_count,omitempty"`
}

// PVCBenchmarkStatus defines the observed state of PVCBenchmark
type PVCBenchmarkStatus struct {
	// "Pending", "Running", "Completed", "Failed", etc.
	Phase string `json:"phase,omitempty"`
	// Map of results (like logs or summary)
	Results map[string]string `json:"results,omitempty"`
	ReadIOPS    Metrics           `json:"readIOPS,omitempty"`
   	WriteIOPS   Metrics           `json:"writeIOPS,omitempty"`
    	Latency     Metrics           `json:"latency,omitempty"`
    	Bandwidth   Metrics           `json:"bandwidth,omitempty"`
}

// Metrics defines the structure for storing min, max, avg, and sum values
type Metrics struct {
    Min string `json:"min,omitempty"`
    Max string `json:"max,omitempty"`
    Avg string `json:"avg,omitempty"`
    Sum string `json:"sum,omitempty"`
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
