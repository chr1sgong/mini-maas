package proxy

import (
	"encoding/binary"
	"hash/fnv"

	"github.com/mini-maas/gateway/internal/openai"
)

// 把"前缀相同"的请求打到同一个endpoint, 配合引擎提高kv cache命中率
// 参考设计文档
type PrefixAffinityPicker struct {
	tracker     *InflightTracker
	maxInflight int64
}

func (p *PrefixAffinityPicker) Pick(params *openai.InferenceParams, endpoints []string) Decision {
	if len(endpoints) == 0 {
		return emptyDecision()
	}

	if params.AffinityKey == 0 {
		// 没有亲和性键, 使用least-inflight策略
		ep, releaseFn := p.tracker.AcquireLeast(endpoints)
		return Decision{
			Endpoint:  ep,
			Reason:    ReasonLeastInflight,
			ReleaseFn: releaseFn,
		}
	}

	preferred := rendezvous(params.AffinityKey, endpoints)
	if p.tracker.Load(preferred) >= p.maxInflight {
		// 大量前缀缓存打到同一个实例上了,超过了单个实例的最大在途请求量了,降级为least-inflight
		ep, releaseFn := p.tracker.AcquireLeast(endpoints)
		return Decision{
			Endpoint:  ep,
			Reason:    ReasonAffinityFallback,
			ReleaseFn: releaseFn,
		}
	}
	return Decision{
		Endpoint:  preferred,
		Reason:    ReasonAffinity,
		ReleaseFn: p.tracker.Acquire(preferred),
	}
}

// rendezvous 根据key和endpoints列表选择一个首选的endpoint
// 核心思路: hash(key, endpoint) 选择hash值最大的endpoint作为首选
// 优点: 无状态, 复杂度O(n), 适合当前规模
// 缺点: 当某个endpoint的hash值总是最大时, 可能导致负载不均衡
// TODO: endpoints很多时, rendezvous算法的复杂度可能成为瓶颈, 需要优化. 参考一致性哈希设计
func rendezvous(key uint64, endpoints []string) string {
	var best string
	var bestScore uint64
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], key)
	for _, ep := range endpoints {
		h := fnv.New64a()
		h.Write(buf[:])
		h.Write([]byte(ep))
		if score := h.Sum64(); score > bestScore {
			best, bestScore = ep, score
		}
	}
	return best
}
