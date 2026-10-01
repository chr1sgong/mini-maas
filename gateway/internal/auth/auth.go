package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mini-maas/gateway/internal/openai"
)

func NewMiddleware(store KeyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/healthz" { // 探活接口直接放行
				next.ServeHTTP(w, r)
				return
			}

			apiKey, ok := extractBearer(r)
			if !ok {
				openai.WriteError(w, http.StatusUnauthorized, "invalid_api_key", "invalid_request_error", "Missing or malformed Authorization header; expected 'Bearer <api-key>'.")
				return
			}
			// 1. 身份校验
			identity, err := store.Get(apiKey)
			switch {
			case errors.Is(err, ErrorKeyNotFound):
				openai.WriteError(w, http.StatusUnauthorized, "invalid_api_key", "invalid_request_error", "API key not found.")
				return
			case err != nil:
				openai.WriteError(w, http.StatusInternalServerError, "internal_error", "internal_server_error", "Internal server error.")
				return
			}
			// 2. 确保用户指定的模型也是有权限获取的
			if params, ok := openai.ParamsFrom(r.Context()); ok && !modelAllowed(identity, params.Model) {
				openai.WriteError(w, http.StatusForbidden, "model_not_allowed", "invalid_request_error", fmt.Sprintf("The specified model `%s` does not exist or you do not have access to it.", params.Model))
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), identity)))
		})
	}
}

func extractBearer(r *http.Request) (string, bool) {
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	return key, ok && key != ""
}

func modelAllowed(identity *Identity, model string) bool {
	for _, m := range identity.Models {
		if m == model {
			return true
		}
	}

	return false
}
