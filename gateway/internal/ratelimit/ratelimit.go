package ratelimit

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/mini-maas/gateway/internal/auth"
	"github.com/mini-maas/gateway/internal/openai"
)

func NewMiddleware(reg *Registry, defaultMaxTokens int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.IdentityFrom(r.Context())
			if !ok { // 没有id仍然放行,说明是探活等业务无关的请求,直接放行
				next.ServeHTTP(w, r)
				return
			}
			pair := reg.pair(id.Key)
			if pair == nil { // 没有配置限额的key 不限流  TODO: 这里需要再考虑下设计,可能有漏洞
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			// TPM 限流判断
			if params, ok := openai.ParamsFrom(ctx); ok {
				estimateTokens := estimateTokens(r, params, defaultMaxTokens)
				if !pair.tpm.AllowN(estimateTokens) {
					write429(w, "tokens", pair.tpm.RetryAfter(estimateTokens))
					return
				}
				ctx = withSettle(ctx, func(usage openai.Usage) {
					refund := estimateTokens - float64(usage.TotalTokens)
					pair.tpm.Refund(refund)
					log.Printf("settle tenant=%s 预估扣减量=%.0f 实际用量=%d 归还量=%.0f", id.Tenant, estimateTokens, usage.TotalTokens, refund)
				})
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func write429(w http.ResponseWriter, reason string, retryAfter int) {
	if retryAfter <= 1 {
		retryAfter = 1
	}

	sec := strconv.Itoa(retryAfter)
	w.Header().Set("Retry-After", sec)
	openai.WriteError(w, http.StatusTooManyRequests, "rate_limit_exceeded", reason, fmt.Sprintf("Rate limit excedded; please retry after %s s", sec))
}

// 预估一下请求需要的tokens数量
func estimateTokens(r *http.Request, params *openai.InferenceParams, defaultMaxTokens int) float64 {
	maxTokens := params.MaxCompletionTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	// 预估的tokens数量 = 请求体大小/4(经验值) + 最大tokens
	return float64(r.ContentLength)/4 + float64(maxTokens)
}

type settleCtxKey struct{}

func withSettle(ctx context.Context, fn func(usage openai.Usage)) context.Context {
	return context.WithValue(ctx, settleCtxKey{}, fn)
}

func HasSettle(ctx context.Context) bool {
	fn, ok := ctx.Value(settleCtxKey{}).(func(usage openai.Usage))
	return ok && fn != nil
}

func SettleFromContext(ctx context.Context, usage openai.Usage) {
	if fn, ok := ctx.Value(settleCtxKey{}).(func(usage openai.Usage)); ok && fn != nil {
		fn(usage)
	}
}
