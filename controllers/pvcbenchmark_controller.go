package controllers

import (
    "context"
    "fmt"
    "strconv"
    "time"

    "github.com/go-logr/logr"
    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/api/errors"
    "k8s.io/apimachinery/pkg/api/resource"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/types"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
    "sigs.k8s.io/controller-runtime/pkg/log"

    pvcv1 "github.com/skotnicky/pvc-bench-operator/api/v1"
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

func (r *PVCBenchmarkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    logger := log.FromContext(ctx)

    // Fetch the PVCBenchmark instance
    var benchmark pvcv1.PVCBenchmark
    if err := r.Get(ctx, req.NamespacedName, &benchmark); err != nil {
        if errors.IsNotFound(err) {
            // The CR is gone; nothing to do
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, err
    }

    // Check if being deleted/finalizers
    if !benchmark.ObjectMeta.DeletionTimestamp.IsZero() {
        // handle finalizer/cleanup if needed
        return ctrl.Result{}, nil
    }

    // Initialize status if empty
    if benchmark.Status.Phase == "" {
        benchmark.Status.Phase = "Pending"
        if err := r.Status().Update(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to update initial status")
            return ctrl.Result{}, err
        }
    }

    // Ensure the required PVCs exist
    if err := r.ensurePVCs(ctx, &benchmark); err != nil {
        logger.Error(err, "Failed to ensure PVCs")
        return ctrl.Result{}, err
    }

    // Ensure benchmarking Pods exist
    if err := r.ensureBenchmarkPods(ctx, &benchmark); err != nil {
        logger.Error(err, "Failed to ensure benchmark pods")
        return ctrl.Result{}, err
    }

    // Check if Pods completed & gather results
    completed, results, readIOPS, writeIOPS, latency, bandwidth, err := r.checkAndCollectResults(ctx, &benchmark)
    if err != nil {
        logger.Error(err, "Error collecting results")
        return ctrl.Result{}, err
    }
    if completed {
        benchmark.Status.Phase = "Completed"
        benchmark.Status.Results = results
        benchmark.Status.ReadIOPS = readIOPS
        benchmark.Status.WriteIOPS = writeIOPS
        benchmark.Status.Latency = latency
        benchmark.Status.Bandwidth = bandwidth
        if err := r.Status().Update(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to update status to Completed")
            return ctrl.Result{}, err
        }

        return ctrl.Result{}, nil
    }

    // Requeue after 10s if not done
    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// ensurePVCs creates or ensures the existence of the required PVCs
func (r *PVCBenchmarkReconciler) ensurePVCs(ctx context.Context, benchmark *pvcv1.PVCBenchmark) error {
    for i := 0; i < benchmark.Spec.Scale.PVCCount; i++ {
        pvcName := fmt.Sprintf("%s-pvc-%d", benchmark.Name, i)

        var pvc corev1.PersistentVolumeClaim
        err := r.Get(ctx, types.NamespacedName{
            Namespace: benchmark.Namespace,
            Name:      pvcName,
        }, &pvc)
        if err != nil && errors.IsNotFound(err) {
            // Create a new PVC
            newPVC := corev1.PersistentVolumeClaim{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      pvcName,
                    Namespace: benchmark.Namespace,
		    Labels: map[string]string{
          		  "app": "pvc-bench-fio", // used to match in anti-affinity
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
            // If user specified a StorageClass
            if benchmark.Spec.PVC.StorageClassName != nil {
                newPVC.Spec.StorageClassName = benchmark.Spec.PVC.StorageClassName
            }

            // Make PVC owned by CR
            if err := controllerutil.SetControllerReference(benchmark, &newPVC, r.Scheme()); err != nil {
                return err
            }
            if err := r.Create(ctx, &newPVC); err != nil {
                return err
            }
        } else if err != nil {
            return err
        }
        // If PVC found, do nothing
    }
    return nil
}

// ensureBenchmarkPods creates Pods that mount each PVC and run fio
func (r *PVCBenchmarkReconciler) ensureBenchmarkPods(ctx context.Context, benchmark *pvcv1.PVCBenchmark) error {
    for i := 0; i < benchmark.Spec.Scale.PVCCount; i++ {
        podName := fmt.Sprintf("%s-bench-%d", benchmark.Name, i)
        pvcName := fmt.Sprintf("%s-pvc-%d", benchmark.Name, i)

        var pod corev1.Pod
        err := r.Get(ctx, types.NamespacedName{
            Namespace: benchmark.Namespace,
            Name:      podName,
	    },&pod)
        if err != nil && errors.IsNotFound(err) {
            // Build fio args from spec
            fioArgs := buildFioArgs(benchmark.Spec.Test.Parameters)
            // Optionally add duration
            if benchmark.Spec.Test.Duration != "" {
                fioArgs = append(fioArgs,
                    fmt.Sprintf("--runtime=%s", benchmark.Spec.Test.Duration),
                    "--time_based",
                )
            }
            // Add output directory and format
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
                    TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
                        {
                            MaxSkew: 1,
                            TopologyKey: "kubernetes.io/hostname",
                            WhenUnsatisfiable: corev1.ScheduleAnyway, // <-- Allow scheduling even if constraints aren't perfectly met
                            LabelSelector: &metav1.LabelSelector{
                                MatchLabels: map[string]string{
                                    "app": "pvc-bench-fio",
                                },
                            },
                        },
                    },
                    Affinity: &corev1.Affinity{
                        PodAntiAffinity: &corev1.PodAntiAffinity{
                            PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
                                {
                                    Weight: 100,
                                    PodAffinityTerm: corev1.PodAffinityTerm{
                                        LabelSelector: &metav1.LabelSelector{
                                            MatchLabels: map[string]string{
                                                "app": "pvc-bench-fio",
                                            },
                                        },
                                        TopologyKey: "kubernetes.io/hostname",
                                    },
                                },
                            },
                        },
                    },
                    Containers: []corev1.Container{
                        {
                            Name:    "fio-benchmark",
                            Image:   "ghcr.io/skotnicky/pvc-operator/fio", // Replace with your actual FIO image
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

            // Make Pod owned by CR
            if err := controllerutil.SetControllerReference(benchmark, &newPod, r.Scheme()); err != nil {
                return err
            }
            if err := r.Create(ctx, &newPod); err != nil {
                return err
            }
        } else if err != nil {
            return err
        }
        // If Pod found, do nothing
    }
    return nil
}

// checkAndCollectResults checks if all Pods are done and gathers results
func (r *PVCBenchmarkReconciler) checkAndCollectResults(ctx context.Context, benchmark *pvcv1.PVCBenchmark) (bool, map[string]string, pvcv1.Metrics, pvcv1.Metrics, pvcv1.Metrics, pvcv1.Metrics, error) {
    results := make(map[string]string)
    readIOPS := pvcv1.Metrics{}
    writeIOPS := pvcv1.Metrics{}
    latency := pvcv1.Metrics{}
    bandwidth := pvcv1.Metrics{}
    completedCount := 0
    total := benchmark.Spec.Scale.PVCCount

    // Initialize min values to a large number
    readIOPS.Min = ""
    writeIOPS.Min = ""
    latency.Min = ""
    bandwidth.Min = ""

    for i := 0; i < total; i++ {
        podName := fmt.Sprintf("%s-bench-%d", benchmark.Name, i)
        var pod corev1.Pod
        if err := r.Get(ctx, types.NamespacedName{
            Namespace: benchmark.Namespace,
            Name:      podName,
        }, &pod); err != nil {
            return false, nil, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, err
        }

        // If Pod is succeeded/failed, it's done
        switch pod.Status.Phase {
        case corev1.PodSucceeded, corev1.PodFailed:
            completedCount++
            results[podName] = string(pod.Status.Phase)

            // Assume results are stored in Pod annotations or other fields
            if readIOPSStr, ok := pod.Annotations["benchmark/readIOPS"]; ok {
                readIOPSVal, err := strconv.ParseFloat(readIOPSStr, 64)
                if err == nil {
                    readIOPS.Sum = formatFloat(readIOPSVal + parseMetric(readIOPS.Sum))
                    readIOPS.Min = formatMin(readIOPS.Min, readIOPSVal)
                    readIOPS.Max = formatMax(readIOPS.Max, readIOPSVal)
                }
            }

            if writeIOPSStr, ok := pod.Annotations["benchmark/writeIOPS"]; ok {
                writeIOPSVal, err := strconv.ParseFloat(writeIOPSStr, 64)
                if err == nil {
                    writeIOPS.Sum = formatFloat(writeIOPSVal + parseMetric(writeIOPS.Sum))
                    writeIOPS.Min = formatMin(writeIOPS.Min, writeIOPSVal)
                    writeIOPS.Max = formatMax(writeIOPS.Max, writeIOPSVal)
                }
            }

            if latencyStr, ok := pod.Annotations["benchmark/latency"]; ok {
                latencyVal, err := strconv.ParseFloat(latencyStr, 64)
                if err == nil {
                    latency.Sum = formatFloat(latencyVal + parseMetric(latency.Sum))
                    latency.Min = formatMin(latency.Min, latencyVal)
                    latency.Max = formatMax(latency.Max, latencyVal)
                }
            }

            if bandwidthStr, ok := pod.Annotations["benchmark/bandwidth"]; ok {
                bandwidthVal, err := strconv.ParseFloat(bandwidthStr, 64)
                if err == nil {
                    bandwidth.Sum = formatFloat(bandwidthVal + parseMetric(bandwidth.Sum))
                    bandwidth.Min = formatMin(bandwidth.Min, bandwidthVal)
                    bandwidth.Max = formatMax(bandwidth.Max, bandwidthVal)
                }
            }
        default:
            // Pod is still Running or Pending
        }
    }

    if completedCount == total {
        readIOPS.Avg = formatFloat(parseMetric(readIOPS.Sum) / float64(total))
        writeIOPS.Avg = formatFloat(parseMetric(writeIOPS.Sum) / float64(total))
        latency.Avg = formatFloat(parseMetric(latency.Sum) / float64(total))
        bandwidth.Avg = formatFloat(parseMetric(bandwidth.Sum) / float64(total))
        return true, results, readIOPS, writeIOPS, latency, bandwidth, nil
    }
    return false, nil, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, pvcv1.Metrics{}, nil
}

// Helper function to parse metric strings into float64
func parseMetric(metric string) float64 {
    value, err := strconv.ParseFloat(metric, 64)
    if err != nil {
        return 0
    }
    return value
}

// Helper function to format float64 to string
func formatFloat(value float64) string {
    return strconv.FormatFloat(value, 'f', -1, 64)
}

// Helper function to calculate and format the minimum value
func formatMin(currentMin string, newValue float64) string {
    if currentMin == "" {
        return formatFloat(newValue)
    }
    currentMinVal := parseMetric(currentMin)
    if newValue < currentMinVal {
        return formatFloat(newValue)
    }
    return currentMin
}

// Helper function to calculate and format the maximum value
func formatMax(currentMax string, newValue float64) string {
    if currentMax == "" {
        return formatFloat(newValue)
    }
    currentMaxVal := parseMetric(currentMax)
    if newValue > currentMaxVal {
        return formatFloat(newValue)
    }
    return currentMax
}

// buildFioArgs transforms the CR's parameters map into fio arguments
func buildFioArgs(params map[string]string) []string {
    var args []string
    for k, v := range params {
        // e.g., { rw: read, bs: 4k } -> ["--rw=read", "--bs=4k"]
        args = append(args, fmt.Sprintf("--%s=%s", k, v))
    }
    return args
}

// (Optional) cleanupResources to delete Pods/PVCs after test
/*
func (r *PVCBenchmarkReconciler) cleanupResources(ctx context.Context, benchmark *pvcv1.PVCBenchmark) error {
    // e.g., loop and delete all pods/pvcs
    return nil
}
*/

func (r *PVCBenchmarkReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&pvcv1.PVCBenchmark{}).
        Owns(&corev1.PersistentVolumeClaim{}).
        Owns(&corev1.Pod{}).
        Complete(r)
}
