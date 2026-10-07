package routetable

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// GVR 是model CRD在API层面唯一寻址方式
var modelGVR = schema.GroupVersionResource{
	Group:    "maas.chr1sgong.io",
	Version:  "v1alpha1",
	Resource: "models",
}

func NewDynamicClient(kubeconfig string) (dynamic.Interface, error) {
	// case1 集群内运行: ServiceAccount token自动注入,直接可用
	if cfg, err := rest.InClusterConfig(); err == nil {
		return dynamic.NewForConfig(cfg)
	}

	// case2 集群外运行: 使用kubeconfig文件
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}
	return dynamic.NewForConfig(cfg)
}

// informer = list-and-watch + 本地缓存 + 断线自动relist重放.
type InformerSource struct {
	client    dynamic.Interface
	namespace string
	mu        sync.Mutex // mu只保护current(最近一次已发布数据)
	current   map[string][]string
	syncedCh  chan struct{} // 只用于首次list
}

func NewInformerSource(client dynamic.Interface, namespace string) *InformerSource {
	return &InformerSource{
		client:    client,
		namespace: namespace,
		current:   make(map[string][]string),
		syncedCh:  make(chan struct{}),
	}
}

// Synced 在首次list全量落表后关闭. main用它当主启动窗口: 首次同步完成前不对外提供服务
func (s *InformerSource) Synced() <-chan struct{} {
	return s.syncedCh
}

// Run 启动informer并阻塞到ctx取消
func (s *InformerSource) Run(ctx context.Context, rt *Table) error {
	// defaultResync设置为0: 不做周期性全量重放, informer有断线重连后的relist
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(s.client, 0, s.namespace, nil)

	informer := factory.ForResource(modelGVR).Informer()

	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { s.upsert(rt, obj) },
		UpdateFunc: func(oldObj, newObj any) { s.upsert(rt, newObj) },
		DeleteFunc: func(obj any) { s.delete(rt, obj) },
	}); err != nil {
		return fmt.Errorf("add event handler: %w", err)
	}

	factory.Start(ctx.Done())

	// 首次list完成 = 本地缓存与apiserver达成一致
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("cache sync: %w", ctx.Err())
	}
	close(s.syncedCh)

	<-ctx.Done()
	return nil
}

func (s *InformerSource) upsert(rt *Table, obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		log.Printf("routetable: unexpected object type %T, skipped", obj)
		return
	}

	endpoints, found, err := unstructured.NestedStringSlice(u.Object, "status", "endpoints")
	if err != nil {
		log.Printf("routetable: model %s has non-string status.endpoints: %v", u.GetName(), err)
		return
	}
	// CR刚apply的时候,Operator还没有把status.endpoints写回, 此时应该当作空列表
	if !found {
		endpoints = []string{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.current[u.GetName()]; ok && slices.Equal(old, endpoints) {
		return
	}
	s.current[u.GetName()] = endpoints
	s.publishLocked(rt)
}

func (s *InformerSource) delete(rt *Table, obj any) {
	// Delete事件的对象可能是tombstone
	// watch期间错过删除, relist补发时, informer会包一层DeletedFinalUnknown, 而不是直接给原对象.
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = t.Obj
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		log.Printf("routetable: unexpected object type %T, skipped", obj)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.current[u.GetName()]; !ok {
		return
	}
	delete(s.current, u.GetName())
	s.publishLocked(rt)
}

// 构造全新的map给table, 每次新建map, 不要原地改map
func (s *InformerSource) publishLocked(rt *Table) {
	snapshot := make(map[string][]string, len(s.current))
	for model, eps := range s.current {
		snapshot[model] = eps
	}
	rt.Replace(snapshot)
	log.Printf("routable replaced: -> %v", snapshot)
}
