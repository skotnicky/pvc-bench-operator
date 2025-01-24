package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PVCSpec defines the PVC characteristics (size, mode, etc.)
type PVCSpec struct {
	Size             string  `json:"size,omitempty"`       // e.g., "10Gi"
	AccessMode       string  `json:"accessMode,omitempty"` // e.g., "ReadWriteOnce"
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// TestSpec defines the FIO test parameters
type TestSpec struct {
	Tool       string            `json:"tool,omitempty"`       // e.g., "fio"
	Duration   string            `json:"duration,omitempty"`   // e.g., "60s"
	Parameters map[string]string `json:"parameters,omitempty"` // e.g. { rw: write, bs: 1m, size: 1Gi, ...}
}

// ScaleSpec defines how many PVCs/Pods to create
type ScaleSpec struct {
	PVCCount int `json:"pvc_count,omitempty"`
}

// Metrics is a structure to hold min, max, sum, avg strings (or floats)
type Metrics struct {
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
	Sum string `json:"sum,omitempty"`
	Avg string `json:"avg,omitempty"`
}

// PVCBenchmarkSpec is the desired state
type PVCBenchmarkSpec struct {
	PVC   PVCSpec   `json:"pvc,omitempty"`
	Test  TestSpec  `json:"test,omitempty"`
	Scale ScaleSpec `json:"scale,omitempty"`
}

// PVCBenchmarkStatus is the observed state
type PVCBenchmarkStatus struct {
	Phase     string            `json:"phase,omitempty"`
	Results   map[string]string `json:"results,omitempty"`
	ReadIOPS  Metrics           `json:"readIOPS,omitempty"`
	WriteIOPS Metrics           `json:"writeIOPS,omitempty"`
	Latency   Metrics           `json:"latency,omitempty"`
	Bandwidth Metrics           `json:"bandwidth,omitempty"`
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
