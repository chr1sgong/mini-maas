package routetable

import "context"

// Source 是路由表数据的来源接口。
type Source interface {
	Run(ctx context.Context, sink *Table) error
}

// for test
type StaticSource struct {
	snapshot map[string][]string
}

func NewStaticSource(snapshot map[string][]string) *StaticSource {
	return &StaticSource{
		snapshot: snapshot,
	}
}

func (s *StaticSource) Run(ctx context.Context, sink *Table) error {
	sink.Replace(s.snapshot)
	return nil
}

// TODO: InformerSource —— watch Model CR，任一 CR 的 status.endpoints 变化都反映进快照。
//
// 实现要点（client-go dynamic informer，无需 codegen）：
//   - GVR: group=maas.chr1sgong.io, version=v1alpha1, resource=models
//     （先 kubectl api-resources 验证 resource 名，别猜）
//   - rest.Config 获取：in-cluster 优先，fallback kubeconfig（main 里的 -kubeconfig flag）
//   - Add/Update/DeleteFunc → 维护全量 map[model名][]string → sink.Replace(新拷贝)
//   - unstructured 取值路径: .status.endpoints（[]any，元素 string）
//     注意 CR 刚 apply、status 还没回填时 endpoints 不存在——视作空列表，不是错误
//   - 重连与事件丢失：informer 自带 relist 兜底，这正是它比手写 watch 循环值钱的地方
//   - 每次 Replace 打一行日志（模型名+endpoints），Step 3 验收就靠它
