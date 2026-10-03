package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/mini-maas/gateway/internal/auth"
	"github.com/mini-maas/gateway/internal/metrics"
	"github.com/mini-maas/gateway/internal/openai"
	"github.com/mini-maas/gateway/internal/routetable"
)

// Handler 是OpenAI兼容端点
type Handler struct {
	Table    *routetable.Table
	Picker   Picker
	Proxy    http.Handler // *httputil.Reverseproxy, 对Handler 而言只是个Handler
	Strategy string       // 路由策略，例如 "round_robin" 或 "least_loaded"
}

// 包装 ResponseWriter 会弄丢底层实现的可选接口——ReverseProxy 流式转发
// 依赖 http.Flusher，http.ResponseController 依赖 Unwrap——必须显式委托回去。
// （生产环境更通用的解法是 felixge/httpsnoop；这里两个接口手写足够。）
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// POST /v1/chat/completions
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	params, ok := openai.ParamsFrom(r.Context())
	if !ok {
		openai.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "missing inference params")
		return
	}

	// 记录请求指标
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
	tenant := "-"
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		tenant = id.Tenant
	}

	defer func() {
		metrics.RequestsTotal.WithLabelValues(params.Model, tenant, strconv.Itoa(sw.code)).Inc()
		metrics.RequestDuration.WithLabelValues(params.Model, strconv.FormatBool(params.Stream)).Observe(time.Since(start).Seconds())
	}()

	endpoints := h.Table.Endpoints(params.Model)
	if len(endpoints) == 0 {
		openai.WriteError(sw, http.StatusNotFound, "model_not_found", "invalid_request_error", fmt.Sprintf("The model `%s` does not exist or you do not have access to it", params.Model))
		return
	}
	decision := h.Picker.Pick(params, endpoints)
	// 确保在请求处理完成后释放决策
	defer decision.ReleaseFn()

	metrics.RouteDecisions.WithLabelValues(h.Strategy, decision.Reason, decision.Endpoint).Inc()

	sw.Header().Set("X-Maas-Endpoint", decision.Endpoint)
	sw.Header().Set("X-Maas-Route-Reason", decision.Reason)
	log.Printf("route model=%s endpoint=%s reason=%s key=%016x", params.Model, decision.Endpoint, decision.Reason, params.AffinityKey)

	h.Proxy.ServeHTTP(sw, withTarget(r, decision.Endpoint))
}

// GET /v1/models
func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	models := h.Table.Models()
	list := openai.ModelList{
		Object: "list",
		Data:   make([]openai.ModelObject, 0, len(models)),
	}
	for _, m := range models {
		list.Data = append(list.Data, openai.ModelObject{
			ID:      m,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "vllm",
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(list)
}
