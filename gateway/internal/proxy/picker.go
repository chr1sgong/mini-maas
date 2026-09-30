package proxy

import (
	"net/http"
	"sync"
)

// Picker 从就绪的模型endpoints中选择一个，返回选中的endpoint的地址
// 调度策略的接口定义
type Picker interface {
	Pick(model string, endpoints []string, r *http.Request) string
}

type RoundRobinPicker struct {
	mu   sync.Mutex
	next map[string]int
}

func NewRoundRobinPicker() *RoundRobinPicker {
	return &RoundRobinPicker{next: make(map[string]int)}
}

// TODO: 直接使用互斥锁全部锁住会影响并发, 轮询的话可以把锁的粒度缩小到每个model的next索引更新上
func (p *RoundRobinPicker) Pick(model string, endpoints []string, r *http.Request) string {
	if len(endpoints) == 0 {
		return ""
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.next == nil {
		p.next = make(map[string]int)
	}

	index := p.next[model] % len(endpoints)
	p.next[model] = (index + 1) % len(endpoints)
	return endpoints[index]
}
