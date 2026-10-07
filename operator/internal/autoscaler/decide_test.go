package autoscaler

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

var testCfg = Config{
	Min: 1, Max: 2,
	UpThreshold: 4, UpSamples: 2,
	IdleRunning: 1, DownWindow: 5 * time.Minute, UpCooldown: 5 * time.Minute,
}

var t0 = time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

func overload() Signal { return Signal{WaitingAvg: 12, Running: 60, HasData: true} }
func idle() Signal     { return Signal{WaitingAvg: 0, Running: 0, HasData: true} }
func noData() Signal   { return Signal{HasData: false} }

// tick 把多拍序列驱动器跑起来：每拍重设 Current/Actual（对应 main 每 tick 重读 spec），
// upBreaches/idleSince/LastScale* 则由 Decide 跨拍携带——这正是要测的防抖状态机。
type tick struct {
	at           time.Duration
	sig          Signal
	current      int
	actual       int
	wantAct      bool
	wantTarget   int
	wantReasonIn string
}

func runScenario(t *testing.T, cfg Config, ticks []tick) {
	t.Helper()
	st := State{}
	for i, tc := range ticks {
		st.Current = tc.current
		st.Actual = tc.actual
		dec, next := Decide(cfg, st, tc.sig, t0.Add(tc.at))
		if dec.Act != tc.wantAct {
			t.Errorf("tick %d: Act=%v want %v (reason=%q)", i, dec.Act, tc.wantAct, dec.Reason)
		}
		if dec.Act && dec.Target != tc.wantTarget {
			t.Errorf("tick %d: Target=%d want %d", i, dec.Target, tc.wantTarget)
		}
		if tc.wantReasonIn != "" && !strings.Contains(dec.Reason, tc.wantReasonIn) {
			t.Errorf("tick %d: reason %q missing %q", i, dec.Reason, tc.wantReasonIn)
		}
		st = next
	}
}

// 单样本超限不动手。
func TestSingleBreachDoesNotAct(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: overload(), current: 1, actual: 1, wantAct: false, wantReasonIn: "busy"},
	})
}

// 连续 2 样本越限 → 1->2（场景 A 的决策路径）。
func TestConsecutiveBreachesScaleUp(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: overload(), current: 1, actual: 1, wantAct: false},
		{at: 5 * time.Second, sig: overload(), current: 1, actual: 1, wantAct: true, wantTarget: 2, wantReasonIn: "consecutive samples"},
	})
}

// 中间夹一个正常样本，证据链断开重新计（毛刺不扩容）。
func TestBreachChainBrokenByGoodSample(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: overload(), current: 1, actual: 1, wantAct: false},
		{at: 5 * time.Second, sig: idle(), current: 1, actual: 1, wantAct: false},
		{at: 10 * time.Second, sig: overload(), current: 1, actual: 1, wantAct: false, wantReasonIn: "busy"},
	})
}

// 到顶不动手（max 即 GPU 供给quota上限）。
func TestAtMaxDoesNotAct(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: overload(), current: 2, actual: 2, wantAct: false},
		{at: 5 * time.Second, sig: overload(), current: 2, actual: 2, wantAct: false, wantReasonIn: "max"},
	})
}

// 不变量 2：扩容未落地（actual < spec）不追加——等待中的扩容不是扩容失败的证据。
func TestInFlightScaleUpNotPiledOn(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: overload(), current: 2, actual: 1, wantAct: false},
		{at: 5 * time.Second, sig: overload(), current: 2, actual: 1, wantAct: false, wantReasonIn: "in flight"},
	})
}

// 静默满窗口 → 2->1；窗口未满不动（场景 B 的两侧都要证）。
func TestIdleFullWindowScalesDown(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: idle(), current: 2, actual: 2, wantAct: false, wantReasonIn: "idle window starts"},
		{at: 4 * time.Minute, sig: idle(), current: 2, actual: 2, wantAct: false, wantReasonIn: "down_window"},
		{at: 5 * time.Minute, sig: idle(), current: 2, actual: 2, wantAct: true, wantTarget: 1, wantReasonIn: "idle"},
	})
}

// 静默中断窗口重计：来一次请求，5 分钟计时从头开始。
func TestIdleWindowResetByTraffic(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: idle(), current: 2, actual: 2, wantAct: false},
		{at: 4 * time.Minute, sig: Signal{WaitingAvg: 0, Running: 3, HasData: true}, current: 2, actual: 2, wantAct: false, wantReasonIn: "busy"},
		{at: 5 * time.Minute, sig: idle(), current: 2, actual: 2, wantAct: false, wantReasonIn: "idle window starts"},
		{at: 10 * time.Minute, sig: idle(), current: 2, actual: 2, wantAct: true, wantTarget: 1},
	})
}

// 兜底running收尾长请求：waiting=0 且 running<=IdleRunning 仍算空闲。
func TestIdleToleratesStraggler(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: Signal{WaitingAvg: 0, Running: 0.8, HasData: true}, current: 2, actual: 2, wantAct: false, wantReasonIn: "idle window starts"},
	})
}

// min=1 是硬底，不能缩到0
func TestAtMinNeverScalesDown(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: idle(), current: 1, actual: 1, wantAct: false},
		{at: 10 * time.Minute, sig: idle(), current: 1, actual: 1, wantAct: false, wantReasonIn: "min"},
	})
}

// 扩后冷却期内静默满窗也不许缩；冷却过了才缩。
// （DownWindow=2m < UpCooldown=5m，才能单独露出冷却这一支。）
func TestUpCooldownBlocksScaleDown(t *testing.T) {
	cfg := testCfg
	cfg.DownWindow = 2 * time.Minute
	runScenario(t, cfg, []tick{
		{at: 0, sig: overload(), current: 1, actual: 1, wantAct: false},
		{at: 5 * time.Second, sig: overload(), current: 1, actual: 1, wantAct: true, wantTarget: 2},
		{at: 10 * time.Second, sig: idle(), current: 2, actual: 2, wantAct: false, wantReasonIn: "idle window starts"},
		{at: 3 * time.Minute, sig: idle(), current: 2, actual: 2, wantAct: false, wantReasonIn: "up_cooldown"},
		{at: 6 * time.Minute, sig: idle(), current: 2, actual: 2, wantAct: true, wantTarget: 1},
	})
}

// 无数据保持现状
func TestNoDataHoldsAndResetsChain(t *testing.T) {
	runScenario(t, testCfg, []tick{
		{at: 0, sig: overload(), current: 1, actual: 1, wantAct: false},
		{at: 5 * time.Second, sig: noData(), current: 1, actual: 1, wantAct: false, wantReasonIn: "no metrics data"},
		{at: 10 * time.Second, sig: overload(), current: 1, actual: 1, wantAct: false, wantReasonIn: "busy"},
		{at: 15 * time.Second, sig: overload(), current: 1, actual: 1, wantAct: true, wantTarget: 2},
	})
}

func TestActuator(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "maas.chr1sgong.io", Version: "v1alpha1", Kind: "Model",
	}, &unstructured.Unstructured{})
	m := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "maas.chr1sgong.io/v1alpha1",
		"kind":       "Model",
		"metadata":   map[string]any{"name": "qwen2.5-7b-awq", "namespace": "mini-maas"},
		"spec":       map[string]any{"serving": map[string]any{"replicas": int64(1)}},
	}}
	cli := fake.NewSimpleDynamicClient(scheme, m)
	ctx := context.Background()

	a := NewActuator(cli, "mini-maas", "qwen2.5-7b-awq", false)
	got, err := a.CurrentReplicas(ctx)
	if err != nil || got != 1 {
		t.Fatalf("CurrentReplicas = %d, %v; want 1, nil", got, err)
	}
	if err := a.PatchReplicas(ctx, 2, "waiting_avg=12.0 >= 4.0 for 2 consecutive samples"); err != nil {
		t.Fatalf("PatchReplicas: %v", err)
	}
	got, err = a.CurrentReplicas(ctx)
	if err != nil || got != 2 {
		t.Fatalf("after patch CurrentReplicas = %d, %v; want 2, nil", got, err)
	}
	// 重新 Get：fake tracker 存的是深拷贝，直接读最初构造的 m.Object 是旧数据。
	fresh, err := cli.Resource(modelGVR).Namespace("mini-maas").Get(ctx, "qwen2.5-7b-awq", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after patch: %v", err)
	}
	reason, found, err := unstructured.NestedString(fresh.Object, "metadata", "annotations", "maas.chr1sgong.io/autoscale-reason")
	if err != nil || !found || !strings.Contains(reason, "waiting_avg") {
		t.Fatalf("reason annotation = %q, %v, %v; want waiting_avg...", reason, found, err)
	}

	// dry-run 只读不写
	dry := NewActuator(cli, "mini-maas", "qwen2.5-7b-awq", true)
	if err := dry.PatchReplicas(ctx, 1, "should not land"); err != nil {
		t.Fatalf("dry-run PatchReplicas: %v", err)
	}
	got, _ = a.CurrentReplicas(ctx)
	if got != 2 {
		t.Fatalf("dry-run mutated replicas to %d", got)
	}
}
