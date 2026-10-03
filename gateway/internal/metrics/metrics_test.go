package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// 一轮写入后 gather：指标族都在、inflight collector 读得到快照。
// 标签形状（有界枚举）靠 code review 与 Q5 纪律保证，测试不重复。
func TestMetricsSmoke(t *testing.T) {
	RequestsTotal.WithLabelValues("m", "t", "200").Inc()
	RouteDecisions.WithLabelValues("round_robin", "rr", "10.0.0.1:8000").Inc()
	RateLimitRejected.WithLabelValues("t", "qps").Inc()
	RequestDuration.WithLabelValues("m", "true").Observe(1.5)
	ObserveTTFT("m", 0.2)
	ObserveTPOT("m", 0.05)

	RegisterInflight(inflightFunc(func() map[string]int64 {
		return map[string]int64{"10.0.0.1:8000": 3}
	}))

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	want := map[string]bool{
		"maas_gateway_requests_total":           false,
		"maas_gateway_request_duration_seconds": false,
		"maas_gateway_ttft_seconds":             false,
		"maas_gateway_tpot_seconds":             false,
		"maas_gateway_route_decisions_total":    false,
		"maas_gateway_ratelimit_rejected_total": false,
		"maas_gateway_inflight_requests":        false,
	}
	for _, f := range families {
		if _, ok := want[f.GetName()]; ok {
			want[f.GetName()] = true
		}
		if f.GetName() == "maas_gateway_inflight_requests" {
			if got := f.GetMetric()[0].GetGauge().GetValue(); got != 3 {
				t.Fatalf("inflight = %v, want 3", got)
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("metric family %s not gathered", name)
		}
	}
}

type inflightFunc func() map[string]int64

func (f inflightFunc) Snapshot() map[string]int64 { return f() }
