package proxy

import (
	"fmt"

	"github.com/mini-maas/gateway/internal/openai"
)

const (
	ReasonRoundRobin       = "round-robin"
	ReasonLeastInflight    = "least-inflight"
	ReasonAffinity         = "affinity"
	ReasonAffinityFallback = "affinity-fallback"
)

type Decision struct {
	Endpoint  string
	Reason    string
	ReleaseFn func() // 后置处理函数, handler在Pick成功后执行defer做一些清理工作, 例如inflight算法会归还计数器
}

func emptyDecision() Decision {
	return Decision{ReleaseFn: func() {}}
}

// Picker 从就绪的模型endpoints中选择一个，返回选中的endpoint的地址
// 调度策略的接口定义
type Picker interface {
	Pick(params *openai.InferenceParams, endpoints []string) Decision
}

func NewPicker(strategy string, tracker *InflightTracker, maxAffinityInflight int64) (Picker, error) {
	switch strategy {
	case "round_robin":
		return NewRoundRobinPicker(), nil
	case "least_inflight":
		return &LeastInflightPicker{tracker: tracker}, nil
	case "prefix_affinity":
		return &PrefixAffinityPicker{tracker: tracker, maxInflight: maxAffinityInflight}, nil
	default:
		return nil, fmt.Errorf("unknown picker strategy: %s", strategy)
	}
}
