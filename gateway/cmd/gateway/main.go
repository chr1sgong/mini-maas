package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/mini-maas/gateway/internal/auth"
	"github.com/mini-maas/gateway/internal/metering"
	"github.com/mini-maas/gateway/internal/metrics"
	"github.com/mini-maas/gateway/internal/middleware"
	"github.com/mini-maas/gateway/internal/openai"
	"github.com/mini-maas/gateway/internal/proxy"
	"github.com/mini-maas/gateway/internal/ratelimit"
	"github.com/mini-maas/gateway/internal/routetable"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	configPath := flag.String("config", "gateway.json", "配置文件路径(JSON)")
	namespace := flag.String("namespace", "mini-maas", "Model CR 所在的k8s namespace")
	kubeconfig := flag.String("kubeconfig", "default", "kubeconfig 文件路径; 空则in-cluster优先、fallback默认路经")

	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 路由表: 通过informer watch model CR实现
	table := routetable.NewTable()
	dynamicClient, err := routetable.NewDynamicClient(*kubeconfig)
	if err != nil {
		log.Fatalf("failed to create dynamic client: %v", err)
	}
	source := routetable.NewInformerSource(dynamicClient, *namespace)
	// 同步路由表
	go func() {
		if err := source.Run(ctx, table); err != nil {
			log.Printf("routetable source existed: %v", err)
		}
	}()

	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	select {
	case <-source.Synced():
	case <-syncCtx.Done():
		cancel()
		log.Fatalf("routetable initial sync timeout: %v", syncCtx.Err())
	}
	cancel() // cancel the sync context if synced successfully

	identities := cfg.identities()
	store := auth.NewStaticKeyStore(identities)
	registry := ratelimit.NewRegistry(identities)

	transport := metering.WrapTransport(proxy.NewTransport(), cfg.Metering)
	// 记录在途请求
	tracker := proxy.NewInflightTracker()
	metrics.RegisterInflight(tracker)
	// 路由选择器
	picker, err := proxy.NewPicker(cfg.Routing.Strategy, tracker, cfg.Routing.MaxAffinityInflight)
	if err != nil {
		log.Fatalf("failed to create picker: %v", err)
	}

	handler := &proxy.Handler{
		Table:    table,
		Picker:   picker,
		Proxy:    proxy.NewReverseProxy(transport),
		Strategy: cfg.Routing.Strategy,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", promhttp.Handler()) // 指标查询直接豁免,与模型不是一套身份校验系统. TODO: 管理平台也有一套身份校验,可以在那部分设计考虑
	mux.HandleFunc("GET /v1/models", handler.Models)
	mux.HandleFunc("POST /v1/chat/completions", handler.ChatCompletions)

	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: middleware.Chain(mux,
			ratelimit.NewMiddleware(registry, cfg.Defaults.MaxCompletionTokens),
			auth.NewMiddleware(store),
			openai.ParamsMiddleware(cfg.Metering, cfg.Routing.AffinityBytes),
			middleware.Logging,
		),
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout:      600 * time.Second, // 流式响应耗时很长,不要设置这个超时时间
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("gateway listening on %s", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("gateway failed to listen: %v", err)
	}

}
