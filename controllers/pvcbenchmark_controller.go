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
    pvcv1 "github.com/skotnicky/pvc-bench-operator/api/v1"
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
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch

// PVCBenchmarkReconciler reconciles a PVCBenchmark object
type PVCBenchmarkReconciler struct {
    client.Client
    Log logr.Logger
}

// Reconcile implements the main loop
func (r *PVCBenchmarkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    logger := log.FromContext(ctx)

    // 1) Fetch PVCBenchmark
    var benchmark pvcv1.PVCBenchmark
    if err := r.Get(ctx, req.NamespacedName, &benchmark); err != nil {
        if errors.IsNotFound(err) {
            // CR is deleted
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, err
    }

    // If CR is being deleted, handle finalizers if needed
    if !benchmark.ObjectMeta.DeletionTimestamp.IsZero() {
        return ctrl.Result{}, nil
    }

    // Initialize .status.phase if empty
    if benchmark.Status.Phase == "" {
        benchmark.Status.Phase = "Pending"
        if err := r.Status().Update(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to set initial phase=Pending")
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

    // 3.5) Check if all init containers "wait-for-other-pods" are running => set configmap if so
    allStarted, err := r.checkAllInitContainersRunning(ctx, &benchmark)
    if err != nil {
        logger.Error(err, "Failed to check init container readiness")
        return ctrl.Result{}, err
    }
    if allStarted {
        if err := r.updateInitReadyConfigMap(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to set init_ready in configmap")
            return ctrl.Result{}, err
        }
    }

    // 4) Check if benchmark pods completed => parse logs
    completed, results,
    readIOPS, writeIOPS,
    readLat, writeLat,
    readBW, writeBW,
    cpu,
    err := r.checkAndCollectResults(ctx, &benchmark)
    if err != nil {
        logger.Error(err, "Error collecting results")
        return ctrl.Result{}, err
    }

    if completed {
        // All pods done => update CR status
        benchmark.Status.Phase = "Completed"
        benchmark.Status.Results = results
        benchmark.Status.ReadIOPS = readIOPS
        benchmark.Status.WriteIOPS = writeIOPS
        benchmark.Status.ReadLatency = readLat
        benchmark.Status.WriteLatency = writeLat
        benchmark.Status.ReadBandwidth = readBW
        benchmark.Status.WriteBandwidth = writeBW
        benchmark.Status.CPUUsage = cpu

        if err := r.Status().Update(ctx, &benchmark); err != nil {
            logger.Error(err, "Failed to update status=Completed")
            return ctrl.Result{}, err
        }
        return ctrl.Result{}, nil
    }

    // Not completed => requeue
    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// checkAllInitContainersRunning => if each pod has init container "wait-for-other-pods" in state.running
func (r *PVCBenchmarkReconciler) checkAllInitContainersRunning(ctx context.Context, bench *pvcv1.PVCBenchmark) (bool, error) {
    var podList corev1.PodList
    if err := r.List(ctx, &podList,
        client.InNamespace(bench.Namespace),
        client.MatchingLabels{"app": "pvc-bench-fio"}); err != nil {
        return false, err
    }

    if len(podList.Items) < bench.Spec.Scale.PVCCount {
        // Not all pods created yet
        return false, nil
    }

    startedCount := 0
    for _, pod := range podList.Items {
        // find init container "wait-for-other-pods"
        for _, initStatus := range pod.Status.InitContainerStatuses {
            if initStatus.Name == "wait-for-other-pods" {
                if initStatus.State.Running != nil {
                    startedCount++
                }
                break
            }
        }
    }
    if startedCount == bench.Spec.Scale.PVCCount {
        return true, nil
    }
    return false, nil
}

// updateInitReadyConfigMap => sets key "init_ready=true" in configmap
func (r *PVCBenchmarkReconciler) updateInitReadyConfigMap(ctx context.Context, bench *pvcv1.PVCBenchmark) error {
    cmName := "init-ready-cm"
    cmNs := bench.Namespace

    var cm corev1.ConfigMap
    if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: cmNs}, &cm); err != nil {
        if errors.IsNotFound(err) {
            // create
            newCM := corev1.ConfigMap{
                ObjectMeta: metav1.ObjectMeta{
                    Name: cmName,
                    Namespace: cmNs,
                },
                Data: map[string]string{
                    "init_ready": "true",
                },
            }
            return r.Create(ctx, &newCM)
        }
        return err
    }

    // if found, just update
    cm.Data["init_ready"] = "true"
    return r.Update(ctx, &cm)
}

// ensurePVCs => your existing logic
func (r *PVCBenchmarkReconciler) ensurePVCs(ctx context.Context, bench *pvcv1.PVCBenchmark) error {
    pvcCount := bench.Spec.Scale.PVCCount
    for i := 0; i < pvcCount; i++ {
        pvcName := fmt.Sprintf("%s-pvc-%d", bench.Name, i)

        var pvc corev1.PersistentVolumeClaim
        err := r.Get(ctx, types.NamespacedName{
            Namespace: bench.Namespace,
            Name:      pvcName,
        }, &pvc)
        if err != nil && errors.IsNotFound(err) {
            newPVC := corev1.PersistentVolumeClaim{
                ObjectMeta: metav1.ObjectMeta{
                    Name: pvcName,
                    Namespace: bench.Namespace,
                    Labels: map[string]string{
                        "app": "pvc-bench-fio",
                    },
                },
                Spec: corev1.PersistentVolumeClaimSpec{
                    AccessModes: []corev1.PersistentVolumeAccessMode{
                        corev1.PersistentVolumeAccessMode(bench.Spec.PVC.AccessMode),
                    },
                    Resources: corev1.VolumeResourceRequirements{
                        Requests: corev1.ResourceList{
                            corev1.ResourceStorage: resource.MustParse(bench.Spec.PVC.Size),
                        },
                    },
                },
            }
            if bench.Spec.PVC.StorageClassName != nil {
                newPVC.Spec.StorageClassName = bench.Spec.PVC.StorageClassName
            }
            // set ownership
            if err := controllerutil.SetControllerReference(bench, &newPVC, r.Scheme()); err != nil {
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

// ensureBenchmarkPods => each Pod has an initContainer that just waits for "init_ready" in configmap
func (r *PVCBenchmarkReconciler) ensureBenchmarkPods(ctx context.Context, bench *pvcv1.PVCBenchmark) error {
    podCount := bench.Spec.Scale.PVCCount
    for i := 0; i < podCount; i++ {
        podName := fmt.Sprintf("%s-bench-%d", bench.Name, i)
        pvcName := fmt.Sprintf("%s-pvc-%d", bench.Name, i)

        var pod corev1.Pod
        err := r.Get(ctx, types.NamespacedName{Namespace: bench.Namespace, Name: podName}, &pod)
        if err != nil && errors.IsNotFound(err) {
            // build fio args
            fioArgs := buildFioArgs(bench.Spec.Test.Parameters)
            if bench.Spec.Test.Duration != "" {
                fioArgs = append(fioArgs, fmt.Sprintf("--runtime=%s", bench.Spec.Test.Duration), "--time_based")
            }
            fioArgs = append(fioArgs,
                "--directory=/mnt/storage",
                "--output-format=json",
                "--name=benchtest",
                "--filename=/mnt/storage/testfile",
                "--group_reporting",
            )

            newPod := corev1.Pod{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      podName,
                    Namespace: bench.Namespace,
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
                        {	
			    Name: "init-config-vol",
        		    VolumeSource: corev1.VolumeSource{
       			        ConfigMap: &corev1.ConfigMapVolumeSource{
            			    LocalObjectReference: corev1.LocalObjectReference{
               			       Name: "init-ready-cm",
			                },
            		    Optional: func() *bool { b := true; return &b }(),
				  },
			     },
   			 },
                    },
                    InitContainers: []corev1.Container{
                        {
                            Name:  "wait-for-other-pods",
                            Image: "alpine:3.17",
                            Command: []string{"/bin/sh"},
                            Args: []string{
                                "-c",
                                `
                                while true; do
                                  val=$(cat /etc/initcfg/init_ready 2>/dev/null || echo "false")
                                  if [ "$val" = "true" ]; then
                                    echo "Operator signaled all init containers started => continuing..."
                                    break
                                  fi
                                  echo "Waiting for operator to set init_ready=true in configmap..."
                                  sleep 5
                                done
                                `,
                            },
                            VolumeMounts: []corev1.VolumeMount{
                                {
                                    Name:      "init-config-vol",
                                    MountPath: "/etc/initcfg",
                                    ReadOnly:  true,
                                },
                            },
                        },
                    },
                    Containers: []corev1.Container{
                        {
                            Name:    "fio-benchmark",
                            Image:   "ghcr.io/skotnicky/pvc-bench-operator/fio:latest",
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
                    TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
                        {
                            MaxSkew: 1,
                            TopologyKey: "kubernetes.io/hostname",
                            WhenUnsatisfiable: corev1.ScheduleAnyway,
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
                },
            }

            // set ownership
            if err := controllerutil.SetControllerReference(bench, &newPod, r.Scheme()); err != nil {
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

// checkAndCollectResults => aggregator of read/write metrics
func (r *PVCBenchmarkReconciler) checkAndCollectResults(
    ctx context.Context,
    bench *pvcv1.PVCBenchmark,
) (bool,
    map[string]string,
    pvcv1.Metrics, pvcv1.Metrics,
    pvcv1.Metrics, pvcv1.Metrics,
    pvcv1.Metrics, pvcv1.Metrics,
    pvcv1.Metrics,
    error,
) {
    results := make(map[string]string)
    completedCount := 0
    total := bench.Spec.Scale.PVCCount

    readIopsAgg := newStatAgg()
    writeIopsAgg := newStatAgg()

    readLatAgg := newStatAgg()
    writeLatAgg := newStatAgg()

    readBwAgg := newStatAgg()
    writeBwAgg := newStatAgg()

    cpuAgg := newStatAgg()

    // Build clientset for logs
    config, err := rest.InClusterConfig()
    if err != nil {
        return false, nil,
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{},
            err
    }
    clientset, err := kubernetes.NewForConfig(config)
    if err != nil {
        return false, nil,
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{}, pvcv1.Metrics{},
            pvcv1.Metrics{},
            err
    }

    for i := 0; i < total; i++ {
        podName := fmt.Sprintf("%s-bench-%d", bench.Name, i)
        var pod corev1.Pod
        if err := r.Get(ctx, types.NamespacedName{
            Namespace: bench.Namespace,
            Name:      podName,
        }, &pod); err != nil {
            return false, nil,
                pvcv1.Metrics{}, pvcv1.Metrics{},
                pvcv1.Metrics{}, pvcv1.Metrics{},
                pvcv1.Metrics{}, pvcv1.Metrics{},
                pvcv1.Metrics{},
                err
        }

        switch pod.Status.Phase {
        case corev1.PodSucceeded, corev1.PodFailed:
            completedCount++
            logs, logErr := getPodLogs(clientset, bench.Namespace, podName)
            if logErr != nil {
                results[podName] = fmt.Sprintf("Error reading logs: %v", logErr)
                continue
            }
            var fioData FioJSON
            parseErr := json.Unmarshal([]byte(logs), &fioData)
            if parseErr != nil {
                results[podName] = logs
                continue
            }

            if len(fioData.Jobs) > 0 {
                j := fioData.Jobs[0]
                // read
                readIopsVal := j.Read.Iops
                readBwVal := float64(j.Read.Bw) / 1024.0
                readLatVal := j.Read.ClatNs.Mean / 1_000_000.0
                // write
                writeIopsVal := j.Write.Iops
                writeBwVal := float64(j.Write.Bw) / 1024.0
                writeLatVal := j.Write.ClatNs.Mean / 1_000_000.0
                // CPU
                cpuVal := j.UsrCpu + j.SysCpu

                // aggregator
                readIopsAgg.Add(readIopsVal)
                writeIopsAgg.Add(writeIopsVal)
                readBwAgg.Add(readBwVal)
                writeBwAgg.Add(writeBwVal)
                readLatAgg.Add(readLatVal)
                writeLatAgg.Add(writeLatVal)
                cpuAgg.Add(cpuVal)

                results[podName] = fmt.Sprintf(
                    "READ => IOPS=%.2f, Lat=%.2f ms, BW=%.2f MB/s; WRITE => IOPS=%.2f, Lat=%.2f ms, BW=%.2f MB/s; CPU=%.2f",
                    readIopsVal, readLatVal, readBwVal,
                    writeIopsVal, writeLatVal, writeBwVal,
                    cpuVal,
                )
            } else {
                results[podName] = logs
            }
        }
    }

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
        cpuUsage := pvcv1.Metrics{
            Min: formatFloat(cpuAgg.Min),
            Max: formatFloat(cpuAgg.Max),
            Sum: formatFloat(cpuAgg.Sum),
            Avg: formatFloat(cpuAgg.Avg()),
        }
        return true, results, readIOPS, writeIOPS, readLat, writeLat, readBw, writeBw, cpuUsage, nil
    }

    return false, results,
        pvcv1.Metrics{}, pvcv1.Metrics{},
        pvcv1.Metrics{}, pvcv1.Metrics{},
        pvcv1.Metrics{}, pvcv1.Metrics{},
        pvcv1.Metrics{},
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

// getPodLogs => read logs from container "fio-benchmark"
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

func (r *PVCBenchmarkReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&pvcv1.PVCBenchmark{}).
        Owns(&corev1.PersistentVolumeClaim{}).
        Owns(&corev1.Pod{}).
        Complete(r)
}

// Helper aggregator
type StatAgg struct {
    Min   float64
    Max   float64
    Sum   float64
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

// FioJSON => parse FIO --output-format=json
type FioJSON struct {
    Jobs []struct {
        Jobname string `json:"jobname"`
        Read struct {
            Bw   int64   `json:"bw"`    // KB/s
            Iops float64 `json:"iops"`
            ClatNs struct {
                Mean float64 `json:"mean"`
            } `json:"lat_ns"`
        } `json:"read"`
        Write struct {
            Bw   int64   `json:"bw"`
            Iops float64 `json:"iops"`
            ClatNs struct {
                Mean float64 `json:"mean"`
            } `json:"lat_ns"`
        } `json:"write"`
        UsrCpu float64 `json:"usr_cpu"`
        SysCpu float64 `json:"sys_cpu"`
    } `json:"jobs"`
}
