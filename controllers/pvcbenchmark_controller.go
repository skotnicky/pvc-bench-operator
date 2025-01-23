package controllers

import (
    "context"
    "fmt"
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

//+kubebuilder:rbac:groups=benchmarking.pvcbenchmarking.k8s.io,resources=pvcbenchmarks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=benchmarking.pvcbenchmarking.k8s.io,resources=pvcbenchmarks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=benchmarking.pvcbenchmarking.k8s.io,resources=pvcbenchmarks/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims;pods;events,verbs=get;list;watch;create;update;patch;delete

func (r *PVCBenchmarkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    logger := log.FromContext(ctx)

    // Fetch the PVCBenchmark instance
    var benchmark pvcv1.PVCBenchmark
    if err := r.Get(ctx, req.NamespacedName, &benchmark); err != nil {
        if errors.IsNotFound(err) {
            // The CR is deleted or not found, no action needed
            return ctrl.Result{}, nil
        }
        // Other error
        return ctrl.Result{}, err
    }

    // If the CR is marked for deletion, handle finalizers if needed
    if !benchmark.ObjectMeta.DeletionTimestamp.IsZero() {
        // perform cleanup if needed
        return ctrl.Result{}, nil
    }

    // Initialize status if not set
    if benchmark.Status.Phase == "" {
        benchmark.Status.Phase = "Pending"
        if err := r.Status().Update(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to update initial status")
            return ctrl.Result{}, err
        }
    }

    // 1. Ensure the required PVCs exist
    if err := r.ensurePVCs(ctx, &benchmark); err != nil {
        logger.Error(err, "Failed to ensure PVCs")
        return ctrl.Result{}, err
    }

    // 2. Ensure benchmarking pods exist
    if err := r.ensureBenchmarkPods(ctx, &benchmark); err != nil {
        logger.Error(err, "Failed to ensure benchmarking pods")
        return ctrl.Result{}, err
    }

    // 3. Check pod statuses & collect results if all are complete
    completed, results, err := r.checkAndCollectResults(ctx, &benchmark)
    if err != nil {
        logger.Error(err, "Error collecting results")
        return ctrl.Result{}, err
    }
    if completed {
        // Update the status with results
        benchmark.Status.Phase = "Completed"
        benchmark.Status.Results = results
        if err := r.Status().Update(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to update status to Completed")
            return ctrl.Result{}, err
        }

        // Optional: Cleanup resources (PVCs & Pods) if you want ephemeral usage
        // err := r.cleanupResources(ctx, &benchmark)
        // if err != nil {
        //     return ctrl.Result{}, err
        // }
        // logger.Info("Cleaned up all resources")

        return ctrl.Result{}, nil
    }

    // Not all pods are complete; requeue after 10s to check again
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
            // Need to create the PVC
            newPVC := corev1.PersistentVolumeClaim{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      pvcName,
                    Namespace: benchmark.Namespace,
                },
                Spec: corev1.PersistentVolumeClaimSpec{
                    AccessModes: []corev1.PersistentVolumeAccessMode{
                        corev1.PersistentVolumeAccessMode(benchmark.Spec.PVC.AccessMode),
                    },
                    Resources: corev1.ResourceRequirements{
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

            // Make PVC owned by the CR
            if err := controllerutil.SetControllerReference(benchmark, &newPVC, r.Scheme()); err != nil {
                return err
            }

            if err := r.Create(ctx, &newPVC); err != nil {
                return err
            }
        } else if err != nil {
            // Other error
            return err
        }
        // If PVC found, do nothing
    }
    return nil
}

// ensureBenchmarkPods creates or ensures the existence of Pods that run the benchmarking tool
func (r *PVCBenchmarkReconciler) ensureBenchmarkPods(ctx context.Context, benchmark *pvcv1.PVCBenchmark) error {
    for i := 0; i < benchmark.Spec.Scale.PVCCount; i++ {
        podName := fmt.Sprintf("%s-bench-%d", benchmark.Name, i)
        pvcName := fmt.Sprintf("%s-pvc-%d", benchmark.Name, i)

        var pod corev1.Pod
        err := r.Get(ctx, types.NamespacedName{
            Namespace: benchmark.Namespace,
            Name:      podName,
        }, &pod)
        if err != nil && errors.IsNotFound(err) {
            // Construct Pod that runs fio with the given parameters
            fioArgs := buildFioArgs(benchmark.Spec.Test.Parameters)
            // Optionally add duration if specified
            if benchmark.Spec.Test.Duration != "" {
                fioArgs = append(fioArgs, fmt.Sprintf("--runtime=%s", benchmark.Spec.Test.Duration))
                fioArgs = append(fioArgs, "--time_based")
            }
            fioArgs = append(fioArgs, "--directory=/mnt/storage")
            fioArgs = append(fioArgs, "--output-format=json") // easier to parse if you want logs

            newPod := corev1.Pod{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      podName,
                    Namespace: benchmark.Namespace,
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
                            Image:   "ghcr.io/your-org/fio:latest", // Replace with your actual fio image
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

            // Make Pod owned by the CR
            if err := controllerutil.SetControllerReference(benchmark, &newPod, r.Scheme()); err != nil {
                return err
            }

            if err := r.Create(ctx, &newPod); err != nil {
                return err
            }
        } else if err != nil {
            // Other error
            return err
        }
        // If Pod found, do nothing
    }
    return nil
}

// checkAndCollectResults checks whether all Pods are completed and aggregates results
func (r *PVCBenchmarkReconciler) checkAndCollectResults(ctx context.Context, benchmark *pvcv1.PVCBenchmark) (bool, map[string]string, error) {
    results := make(map[string]string)
    completedCount := 0
    total := benchmark.Spec.Scale.PVCCount

    for i := 0; i < total; i++ {
        podName := fmt.Sprintf("%s-bench-%d", benchmark.Name, i)
        var pod corev1.Pod
        err := r.Get(ctx, types.NamespacedName{
            Namespace: benchmark.Namespace,
            Name:      podName,
        }, &pod)
        if err != nil {
            return false, nil, err
        }

        switch pod.Status.Phase {
        case corev1.PodSucceeded, corev1.PodFailed:
            // Pod is done
            completedCount++
            // (Optional) Retrieve logs to parse results
            // logs, logErr := r.getPodLogs(ctx, pod)
            // if logErr == nil {
            //     results[podName] = logs
            // } else {
            //     results[podName] = fmt.Sprintf("Error reading logs: %v", logErr)
            // }
            // For now, just store the phase
            results[podName] = string(pod.Status.Phase)

        default:
            // Pod is still Running or Pending
        }
    }

    if completedCount == total {
        return true, results, nil
    }
    return false, nil, nil
}

// buildFioArgs transforms the CR's parameters map into fio arguments
func buildFioArgs(params map[string]string) []string {
    var args []string
    for k, v := range params {
        // e.g., {rw: read, bs: 4k} -> ["--rw=read", "--bs=4k"]
        args = append(args, fmt.Sprintf("--%s=%s", k, v))
    }
    return args
}

// (Optional) gather logs from the Pod to parse fio JSON
/* 
func (r *PVCBenchmarkReconciler) getPodLogs(ctx context.Context, pod corev1.Pod) (string, error) {
    req := r.Clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
    stream, err := req.Stream(ctx)
    if err != nil {
        return "", err
    }
    defer stream.Close()
    
    buf, err := io.ReadAll(stream)
    if err != nil {
        return "", err
    }
    return string(buf), nil
}
*/

// (Optional) cleanup after completion
/*
func (r *PVCBenchmarkReconciler) cleanupResources(ctx context.Context, benchmark *pvcv1.PVCBenchmark) error {
    // Delete all Pods and PVCs
    // ...
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
