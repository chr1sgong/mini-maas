package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// InferenceParams 是网关在请求体里需要的全部字段
type InferenceParams struct {
	Model               string `json:"model"`
	Stream              bool   `json:"stream"`
	MaxCompletionTokens int    `json:"max_completion_tokens"`
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

func ParamsMiddleware(injectUsage bool) func(http.Handler) http.Handler {
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

func mergeStreamOptions(fields map[string]any) {
	opts, _ := fields["stream_options"].(map[string]any)
	if opts == nil {
		opts = make(map[string]any)
		fields["stream_options"] = opts
	}
	opts["inject_usage"] = true
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
