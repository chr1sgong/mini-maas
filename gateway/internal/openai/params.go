package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"net/http"
)

// InferenceParams 是网关在请求体里需要的全部字段
type InferenceParams struct {
	Model               string `json:"model"`
	Stream              bool   `json:"stream"`
	MaxCompletionTokens int    `json:"max_completion_tokens"`
	AffinityKey         uint64 `json:"affinity_key"` // 用于前缀亲和性的键, 需要提前算好,不要在网关实时计算, 否则会导致request的Body重复读取,影响性能
}

type paramsCtxKey struct{}

// 把参数放到context中
func WithParams(ctx context.Context, p *InferenceParams) context.Context {
	return context.WithValue(ctx, paramsCtxKey{}, p)
}

// 从context中取出参数
func ParamsFrom(ctx context.Context) (*InferenceParams, bool) {
	p, ok := ctx.Value(paramsCtxKey{}).(*InferenceParams)
	return p, ok
}

func ParamsMiddleware(injectUsage bool, affinityBytes int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "could not read request body")
				return
			}
			var fields map[string]any
			if err := json.Unmarshal(body, &fields); err != nil {
				WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "request body is not valid json")
				return
			}

			params := &InferenceParams{
				Model:  stringField(fields, "model"),
				Stream: boolField(fields, "stream"),
				MaxCompletionTokens: firstNonZero(
					intField(fields, "max_completion_tokens"),
					intField(fields, "max_tokens"),
				),
				AffinityKey: affinityKey(fields, affinityBytes), // 假设最大使用affinityBytes字节计算亲和性键
			}

			if params.Model == "" {
				WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "model is required")
				return
			}

			if injectUsage && params.Stream {
				mergeStreamOptions(fields)
				if body, err = json.Marshal(fields); err != nil {
					WriteError(w, http.StatusInternalServerError, "internal_error", "internal_error", "could not re-encode request body")
				}
			}
			// !!! 还原body
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			ctx := WithParams(r.Context(), params)
			r = r.WithContext(ctx)
			next.ServeHTTP(w, r)
		})
	}
}

func affinityKey(fields map[string]any, maxBytes int) uint64 {
	if maxBytes <= 0 {
		return 0
	}

	msg, ok := fields["message"].([]any)
	if !ok || len(msg) == 0 {
		return 0
	}

	h := fnv.New64a()
	budget := maxBytes
	for _, m := range msg {
		if budget <= 0 {
			break
		}
		// OpenAI协议, 解析role和content
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		// role, content家一个分隔符,防止("ab","c") 与 ("a","bc") 撞 key
		// TODO: 未来支持非字符串(多模态)的content
		role, _ := mm["role"].(string)
		_, _ = h.Write([]byte(role))
		_, _ = h.Write([]byte{0})

		content, _ := mm["content"].(string)
		n := min(len(content), budget)
		_, _ = h.Write([]byte(content[:n]))
		_, _ = h.Write([]byte{0})
		budget -= n
	}
	return h.Sum64()
}

func mergeStreamOptions(fields map[string]any) {
	opts, _ := fields["stream_options"].(map[string]any)
	if opts == nil {
		opts = make(map[string]any)
	}
	opts["include_usage"] = true
	fields["stream_options"] = opts
}

func stringField(fields map[string]any, key string) string {
	s, _ := fields[key].(string)
	return s
}

func boolField(fields map[string]any, key string) bool {
	b, _ := fields[key].(bool)
	return b
}

func intField(fields map[string]any, key string) int {
	i, _ := fields[key].(float64)
	return int(i)
}

func firstNonZero(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}
