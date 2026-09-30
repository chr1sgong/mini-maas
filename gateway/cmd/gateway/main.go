package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/mini-maas/gateway/internal/middleware"
	"github.com/mini-maas/gateway/internal/proxy"
	"github.com/mini-maas/gateway/internal/routetable"
)

func main() {
	listen := flag.String("listen", ":8080", "监听地址")
	namespace := flag.String("namespace", "mini-maas", "Model CR 所在的k8s namespace")
	kubeconfig := flag.String("kubeconfig", "default", "kubeconfig 文件路径; 空则in-cluster优先、fallback默认路经")

	flag.Parse()

	_ = namespace
	_ = kubeconfig

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	defer stop()

	table := routetable.NewTable()

	source := routetable.NewStaticSource(map[string][]string{
		"qwen2-5-3b": {"10.42.0.23:8000"},
		"qwen2-5-7b": {"10.42.0.26:8000"},
	})

	go func() {
		if err := source.Run(ctx, table); err != nil {
			log.Printf("routetable source existed: %v", err)
		}
	}()

	handler := &proxy.Handler{
		Table:  table,
		Picker: proxy.NewRoundRobinPicker(),
		Proxy:  proxy.NewReverseProxy(proxy.NewTransport()),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/models", handler.Models)
	mux.HandleFunc("POST /v1/chat/completions", handler.ChatCompletions)

	srv := &http.Server{
		Addr: *listen,
		Handler: middleware.Chain(mux,
			middleware.Logging,
			// middleware.Auth,
			// middleware.RateLimit,
		),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second, // TODO SSE要覆盖整个响应的生命周期，需要权衡
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("gateway listening on %s", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("gateway failed to listen: %v", err)
	}

}
