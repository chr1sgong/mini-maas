package autoscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var modelGVR = schema.GroupVersionResource{
	Group:    "maas.chr1sgong.io",
	Version:  "v1alpha1",
	Resource: "models",
}

func NewDynamicClient(kubeconfig string) (dynamic.Interface, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return dynamic.NewForConfig(cfg)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}

	return dynamic.NewForConfig(cfg)
}

type Actuator struct {
	cli       dynamic.Interface
	namespace string
	model     string
	dryRun    bool
}

func NewActuator(cli dynamic.Interface, namespace, model string, dryRun bool) *Actuator {
	return &Actuator{
		cli:       cli,
		namespace: namespace,
		model:     model,
		dryRun:    dryRun,
	}
}

func (a *Actuator) CurrentReplicas(ctx context.Context) (int, error) {
	m, err := a.cli.Resource(modelGVR).Namespace(a.namespace).Get(ctx, a.model, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("get model %s/%s: %w", a.namespace, a.model, err)
	}

	r, found, err := unstructured.NestedInt64(m.Object, "spec", "serving", "replicas")
	if err != nil || !found {
		return 0, fmt.Errorf("spec.serving.replicas not found on model %s/%s", a.namespace, a.model)
	}

	return int(r), nil
}

func (a *Actuator) PatchReplicas(ctx context.Context, target int, reason string) error {
	if a.dryRun {
		return nil
	}
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"serving": map[string]any{
				"replicas": target,
			},
		},
		"metadata": map[string]any{
			"annotations": map[string]any{
				"maas.chr1sgong.io/autoscale-reason": reason,
			}},
	})
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		_, err = a.cli.Resource(modelGVR).Namespace(a.namespace).Patch(ctx, a.model, types.MergePatchType, patch, metav1.PatchOptions{})
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
			// do nothing, just wait
		}
	}
	return fmt.Errorf("patch model %s/%s failed: %w", a.namespace, a.model, lastErr)
}
