package proxy

import (
	"sync"
	"sync/atomic"
)

type InflightTracker struct {
	counters sync.Map // endpoint -> atomic.Int64
	pickMu   sync.Mutex
}

func NewInflightTracker() *InflightTracker {
	return &InflightTracker{}
}

func (t *InflightTracker) counter(ep string) *atomic.Int64 {
	if v, ok := t.counters.Load(ep); ok {
		return v.(*atomic.Int64)
	}

	// endpoint not found, create a new counter for it
	v, _ := t.counters.LoadOrStore(ep, &atomic.Int64{})
	return v.(*atomic.Int64)
}

// Load returns the current inflight count for the given endpoint.
func (t *InflightTracker) Load(ep string) int64 {
	return t.counter(ep).Load()
}

// Acquire increments the inflight count for the given endpoint and returns the function to release it.
// release function 使用sync.Once做幂等处理.
// 未来可能增加重试/熔断时, 不能每个请求恰好释放一次, 这里使用sync.Once保证幂等性.
func (t *InflightTracker) Acquire(ep string) func() {
	c := t.counter(ep)
	c.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			c.Add(-1)
		})
	}
}

// 原子地选出endpoints中在途请求最少的endpoint, 并将其inflight计数加1, 返回该endpoint及释放函数.
func (t *InflightTracker) AcquireLeast(endpoints []string) (string, func()) {
	if len(endpoints) == 0 {
		return "", func() {}
	}

	t.pickMu.Lock()

	best, bestCount := endpoints[0], t.counter(endpoints[0])
	for _, ep := range endpoints[1:] {
		if c := t.counter(ep); c.Load() < bestCount.Load() {
			best, bestCount = ep, c
		}
	}
	bestCount.Add(1)
	t.pickMu.Unlock()

	var once sync.Once
	return best, func() {
		once.Do(func() {
			bestCount.Add(-1)
		})
	}
}

// Snapshot 返回全部计数的一份拷贝数据, 目前用于测试和压测
func (t *InflightTracker) Snapshot() map[string]int64 {
	out := map[string]int64{}
	t.counters.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})

	return out
}
