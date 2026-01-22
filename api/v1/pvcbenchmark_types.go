package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PVCSpec defines the PVC characteristics (size, mode, etc.)
type PVCSpec struct {
	// e.g., "10Gi"
	Size string `json:"size,omitempty"`
	// e.g., "ReadWriteOnce"
	AccessMode string `json:"accessMode,omitempty"`
	// Optional: set a StorageClass
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// TestSpec defines the FIO test parameters
type TestSpec struct {
	// e.g., "fio"
	Tool string `json:"tool,omitempty"`
	// e.g., "60s"
	Duration string `json:"duration,omitempty"`
	// Optional: Custom FIO image
	Image string `json:"image,omitempty"`
	// FIO parameters, e.g. {rw: randrw, bs: 4k, size: 1Gi}
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ScaleSpec defines how many PVCs/Pods to create
type ScaleSpec struct {
	PVCCount int `json:"pvc_count,omitempty"`
}

// Metrics holds min, max, sum, average for a single metric
type Metrics struct {
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
	Sum string `json:"sum,omitempty"`
	Avg string `json:"avg,omitempty"`
}

// PVCBenchmarkSpec is the desired state of the CR
type PVCBenchmarkSpec struct {
	PVC   PVCSpec   `json:"pvc,omitempty"`
	Test  TestSpec  `json:"test,omitempty"`
	Scale ScaleSpec `json:"scale,omitempty"`
}

// PVCBenchmarkStatus is the observed state
type PVCBenchmarkStatus struct {
	Phase   string            `json:"phase,omitempty"`
	Results map[string]string `json:"results,omitempty"`
	// We store separate read/write IOPS, latency, bandwidth
	ReadIOPS       Metrics `json:"readIOPS,omitempty"`
	WriteIOPS      Metrics `json:"writeIOPS,omitempty"`
	ReadLatency    Metrics `json:"readLatency,omitempty"`
	WriteLatency   Metrics `json:"writeLatency,omitempty"`
	ReadBandwidth  Metrics `json:"readBandwidth,omitempty"`
	WriteBandwidth Metrics `json:"writeBandwidth,omitempty"`
	CPUUsage       Metrics `json:"cpuUsage,omitempty"`
	// Conditions representing the current state of the benchmark
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PVCBenchmark is the Schema for the pvcbenchmarks API
type PVCBenchmark struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PVCBenchmarkSpec   `json:"spec,omitempty"`
	Status PVCBenchmarkStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PVCBenchmarkList contains a list of PVCBenchmark
type PVCBenchmarkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PVCBenchmark `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PVCBenchmark{}, &PVCBenchmarkList{})
}
