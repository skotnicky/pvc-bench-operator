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
    pvcv1 "github.com/skotnicky/pvc-bench-operator/api/v1" // Adjust path
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

// +kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=benchmarking.taikun.cloud,resources=pvcbenchmarks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces;pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims;pods;events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get

// PVCBenchmarkReconciler reconciles a PVCBenchmark object
type PVCBenchmarkReconciler struct {
    client.Client
    Log logr.Logger
}

func (r *PVCBenchmarkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    logger := log.FromContext(ctx)

    // 1) Fetch PVCBenchmark
    var benchmark pvcv1.PVCBenchmark
    if err := r.Get(ctx, req.NamespacedName, &benchmark); err != nil {
        if errors.IsNotFound(err) {
            // CR is gone
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, err
    }

    // If CR is being deleted
    if !benchmark.ObjectMeta.DeletionTimestamp.IsZero() {
        // handle finalizers or cleanup if needed
        return ctrl.Result{}, nil
    }

    // Initialize status if empty
    if benchmark.Status.Phase == "" {
        benchmark.Status.Phase = "Pending"
        if err := r.Status().Update(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to set phase=Pending")
            return ctrl.Result{}, err
        }
    }

    // 2) Ensure PVCs
    if err := r.ensurePVCs(ctx, &benchmark); err != nil {
        logger.Error(err, "Failed to ensure PVCs")
        return ctrl.Result{}, err
    }

    // 3) Ensure Pods
    if err := r.ensureBenchmarkPods(ctx, &benchmark); err != nil {
        logger.Error(err, "Failed to ensure benchmark pods")
        return ctrl.Result{}, err
    }

    // 4) Check if pods completed; parse logs
    completed, results,
    readIOPS, writeIOPS,
    readLat, writeLat,
    readBW, writeBW,
    err := r.checkAndCollectResults(ctx, &benchmark)
    if err != nil {
        logger.Error(err, "Error collecting results")
        return ctrl.Result{}, err
    }

    if completed {
        // All pods done → update status with aggregated results
        benchmark.Status.Phase = "Completed"
        benchmark.Status.Results = results

        benchmark.Status.ReadIOPS = readIOPS
        benchmark.Status.WriteIOPS = writeIOPS
        benchmark.Status.ReadLatency = readLat
        benchmark.Status.WriteLatency = writeLat
        benchmark.Status.ReadBandwidth = readBW
        benchmark.Status.WriteBandwidth = writeBW

        if err := r.Status().Update(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to update CR status=Completed")
            return ctrl.Result{}, err
        }
        return ctrl.Result{}, nil
    }

    // If not completed, requeue in 10s
    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// ensurePVCs creates N PVCs
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
    }
    return nil
}

// ensureBenchmarkPods creates Pods that run fio
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
            fioArgs := buildFioArgs(benchmark.Spec.Test.Parameters)
            if benchmark.Spec.Test.Duration != "" {
                fioArgs = append(fioArgs, fmt.Sprintf("--runtime=%s", benchmark.Spec.Test.Duration), "--time_based")
            }
            fioArgs = append(fioArgs,
	    	"--directory=/mnt/storage",
                "--output-format=json",
                "--name=benchtest",
                "--filename=/mnt/storage/testfile",
		"--group_reporting",
            )
	    readinessCheckArgs := []string{
                benchmark.Namespace,
                "app=pvc-bench-fio",
                strconv.Itoa(benchmark.Spec.Scale.PVCCount),
	    }
	    readinessCheckArgs = append(readinessCheckArgs, fioArgs...)

            newPod := corev1.Pod{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      podName,
                    Namespace: benchmark.Namespace,
                    Labels: map[string]string{
                        "app": "pvc-bench-fio",
                    },
                },
                Spec: corev1.PodSpec{
			ServiceAccountName: "pvc-bench-operator-controller-manager",
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
                            Image:   "ghcr.io/skotnicky/pvc-bench-operator/fio:latest",
			    Command: []string{"/usr/local/bin/check_pods_ready.sh"},
                            Args: readinessCheckArgs,
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
    }
    return nil
}

// checkAndCollectResults looks at each Pod, fetches logs, parses read+write metrics
func (r *PVCBenchmarkReconciler) checkAndCollectResults(
    ctx context.Context,
    benchmark *pvcv1.PVCBenchmark,
) (bool,
    map[string]string,
    pvcv1.Metrics, pvcv1.Metrics,  // readIOPS, writeIOPS
    pvcv1.Metrics, pvcv1.Metrics,  // readLat, writeLat
    pvcv1.Metrics, pvcv1.Metrics,  // readBW, writeBW
    error,
) {

    results := make(map[string]string)
    completedCount := 0
    total := benchmark.Spec.Scale.PVCCount

    // Aggregators for read/write IOPS, lat, BW
    readIopsAgg := newStatAgg()
    writeIopsAgg := newStatAgg()

    readLatAgg := newStatAgg()
    writeLatAgg := newStatAgg()

    readBwAgg := newStatAgg()
    writeBwAgg := newStatAgg()

    // Build a clientset for logs
    config, err := rest.InClusterConfig()
    if err != nil {
        return false, nil,
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{}, pvcv1.Metrics{},
            err
    }
    clientset, err := kubernetes.NewForConfig(config)
    if err != nil {
        return false, nil,
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{}, pvcv1.Metrics{},
            err
    }

    for i := 0; i < total; i++ {
        podName := fmt.Sprintf("%s-bench-%d", benchmark.Name, i)
        var pod corev1.Pod
        if err := r.Get(ctx, types.NamespacedName{
            Namespace: benchmark.Namespace,
            Name:      podName,
        }, &pod); err != nil {
            return false, nil,
                pvcv1.Metrics{}, pvcv1.Metrics{},
                pvcv1.Metrics{}, pvcv1.Metrics{},
                pvcv1.Metrics{}, pvcv1.Metrics{},
                err
        }

        switch pod.Status.Phase {
        case corev1.PodSucceeded, corev1.PodFailed:
            completedCount++
            logs, logErr := getPodLogs(clientset, benchmark.Namespace, podName)
            if logErr != nil {
                results[podName] = fmt.Sprintf("Error reading logs: %v", logErr)
                continue
            }
            var fioData FioJSON
            parseErr := json.Unmarshal([]byte(logs), &fioData)
            if parseErr != nil {
                // fallback to raw logs if parse fails
                results[podName] = string(logs)
                continue
            }

            if len(fioData.Jobs) > 0 {
                j := fioData.Jobs[0]

                // read metrics
                readIopsVal := j.Read.Iops
                readBwVal := float64(j.Read.Bw) / 1024.0           // KB/s → MB/s
                readLatVal := j.Read.ClatNs.Mean / 1_000_000.0     // ns → ms

                // write metrics
                writeIopsVal := j.Write.Iops
                writeBwVal := float64(j.Write.Bw) / 1024.0
                writeLatVal := j.Write.ClatNs.Mean / 1_000_000.0

                // Update aggregator
                readIopsAgg.Add(readIopsVal)
                writeIopsAgg.Add(writeIopsVal)

                readBwAgg.Add(readBwVal)
                writeBwAgg.Add(writeBwVal)

                readLatAgg.Add(readLatVal)
                writeLatAgg.Add(writeLatVal)

                // Per-pod text summary
                results[podName] = fmt.Sprintf(
                    "READ => IOPS=%.2f, Lat=%.2f ms, BW=%.2f MB/s; WRITE => IOPS=%.2f, Lat=%.2f ms, BW=%.2f MB/s",
                    readIopsVal, readLatVal, readBwVal,
                    writeIopsVal, writeLatVal, writeBwVal,
                )
            } else {
                results[podName] = string(logs)
            }
        }
    }

    if completedCount == total {
        // Convert each aggregator to pvcv1.Metrics
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
        readLat := pvcv1.Metrics{
            Min: formatFloat(readLatAgg.Min),
            Max: formatFloat(readLatAgg.Max),
            Sum: formatFloat(readLatAgg.Sum),
            Avg: formatFloat(readLatAgg.Avg()),
        }
        writeLat := pvcv1.Metrics{
            Min: formatFloat(writeLatAgg.Min),
            Max: formatFloat(writeLatAgg.Max),
            Sum: formatFloat(writeLatAgg.Sum),
            Avg: formatFloat(writeLatAgg.Avg()),
        }
        readBw := pvcv1.Metrics{
            Min: formatFloat(readBwAgg.Min),
            Max: formatFloat(readBwAgg.Max),
            Sum: formatFloat(readBwAgg.Sum),
            Avg: formatFloat(readBwAgg.Avg()),
        }
        writeBw := pvcv1.Metrics{
            Min: formatFloat(writeBwAgg.Min),
            Max: formatFloat(writeBwAgg.Max),
            Sum: formatFloat(writeBwAgg.Sum),
            Avg: formatFloat(writeBwAgg.Avg()),
        }
        return true, results, readIOPS, writeIOPS, readLat, writeLat, readBw, writeBw, nil
    }

    return false, results,
        pvcv1.Metrics{}, pvcv1.Metrics{},
        pvcv1.Metrics{}, pvcv1.Metrics{},
        pvcv1.Metrics{}, pvcv1.Metrics{},
        nil
}

// buildFioArgs transforms CR's TestSpec parameters into FIO flags
func buildFioArgs(params map[string]string) []string {
    var args []string
    for k, v := range params {
        args = append(args, fmt.Sprintf("--%s=%s", k, v))
    }
    return args
}

// getPodLogs obtains container logs from "fio-benchmark"
func getPodLogs(clientset *kubernetes.Clientset, namespace, podName string) (string, error) {
    opts := &corev1.PodLogOptions{
        Container: "fio-benchmark",
    }
    req := clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
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

// SetupWithManager sets up the controller with Manager
func (r *PVCBenchmarkReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&pvcv1.PVCBenchmark{}).
        Owns(&corev1.PersistentVolumeClaim{}).
        Owns(&corev1.Pod{}).
        Complete(r)
}

// StatAgg aggregates min, max, sum, count for a float64
type StatAgg struct {
    Min float64
    Max float64
    Sum float64
    Count int
}
func newStatAgg() StatAgg {
    return StatAgg{
        Min: math.MaxFloat64,
        Max: 0,
        Sum: 0,
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

// FioJSON matches your FIO --output-format=json structure
type FioJSON struct {
    Jobs []struct {
        Jobname string `json:"jobname"`
        Read struct {
            Bw   int64   `json:"bw"`    // KB/s
            Iops float64 `json:"iops"`
            ClatNs struct {
                Mean float64 `json:"mean"`
            } `json:"clat_ns"`
        } `json:"read"`
        Write struct {
            Bw   int64   `json:"bw"`
            Iops float64 `json:"iops"`
            ClatNs struct {
                Mean float64 `json:"mean"`
            } `json:"clat_ns"`
        } `json:"write"`
    } `json:"jobs"`
}
