package proxy

import (
	"sync"
	"sync/atomic"

	"github.com/mini-maas/gateway/internal/openai"
)

type RoundRobinPicker struct {
	mu   sync.Mutex
	next map[string]*atomic.Int64
}

func NewRoundRobinPicker() *RoundRobinPicker {
	return &RoundRobinPicker{next: make(map[string]*atomic.Int64)}
}

// TODO: 直接使用互斥锁全部锁住会影响并发, 轮询的话可以把锁的粒度缩小到每个model的next索引更新上
func (p *RoundRobinPicker) Pick(params *openai.InferenceParams, endpoints []string) Decision {
	if len(endpoints) == 0 {
		return emptyDecision()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	model := params.Model
	// TODO: 这个地方如果同一个model同时过来,就会有两个goroutine同时创建atomic.Int64实例, 会有竞态问题
	if _, ok := p.next[model]; !ok {
		p.next[model] = &atomic.Int64{}
	}
	index := int(p.next[model].Load()) % len(endpoints)
	p.next[model].Store(int64((index + 1) % len(endpoints)))
	return Decision{
		Endpoint:  endpoints[index],
		Reason:    ReasonRoundRobin,
		ReleaseFn: func() {},
	}
}
