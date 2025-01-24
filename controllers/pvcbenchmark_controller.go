package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	pvcv1 "github.com/skotnicky/pvc-bench-operator/api/v1" // Adjust module path
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PVCBenchmarkReconciler reconciles a PVCBenchmark object
type PVCBenchmarkReconciler struct {
	client.Client
	Log logr.Logger
}

//+kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims;pods;events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods/log,verbs=get


func (r *PVCBenchmarkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the PVCBenchmark instance
	var benchmark pvcv1.PVCBenchmark
	if err := r.Get(ctx, req.NamespacedName, &benchmark); err != nil {
		if errors.IsNotFound(err) {
			// CR is gone, no-op
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// If the CR is being deleted, handle finalizers if needed
	if !benchmark.ObjectMeta.DeletionTimestamp.IsZero() {
		// cleanup logic if desired
		return ctrl.Result{}, nil
	}

	// Initialize status if blank
	if benchmark.Status.Phase == "" {
		benchmark.Status.Phase = "Pending"
		if err := r.Status().Update(ctx, &benchmark); err != nil {
			logger.Error(err, "Failed to set phase=Pending")
			return ctrl.Result{}, err
		}
	}

	// 2. Ensure PVCs exist
	if err := r.ensurePVCs(ctx, &benchmark); err != nil {
		logger.Error(err, "Failed to ensure PVCs")
		return ctrl.Result{}, err
	}

	// 3. Ensure Pods exist
	if err := r.ensureBenchmarkPods(ctx, &benchmark); err != nil {
		logger.Error(err, "Failed to ensure benchmark pods")
		return ctrl.Result{}, err
	}

	// 4. Check if Pods completed & parse logs
	completed, results, readIOPS, writeIOPS, latency, bandwidth, err := r.checkAndCollectResults(ctx, &benchmark)
	if err != nil {
		logger.Error(err, "Error collecting results")
		return ctrl.Result{}, err
	}
	if completed {
		// All pods done → update status
		benchmark.Status.Phase = "Completed"
		benchmark.Status.Results = results
		benchmark.Status.ReadIOPS = readIOPS
		benchmark.Status.WriteIOPS = writeIOPS
		benchmark.Status.Latency = latency
		benchmark.Status.Bandwidth = bandwidth

		if err := r.Status().Update(ctx, &benchmark); err != nil {
			logger.Error(err, "Failed to update status=Completed")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// If not completed, requeue after 10s
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// ensurePVCs creates N PVCs if not already present
func (r *PVCBenchmarkReconciler) ensurePVCs(ctx context.Context, benchmark *pvcv1.PVCBenchmark) error {
	pvcCount := benchmark.Spec.Scale.PVCCount
	for i := 0; i < pvcCount; i++ {
		pvcName := fmt.Sprintf("%s-pvc-%d", benchmark.Name, i)

		var pvc corev1.PersistentVolumeClaim
		err := r.Get(ctx, types.NamespacedName{
			Namespace: benchmark.Namespace,
			Name:      pvcName,
		}, &pvc)
		if err != nil && errors.IsNotFound(err) {
			// Create it
			newPVC := corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pvcName,
					Namespace: benchmark.Namespace,
					Labels: map[string]string{
						"app": "pvc-bench-fio",
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.PersistentVolumeAccessMode(benchmark.Spec.PVC.AccessMode),
					},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse(benchmark.Spec.PVC.Size),
						},
					},
				},
			}
			if benchmark.Spec.PVC.StorageClassName != nil {
				newPVC.Spec.StorageClassName = benchmark.Spec.PVC.StorageClassName
			}
			// Set ownership
			if err := controllerutil.SetControllerReference(benchmark, &newPVC, r.Scheme()); err != nil {
				return err
			}
			if err := r.Create(ctx, &newPVC); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		// If found, do nothing
	}
	return nil
}

// ensureBenchmarkPods creates Pods that run fio on each PVC
func (r *PVCBenchmarkReconciler) ensureBenchmarkPods(ctx context.Context, benchmark *pvcv1.PVCBenchmark) error {
	podCount := benchmark.Spec.Scale.PVCCount
	for i := 0; i < podCount; i++ {
		podName := fmt.Sprintf("%s-bench-%d", benchmark.Name, i)
		pvcName := fmt.Sprintf("%s-pvc-%d", benchmark.Name, i)

		var pod corev1.Pod
		err := r.Get(ctx, types.NamespacedName{
			Namespace: benchmark.Namespace,
			Name:      podName,
		}, &pod)
		if err != nil && errors.IsNotFound(err) {
			// Construct fio args
			fioArgs := buildFioArgs(benchmark.Spec.Test.Parameters)
			if benchmark.Spec.Test.Duration != "" {
				fioArgs = append(fioArgs, fmt.Sprintf("--runtime=%s", benchmark.Spec.Test.Duration), "--time_based")
			}
			// Add directory, JSON output, job name, filename
			fioArgs = append(fioArgs,
				"--directory=/mnt/storage",
				"--output-format=json",
				"--name=benchtest",
				"--filename=/mnt/storage/testfile",
			)

			newPod := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: benchmark.Namespace,
					Labels: map[string]string{
						"app": "pvc-bench-fio",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Volumes: []corev1.Volume{
						{
							Name: "pvc-volume",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "fio-benchmark",
							Image:   "ghcr.io/skotnicky/pvc-operator/fio:latest",
							Command: []string{"fio"},
							Args:    fioArgs,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "pvc-volume",
									MountPath: "/mnt/storage",
								},
							},
						},
					},
				},
			}
			if err := controllerutil.SetControllerReference(benchmark, &newPod, r.Scheme()); err != nil {
				return err
			}
			if err := r.Create(ctx, &newPod); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		// If found, do nothing
	}
	return nil
}

// checkAndCollectResults checks if pods are done & parses logs
func (r *PVCBenchmarkReconciler) checkAndCollectResults(
	ctx context.Context,
	benchmark *pvcv1.PVCBenchmark,
) (bool, map[string]string, pvcv1.Metrics, pvcv1.Metrics, pvcv1.Metrics, pvcv1.Metrics, error) {

	results := make(map[string]string)
	completedCount := 0
	total := benchmark.Spec.Scale.PVCCount

	// We want to track readIOPS, writeIOPS, latency, bandwidth, etc.
	// We can define aggregator for each or just store them in aggregator.
	readIopsAgg := newStatAgg()
	writeIopsAgg := newStatAgg()
	latAgg := newStatAgg()
	bwAgg := newStatAgg()

	// Create a direct clientset for logs
	config, err := rest.InClusterConfig()
	if err != nil {
		return false, nil, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return false, nil, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, err
	}

	for i := 0; i < total; i++ {
		podName := fmt.Sprintf("%s-bench-%d", benchmark.Name, i)
		var pod corev1.Pod
		if err := r.Get(ctx, types.NamespacedName{Namespace: benchmark.Namespace, Name: podName}, &pod); err != nil {
			return false, nil, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, err
		}

		switch pod.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			completedCount++
			// Gather logs
			logs, logErr := getPodLogs(clientset, benchmark.Namespace, podName)
			if logErr != nil {
				results[podName] = fmt.Sprintf("Error reading logs: %v", logErr)
				continue
			}
			// Parse
			var fioData FioJSON
			parseErr := json.Unmarshal([]byte(logs), &fioData)
			if parseErr != nil {
				results[podName] = string(logs) // store raw logs if JSON parse fails
				continue
			}

			// If we have at least 1 job
			if len(fioData.Jobs) > 0 {
				j := fioData.Jobs[0]
				// read is j.Read, write is j.Write
				readIopsVal := j.Read.Iops
				writeIopsVal := j.Write.Iops
				// bandwidth (KB/s) => pick read/write, or just do write if only writing
				// from your sample, "write" has iops=185.33, bw=189778
				// You might treat lat = j.Write.clat_ns.mean, etc.
				// For brevity, let's treat "lat" as the write's clat_ns.mean
				latVal := j.Write.ClatNs.Mean
				bwVal := float64(j.Write.Bw) // KB/s

				// Aggregation
				readIopsAgg.Add(readIopsVal)
				writeIopsAgg.Add(writeIopsVal)
				latAgg.Add(latVal)
				bwAgg.Add(bwVal)

				results[podName] = fmt.Sprintf(
					"ReadIOPS=%.2f, WriteIOPS=%.2f, Lat=%.2f ns, BW=%.2f KB/s",
					readIopsVal, writeIopsVal, latVal, bwVal,
				)
			} else {
				// no job
				results[podName] = string(logs)
			}
		default:
			// Pod still running
		}
	}

	// If all done, compute final min/max/avg
	if completedCount == total {
		readIOPS := pvcv1.Metrics{
			Min: formatFloat(readIopsAgg.Min),
			Max: formatFloat(readIopsAgg.Max),
			Sum: formatFloat(readIopsAgg.Sum),
			Avg: formatFloat(readIopsAgg.Avg()),
		}
		writeIOPS := pvcv1.Metrics{
			Min: formatFloat(writeIopsAgg.Min),
			Max: formatFloat(writeIopsAgg.Max),
			Sum: formatFloat(writeIopsAgg.Sum),
			Avg: formatFloat(writeIopsAgg.Avg()),
		}
		latency := pvcv1.Metrics{
			Min: formatFloat(latAgg.Min),
			Max: formatFloat(latAgg.Max),
			Sum: formatFloat(latAgg.Sum),
			Avg: formatFloat(latAgg.Avg()),
		}
		bandwidth := pvcv1.Metrics{
			Min: formatFloat(bwAgg.Min),
			Max: formatFloat(bwAgg.Max),
			Sum: formatFloat(bwAgg.Sum),
			Avg: formatFloat(bwAgg.Avg()),
		}
		return true, results, readIOPS, writeIOPS, latency, bandwidth, nil
	}
	return false, nil, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, nil
}

// buildFioArgs transforms the CR's TestSpec parameters into fio flags
func buildFioArgs(params map[string]string) []string {
	var args []string
	for k, v := range params {
		args = append(args, fmt.Sprintf("--%s=%s", k, v))
	}
	return args
}

// getPodLogs fetches logs from container "fio-benchmark"
func getPodLogs(clientset *kubernetes.Clientset, namespace, podName string) (string, error) {
	logOpts := &corev1.PodLogOptions{
		Container: "fio-benchmark",
	}
	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, logOpts)
	stream, err := req.Stream(context.Background())
	if err != nil {
		return "", err
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SetupWithManager sets up the controller
func (r *PVCBenchmarkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pvcv1.PVCBenchmark{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

// -------------------------------------------------------
// Aggregator + FioJSON struct
// -------------------------------------------------------

// StatAgg helps track min, max, sum, and count for a float metric
type StatAgg struct {
	Min   float64
	Max   float64
	Sum   float64
	Count int
}

// newStatAgg instantiates aggregator with big Min
func newStatAgg() StatAgg {
	return StatAgg{
		Min:   math.MaxFloat64,
		Max:   0,
		Sum:   0,
		Count: 0,
	}
}

func (s *StatAgg) Add(val float64) {
	if val < s.Min {
		s.Min = val
	}
	if val > s.Max {
		s.Max = val
	}
	s.Sum += val
	s.Count++
}

func (s *StatAgg) Avg() float64 {
	if s.Count == 0 {
		return 0
	}
	return s.Sum / float64(s.Count)
}

func formatFloat(val float64) string {
	return strconv.FormatFloat(val, 'f', 2, 64)
}

// FioJSON example struct for your posted logs
type FioJSON struct {
	FioVersion string `json:"fio version,omitempty"`
	Jobs       []struct {
		Jobname string `json:"jobname"`
		Read    struct {
			Bw   int64   `json:"bw"` // KB/s
			Iops float64 `json:"iops"`
			// lat/clat if needed
		} `json:"read"`
		Write struct {
			Bw     int64   `json:"bw"`
			Iops   float64 `json:"iops"`
			ClatNs struct {
				Mean float64 `json:"mean"`
				// etc.
			} `json:"clat_ns"`
		} `json:"write"`
	} `json:"jobs"`
}
