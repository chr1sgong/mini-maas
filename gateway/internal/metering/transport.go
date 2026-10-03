package metering

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mini-maas/gateway/internal/metrics"
	"github.com/mini-maas/gateway/internal/openai"
	"github.com/mini-maas/gateway/internal/ratelimit"
)

// WrapTransport是transport层的统一旁路
// 1. 观测请求和响应的流量数据, 推理请求都包一层
// 2. 延迟指标必开, 结算功能可选
func WrapTransport(base http.RoundTripper, settleEnable bool) http.RoundTripper {
	return &transport{base: base, settleEnable: settleEnable}
}

type transport struct {
	base         http.RoundTripper
	settleEnable bool
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	params, ok := openai.ParamsFrom(req.Context())
	if !ok {
		return resp, nil
	}
	resp.Body = newTeeBody(resp.Body, req.Context(), teeOptions{
		model:        params.Model,
		sse:          strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"),
		start:        start,
		settleEnable: t.settleEnable,
	})

	return resp, nil
}

type teeOptions struct {
	model        string
	sse          bool
	start        time.Time
	settleEnable bool
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

func newTeeBody(rc io.ReadCloser, ctx context.Context, opt teeOptions) io.ReadCloser {
	ch := make(chan []byte, 64)
	// TODO: 可以使用消息队列, 以提高吞吐量和可靠性. 当前先简化使用coroutine
	go consume(ctx, ch, opt)

	return &teeBody{rc: rc, ch: ch}
}

func consume(ctx context.Context, ch <-chan []byte, opt teeOptions) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("metering: sidecar panic recovered: %v", r)
		}
	}()

	p := newParser(opt.sse)

	// 上报TTFT, TPOT指标
	// TODO: 抽成函数
	var firstChunk, prevChunk time.Time
	var gaps time.Duration
	var gapCnt int

	for chunk := range ch {
		now := time.Now()
		if firstChunk.IsZero() {
			firstChunk = now
			if opt.sse {
				metrics.ObserveTTFT(opt.model, now.Sub(opt.start).Seconds())
			}
		} else if opt.sse {
			gaps += now.Sub(prevChunk)
			gapCnt++
		}
		prevChunk = now
		p.feed(chunk)
	}

	if opt.sse && gapCnt > 0 {
		metrics.ObserveTPOT(opt.model, gaps.Seconds()/float64(gapCnt))
	}

	if !opt.settleEnable {
		return
	}

	if usage := p.finish(); usage != nil {
		ratelimit.SettleFromContext(ctx, *usage)
	} else {
		log.Printf("metering: usage not found, settlement skippped")
	}
}
