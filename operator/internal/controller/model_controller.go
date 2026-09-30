/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	maasv1alpha1 "github.com/chr1sgong/mini-maas/operator/api/v1alpha1"
)

const (
	resourceGPU       corev1.ResourceName = "nvidia.com/gpu"
	vllmCacheHostPath string              = "/data/mini-maas/vllm-cache"
)

// ModelReconciler reconciles a Model object
type ModelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=maas.chr1sgong.io,resources=models,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maas.chr1sgong.io,resources=models/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maas.chr1sgong.io,resources=models/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Model object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/reconcile
func (r *ModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logs := logf.FromContext(ctx)

	// 1. Get Model CR; Not Found -> return nil
	model := &maasv1alpha1.Model{}
	if err := r.Get(ctx, req.NamespacedName, model); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// 2. CreateOrUpdate Deployment (mutateFn: 覆盖字段 + SetControllerReference)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      parseDeploymentName(model),
			Namespace: model.Namespace,
		},
	}
	desired := buildDeployment(model)
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		// mutateFn: 把deployment改成期望状态，只修改自己管理的字段
		deployment.Labels = desired.Labels
		deployment.Spec.Strategy = desired.Spec.Strategy
		deployment.Spec.Replicas = desired.Spec.Replicas
		deployment.Spec.Template = desired.Spec.Template
		deployment.Spec.Selector = desired.Spec.Selector
		return controllerutil.SetControllerReference(model, deployment, r.Scheme)
	})
	if err != nil {
		logs.Error(err, "failed to create or update Deployment", "operation", op)
		return ctrl.Result{}, err
	}
	// 3. CreateOrUpdate Service (同理： select = app label, targetPort 用命名端口http)
	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      parseDeploymentName(model),
			Namespace: model.Namespace,
		},
	}
	op, err = controllerutil.CreateOrUpdate(ctx, r.Client, &svc, func() error {
		svc.Labels = childLabels(model)
		svc.Spec.Selector = map[string]string{
			"app": model.ObjectMeta.Name, // 与Deployment的Selector一致
		}
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "http",
				Port:       8000,
				TargetPort: intstr.FromString("http"),
			},
		}
		return controllerutil.SetControllerReference(model, &svc, r.Scheme)
	})
	if err != nil {
		logs.Error(err, "failed to create or update Service", "operation", op)
		return ctrl.Result{}, err
	}
	// 4. 读Deployment Available condition -> 推phase
	phase := derivePhase(deployment)

	// 5. list Pods (app label, Running + Ready) -> 填写endpoints
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(model.Namespace), client.MatchingLabels(map[string]string{"app": model.ObjectMeta.Name})); err != nil {
		return ctrl.Result{}, err
	}

	var endpoints []string
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				endpoints = append(endpoints, pod.Status.PodIP+":8000")
				break
			}
		}
	}
	sort.Strings(endpoints)

	// 6. phase/endpoints 变了才执行Status().Update
	if model.Status.Phase == phase && slices.Equal(model.Status.Endpoints, endpoints) {
		return ctrl.Result{}, nil
	}
	model.Status.Phase = phase
	model.Status.Endpoints = endpoints
	if err := r.Status().Update(ctx, model); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func derivePhase(deployment *appsv1.Deployment) string {
	if deployment.Status.ObservedGeneration == 0 {
		return "Pending"
	}

	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable {
			if condition.Status == corev1.ConditionTrue {
				return "Ready"
			}
		}
	}

	return "Creating"
}

// SetupWithManager sets up the controller with the Manager.
func (r *ModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&maasv1alpha1.Model{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("model").
		Complete(r)
}

func buildArgs(model *maasv1alpha1.Model) []string {
	args := []string{}
	modelName := parseModelName(model)
	args = append(args, "--model="+modelName)
	args = append(args, "--served-model-name="+model.ObjectMeta.Name)
	args = append(args, "--max-model-len="+strconv.Itoa(model.Spec.EngineConfig.MaxModelLen))
	args = append(args, "--max-num-seqs="+strconv.Itoa(model.Spec.EngineConfig.MaxNumSeqs))
	args = append(args, "--gpu-memory-utilization="+*model.Spec.Serving.GPUMemoryUtilization)
	if *model.Spec.EngineConfig.EnablePrefixCaching {
		args = append(args, "--enable-prefix-caching")
	} else {
		args = append(args, "--no-enable-prefix-caching")
	}

	return args
}

func parseModelName(model *maasv1alpha1.Model) string {
	artifactURI := model.Spec.ArtifactURI
	return strings.TrimPrefix(artifactURI, "hf://")
}

func parseDeploymentName(model *maasv1alpha1.Model) string {
	return model.Spec.Engine + "-" + model.ObjectMeta.Name
}

func buildDeployment(model *maasv1alpha1.Model) *appsv1.Deployment {
	labels := childLabels(model)
	replicas := int32(*model.Spec.Serving.Replicas)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      parseDeploymentName(model),
			Namespace: model.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": model.ObjectMeta.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            model.Spec.Engine, // 目前只有vllm
							Image:           model.Spec.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args:            buildArgs(model),
							Env: []corev1.EnvVar{
								{
									Name:  "HF_ENDPOINT", // 国内镜像加速
									Value: "https://hf-mirror.com",
								},
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 8000,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("16Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("32Gi"),
									resourceGPU:           resource.MustParse("1"),
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: intstr.FromString("http"),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
								FailureThreshold:    90,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "vllm-cache",
									MountPath: "/root/.cache/vllm",
								},
								{
									Name:      "hf-cache",
									MountPath: "/root/.cache/huggingface",
								},
								{
									Name:      "shm",
									MountPath: "/dev/shm",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "vllm-cache",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: vllmCacheHostPath,
									Type: ptr.To(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: "hf-cache",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/data/mini-maas/hf-cache",
									Type: ptr.To(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: "shm",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{
									Medium:    corev1.StorageMediumMemory,
									SizeLimit: ptr.To(resource.MustParse("8Gi")),
								},
							},
						},
					},
				},
			},
		},
	}
}

func childLabels(model *maasv1alpha1.Model) map[string]string {
	return map[string]string{
		"app":                          model.ObjectMeta.Name,
		"app.kubernetes.io/managed-by": "maas-operator", // 资源区分，故障排查用
		"maas.chr1sgong.io/model":      model.ObjectMeta.Name,
	}
}
