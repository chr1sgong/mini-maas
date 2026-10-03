package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	namespace = "maas"
	subsystem = "gateway"
)

var (

	// 请求计数
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "requests_total",
		Help:      "Total number of requests received by the gateway.",
	}, []string{"model", "tenant", "code"})

	// 网关视角的请求耗时
	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "request_duration_seconds",
		Help:      "Request duration in seconds observed by gateway.",
		Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30},
	}, []string{"model", "stream"})

	// 路由决策计数
	RouteDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "route_decisions_total",
		Help:      "Number of route decisions made by the gateway.",
	}, []string{"strategy", "reason", "endpoint"})

	// 限流(429)计数, dimension: qps | tpm
	RateLimitRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "ratelimit_rejected_total",
		Help:      "Number of requests rejected due to rate limiting.",
	}, []string{"tenant", "dimension"})

	ttft = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "ttft_seconds",
		Help:      "Time to first SSE chunk observed by the gateway (upstream queue + prefill + first token).",
		Buckets:   []float64{0.05, 0.1, 0.2, 0.4, 0.8, 1.6, 3.2},
	}, []string{"model"})

	tpot = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tpot_seconds",
		Help:      "(Mean gap between SSE chunks observed by the gateway (chunk ~= token)).",
		Buckets:   []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32},
	}, []string{"model"})

	ttftObservers sync.Map // model -> prometheus.Observer
	tpotObservers sync.Map
)

func ObserveTTFT(model string, seconds float64) {
	observer, ok := ttftObservers.Load(model)
	if !ok {
		observer, _ = ttftObservers.LoadOrStore(model, ttft.WithLabelValues(model))
	}
	observer.(prometheus.Observer).Observe(seconds)
}

func ObserveTPOT(model string, seconds float64) {
	observer, ok := tpotObservers.Load(model)
	if !ok {
		observer, _ = tpotObservers.LoadOrStore(model, tpot.WithLabelValues(model))
	}
	observer.(prometheus.Observer).Observe(seconds)
}

type InflightSource interface {
	Snapshot() map[string]int64
}

type InflightCollector struct {
	src  InflightSource
	desc *prometheus.Desc
}

func (c *InflightCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *InflightCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.src.Snapshot()
	for endpoint, inflight := range snapshot {
		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			float64(inflight),
			endpoint,
		)
	}
}

func RegisterInflight(src InflightSource) {
	prometheus.MustRegister(&InflightCollector{
		src: src,
		desc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "inflight_requests"),
			"In-flight requests per upstream endpoint.",
			[]string{"endpoint"},
			nil,
		),
	})
}
