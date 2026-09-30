package routetable

// Table 是路由表，存储了服务实例的路由信息。
// 每次请求都要读，只有Source 的watch事件会触发写，是一个典型的读多写少场景
// 设计思考：数据怎么存储？读要不要加锁？
type Table struct {
	snapshot map[string][]string
}

func NewTable() *Table {
	return &Table{
		snapshot: make(map[string][]string),
	}
}

func (t *Table) Endpoints(key string) []string {
	// TODO: 根据 key 返回对应的服务实例列表
	if endpoints, ok := t.snapshot[key]; ok {
		return endpoints
	}
	return nil
}

func (t *Table) Models() []string {
	// TODO: 返回所有的服务模型列表
	models := make([]string, 0, len(t.snapshot))
	for model := range t.snapshot {
		models = append(models, model)
	}
	return models
}

// Replace 用新的快照替换当前的路由表数据。
// Source侧会维护全量数据，任何CR 事件后构建新快照可以使用Replace
func (t *Table) Replace(snapshot map[string][]string) {
	// TODO: 要不要加锁
	t.snapshot = snapshot
}
