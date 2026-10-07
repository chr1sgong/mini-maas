package proxy

import (
	"github.com/mini-maas/gateway/internal/openai"
)

type LeastInflightPicker struct {
	tracker *InflightTracker
}

func (p *LeastInflightPicker) Pick(params *openai.InferenceParams, endpoints []string) Decision {
	if len(endpoints) == 0 {
		return emptyDecision()
	}
	ep, releaseFn := p.tracker.AcquireLeast(endpoints)
	return Decision{
		Endpoint:  ep,
		Reason:    ReasonLeastInflight,
		ReleaseFn: releaseFn,
	}
}
