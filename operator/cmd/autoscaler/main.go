package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/chr1sgong/mini-maas/operator/internal/autoscaler"
)

func main() {
	var (
		promAddr    = flag.String("prom-addr", "http://localhost:9090", "Prometheus base URL")
		kubeconfig  = flag.String("kubeconfig", "", "kubeconfig path (empty = in-cluster, else default loading rules)")
		namespace   = flag.String("namespace", "mini-maas", "Model CR namespace")
		model       = flag.String("model", "qwen2.5-7b-awq", "Model CR name ")
		minR        = flag.Int("min", 1, "min replicas")
		maxR        = flag.Int("max", 2, "max replicas")
		tick        = flag.Duration("tick", 5*time.Second, "loop interval")
		upThreshold = flag.Float64("up-threshold", 4, "scale up when metrics >= this")
		upSamples   = flag.Int("up-samples", 2, "consecutive reaching samples requred to scale up")
		idleRunning = flag.Float64("idle-running", 1, "running <= this counts as idle")
		downWindow  = flag.Duration("down-window", 5*time.Minute, "sustained idle required to scale down")
		upCooldown  = flag.Duration("up-cooldown", 5*time.Minute, "no scale-down within this after a scale up")
		dryRun      = flag.Bool("dry-run", false, "log descisions without pathcing the CR")
	)
	flag.Parse()

	cfg := autoscaler.Config{
		Min:         *minR,
		Max:         *maxR,
		UpThreshold: *upThreshold,
		UpSamples:   *upSamples,
		IdleRunning: *idleRunning,
		DownWindow:  *downWindow,
		UpCooldown:  *upCooldown,
	}

	cli, err := autoscaler.NewDynamicClient(*kubeconfig)
	if err != nil {
		log.Fatalf("kube client: %v", err)
	}

	promClient := autoscaler.NewPromClient(*promAddr)
	act := autoscaler.NewActuator(cli, *namespace, *model, *dryRun)

	mode := "ACTIVE"
	if *dryRun {
		mode = "DRY-RUN"
	}
	log.Printf("autoscaler %s model=%s/%s min=%d max=%d tick=%s up=%.1f*%d down=idle%s", mode, *namespace, *model, *minR, *maxR, *tick, *upThreshold, *upSamples, *downWindow)

	st := autoscaler.State{}
	step(cfg, promClient, act, *model, *dryRun, &st)
	ticker := time.NewTicker(*tick)
	defer ticker.Stop()
	for range ticker.C {
		step(cfg, promClient, act, *model, *dryRun, &st)
	}
}

func step(cfg autoscaler.Config, promClient *autoscaler.PromClient, act *autoscaler.Actuator, model string, dryRun bool, st *autoscaler.State) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	current, err := act.CurrentReplicas(ctx)
	if err != nil {
		log.Printf("read spec failed: %v; skip tick", err)
		return
	}
	st.Current = current

	waiting, okW, errW := promClient.QueryScaler(ctx, autoscaler.WaitingQuery(model))
	running, okR, errR := promClient.QueryScaler(ctx, autoscaler.RunningQuery(model))
	actual, okA, errA := promClient.QueryScaler(ctx, autoscaler.ActualQuery(model))

	if errW != nil || errR != nil || errA != nil {
		log.Printf("prometheus error (waiting=%v, running=%v, actual=%v)", errW, errR, errA)
	}
	hasData := errW == nil && errR == nil && errA == nil && okW && okR && okA
	st.Actual = int(actual)

	dec, next := autoscaler.Decide(cfg, *st, autoscaler.Signal{
		WaitingAvg: waiting,
		Running:    running,
		HasData:    hasData,
	}, time.Now())
	*st = next

	if !dec.Act {
		log.Printf("hold replicas=%d actual=%d waiting=%.1f running=%.1f reason=%q", st.Current, st.Actual, waiting, running, dec.Reason)
		return
	}

	prefix := ""
	if dryRun {
		prefix = "[DRY-RUN] would "
	}
	log.Printf("%sscale %d -> %d reason=%q", prefix, st.Current, dec.Target, dec.Reason)
	if err := act.PatchReplicas(ctx, dec.Target, dec.Reason); err != nil {
		// log it and path in next tick
		log.Printf("patch failed: %v", err)
	}
}
