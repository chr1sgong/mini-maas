package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mini-maas/gateway/internal/openai"
	"github.com/mini-maas/gateway/internal/routetable"
)

// Handler 是OpenAI兼容端点
type Handler struct {
	Table  *routetable.Table
	Picker Picker
	Proxy  http.Handler // *httputil.Reverseproxy, 对Handler 而言只是个Handler
}

// POST /v1/chat/completions
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	params, ok := openai.ParamsFrom(r.Context())
	if !ok {
		openai.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "missing inference params")
		return
	}
	endpoints := h.Table.Endpoints(params.Model)
	if len(endpoints) == 0 {
		openai.WriteError(w, http.StatusNotFound, "model not found", "invalid_request_error", fmt.Sprintf("The model `%s` does not exist or you do not have access to it", params.Model))
		return
	}
	ep := h.Picker.Pick(params.Model, endpoints, r)
	h.Proxy.ServeHTTP(w, withTarget(r, ep))
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
