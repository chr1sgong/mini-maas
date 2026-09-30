package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/mini-maas/gateway/internal/routetable"
)

// Handler 是OpenAI兼容端点
type Handler struct {
	Table  *routetable.Table
	Picker Picker
	Proxy  http.Handler // *httputil.Reverseproxy, 对Handler 而言只是个Handler
}

// requestPeek 只取路由与后续限流需要的字段，body直接转发给上游
type requestPeek struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"` // 是否流式输出， TPM计数，监控指标都需要
}

// ChatCompletions 处理 POST /v1/chat/completions 请求
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	// TODO: 实现ChatCompletions处理逻辑
	peek, err := peekModel(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	endpoints := h.Table.Endpoints(peek.Model)
	ep := h.Picker.Pick(peek.Model, endpoints, r)
	h.Proxy.ServeHTTP(w, withTarget(r, ep))
}

// 处理 GET /v1/models
func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	models := h.Table.Models()
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(modelList{
		Object: "list",
		Data: func() []modelObject {
			objs := make([]modelObject, 0, len(models))
			for _, m := range models {
				objs = append(objs, modelObject{
					ID:      m,
					Object:  "model",
					Created: time.Now().Unix(),
					OwnedBy: "vllm",
				})
			}
			return objs
		}(),
	})
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

func peekModel(r *http.Request) (*requestPeek, error) {
	// TODO: 实现peekModel逻辑，从请求中解析出model和stream字段
	// 从请求中解析出model和stream字段
	// Warning: Reading the body here will consume it, so we need to restore it afterwards.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var peek requestPeek
	if err := json.Unmarshal(body, &peek); err != nil {
		return nil, err
	}

	return &peek, nil
}
