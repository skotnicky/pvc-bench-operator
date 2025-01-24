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

//+kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims;pods;events,verbs=get;list;watch;create;update;patch;delete

func (r *PVCBenchmarkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the PVCBenchmark instance
	var benchmark pvcv1.PVCBenchmark
	if err := r.Get(ctx, req.NamespacedName, &benchmark); err != nil {
		if errors.IsNotFound(err) {
			// The CR is gone; nothing to do
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Check if being deleted/finalizers
	if !benchmark.ObjectMeta.DeletionTimestamp.IsZero() {
		// handle finalizer/cleanup if needed
		return ctrl.Result{}, nil
	}

	// 3. Initialize status if empty
	if benchmark.Status.Phase == "" {
		benchmark.Status.Phase = "Pending"
		if err := r.Status().Update(ctx, &benchmark); err != nil {
			logger.Error(err, "Failed to update initial status")
			return ctrl.Result{}, err
		}
	}

	// 4. Ensure the required PVCs exist
	if err := r.ensurePVCs(ctx, &benchmark); err != nil {
		logger.Error(err, "Failed to ensure PVCs")
		return ctrl.Result{}, err
	}

	// 5. Ensure benchmarking Pods exist
	if err := r.ensureBenchmarkPods(ctx, &benchmark); err != nil {
		logger.Error(err, "Failed to ensure benchmark pods")
		return ctrl.Result{}, err
	}

	// 6. Check if Pods completed & gather results
	completed, results, err := r.checkAndCollectResults(ctx, &benchmark)
	if err != nil {
		logger.Error(err, "Error collecting results")
		return ctrl.Result{}, err
	}
	if completed {
		benchmark.Status.Phase = "Completed"
		benchmark.Status.Results = results
		if err := r.Status().Update(ctx, &benchmark); err != nil {
			logger.Error(err, "Failed to update status to Completed")
			return ctrl.Result{}, err
		}

		// (Optional) Cleanup resources if ephemeral usage is desired
		// r.cleanupResources(ctx, &benchmark)

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
		}, &pod)
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
func (r *PVCBenchmarkReconciler) checkAndCollectResults(ctx context.Context, benchmark *pvcv1.PVCBenchmark) (bool, map[string]string, error) {
	results := make(map[string]string)
	completedCount := 0
	total := benchmark.Spec.Scale.PVCCount

	for i := 0; i < total; i++ {
		podName := fmt.Sprintf("%s-bench-%d", benchmark.Name, i)
		var pod corev1.Pod
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: benchmark.Namespace,
			Name:      podName,
		}, &pod); err != nil {
			return false, nil, err
		}

		// If Pod is succeeded/failed, it's done
		switch pod.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			completedCount++
			// Minimal example: store Pod phase in results
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
