package metering

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/mini-maas/gateway/internal/ratelimit"
)

func WrapTransport(base http.RoundTripper) http.RoundTripper {
	return &transport{base: base}
}

type transport struct {
	base http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if !ratelimit.HasSettle(req.Context()) {
		// 没有注册结算逻辑, 直接返回
		return resp, nil
	}
	// 计量要读取的是上游响应体, 不是请求体;
	// 若误传 req.Body, 响应体被替换为空, 客户端会看到 Content-Length 与实际字节数不符
	resp.Body = newTeeBody(resp.Body, req.Context(), strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"))

	return resp, nil
}

type teeBody struct {
	rc io.ReadCloser
	ch chan<- []byte
}

func (t *teeBody) Read(p []byte) (n int, err error) {
	n, err = t.rc.Read(p)
	if n > 0 {
		chunk := make([]byte, n)
		copy(chunk, p[:n])
		select {
		case t.ch <- chunk:
		default:
			log.Printf("metering sidecar channel full, chunk dropped (本条结算dropped)")
		}
	}

	return n, err
}

func (t *teeBody) Close() error {
	err := t.rc.Close()
	close(t.ch) // 通知收尾 ReverseProxy确保关闭响应体
	return err
}

func newTeeBody(rc io.ReadCloser, ctx context.Context, sse bool) io.ReadCloser {
	ch := make(chan []byte, 64)
	go consume(ctx, ch, sse)

	return &teeBody{rc: rc, ch: ch}
}

func consume(ctx context.Context, ch <-chan []byte, sse bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("metering: sidecar panic recovered: %v", r)
		}
	}()

	p := newParser(sse)
	for chunk := range ch {
		p.feed(chunk)
	}

	if usage := p.finish(); usage != nil {
		ratelimit.SettleFromContext(ctx, *usage)
	} else {
		log.Printf("metering: usage not found, settlement skippped")
	}
}
