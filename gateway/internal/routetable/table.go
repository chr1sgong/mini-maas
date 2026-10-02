package routetable

import (
	"slices"
	"sync/atomic"
)

// Table 是路由表，存储了服务实例的路由信息。
// 每次请求都要读，只有Source 的watch事件会触发写，是一个典型的读多写少场景
// 使用Copy-On-Write原子快照: 写的时候使用全新的不可变map存入原子指针, 读的时候直接读取原子指针指向的快照，无需加锁。
type Table struct {
	snapshot atomic.Pointer[map[string][]string]
}

func NewTable() *Table {
	t := &Table{}
	t.snapshot.Store(&map[string][]string{})
	return t
}

func (t *Table) Endpoints(key string) []string {
	return (*t.snapshot.Load())[key]
}

func (t *Table) Models() []string {
	snap := *t.snapshot.Load()
	models := make([]string, 0, len(snap))
	for model := range snap {
		models = append(models, model)
	}
	slices.Sort(models)
	return models
}

// Replace 用新的快照替换当前的路由表数据。
// Source侧会维护全量数据，任何CR 事件后构建新快照可以使用Replace
// TODO: 被摘除的endpoints需要做相应的清理
func (t *Table) Replace(snapshot map[string][]string) {
	t.snapshot.Store(&snapshot)
}
