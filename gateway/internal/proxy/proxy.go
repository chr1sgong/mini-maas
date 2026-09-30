package proxy

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// 理由结果（路由算法选中的endpoint) 通过request context传给Rewrite
// Handler负责“路由决策”， ReverseProxy负责“字节搬运”
type ctxKeyTarget struct {
}

func withTarget(r *http.Request, target string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyTarget{}, target))
}

func targetFrom(r *http.Request) string {
	v, _ := r.Context().Value(ctxKeyTarget{}).(string)
	return v
}

// NewReverseProxy 构造转发器
// TODO: 流式的生死线都在这几个回调里
func NewReverseProxy(transport *http.Transport) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: transport,

		// Rewrite 在转发前写上游请求
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := targetFrom(pr.In)
			if !strings.Contains(target, "://") {
				target = "http://" + target
			}
			targetURL, err := url.Parse(target)
			if err != nil || targetURL.Hostname() == "" || targetURL.User != nil ||
				(targetURL.Scheme != "http" && targetURL.Scheme != "https") {
				log.Printf("invalid proxy target %q", target)
				pr.Out.URL = &url.URL{}
				return
			}
			pr.SetURL(targetURL)
			pr.SetXForwarded()
		},

		// ErrorHandler 在“连不上上游 | 拷贝出错” 时兜底
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				log.Printf("proxy request canceled: %v", err)
				return
			}
			log.Printf("proxy error: %v", err)
			writeError(w, http.StatusBadGateway, "upstream_error", "server_error", "The upstream model service is unavailable.")
		},

		// FlushInterval: 先读Go源码 flushInterval()
		// 确认SSE响应在零值下的实际行动，再决定设不j设
	}
}

// NewTransport 上游连接池。http.DefaultTransport 的MaxIdleConnsPerHost=2是经典网关坑：并发大于2时反复建连，P99飙升
func NewTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 256,
		IdleConnTimeout:     90 * time.Second,

		// TODO: ResponseHeaderTimeout要不要设置
		// vLLM排队时TTFT可达十多秒，设置笑了会误杀排队中的请求
	}
}
