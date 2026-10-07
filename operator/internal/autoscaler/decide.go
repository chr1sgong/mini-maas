package autoscaler

import (
	"fmt"
	"log"
	"time"
)

type Config struct {
	Min         int     // 最小缩到多少实例
	Max         int     // 最多扩到多少实例
	UpThreshold float64 // waiting队列均值超过的阈值
	UpSamples   int     // 连续超过阈值的样本数, 持续超过UpThreshold UpSamples次才触发扩容
	IdleRunning float64
	DownWindow  time.Duration // 持续空闲满足窗口时间才缩容
	UpCooldown  time.Duration // 扩容后冷却期内不允许缩容 (防止"扩完就缩容 -> 出发扩容"的死循环)
}

// 扩缩容决策输入
type Signal struct {
	WaitingAvg float64
	Running    float64
	HasData    bool
}

type State struct {
	Current       int // 当前的spec.serving.replicas
	Actual        int // replicas_actual
	LastScaleUp   time.Time
	LastScaleDown time.Time
	upReaches     int // 达到或超过阈值的次数
	idleSince     time.Time
}

type Decision struct {
	Act    bool
	Target int
	Reason string
}

func hold(st State, format string, args ...any) (Decision, State) {
	return Decision{Act: false, Target: st.Current, Reason: fmt.Sprintf(format, args...)}, st
}

// Decide实现
// 1. 一次只动一个副本
// 2. 扩容未落地(Actual < Current), 不再扩,等落地
// 3. 扩容落地后UpCooldown内不缩容
func Decide(cfg Config, st State, sig Signal, now time.Time) (Decision, State) {
	if !sig.HasData {
		// 没数据,不操作
		st.upReaches = 0
		st.idleSince = time.Time{}
		return hold(st, "no metrics data; holding replicas=%d", st.Current)
	}

	if sig.WaitingAvg >= cfg.UpThreshold {
		st.upReaches++
	} else {
		st.upReaches = 0
	}
	log.Printf("decision: upReaches=%d cfg.UpSamples=%d", st.upReaches, cfg.UpSamples)
	if st.upReaches >= cfg.UpSamples {
		// 持续采样超阈值次数达到对应次数了
		st.upReaches = 0
		switch {
		case st.Actual < st.Current:
			// 上一次扩容还没跑完
			return hold(st, "waiting_avg=%.1f >= %.1f but previous scale-up still in flight (actual=%d, spec=%d)", sig.WaitingAvg, cfg.UpThreshold, st.Actual, st.Current)
		case st.Current >= cfg.Max:
			return hold(st, "waiting_avg=%.1f >= %.1f but replicas already reaches max=%d", sig.WaitingAvg, cfg.UpThreshold, cfg.Max)
		default:
			st.LastScaleUp = now
			st.idleSince = time.Time{} // 扩容中,把idle时间清零
			return Decision{Act: true, Target: st.Current + 1, Reason: fmt.Sprintf("waiting_avg=%.1f >= %.1f for %d consecutive samples", sig.WaitingAvg, cfg.UpThreshold, cfg.UpSamples)}, st
		}
	}

	idle := sig.WaitingAvg == 0 && sig.Running <= cfg.IdleRunning
	if !idle {
		st.idleSince = time.Time{}
		return hold(st, "busy: waiting_avg=%.1f running=%.1f", sig.WaitingAvg, sig.Running)
	}
	if st.idleSince.IsZero() {
		st.idleSince = now
		return hold(st, "idle window starts (waiting=0 running=%.1f)", sig.Running)
	}
	if now.Sub(st.idleSince) < cfg.DownWindow {
		return hold(st, "idle for %s < down_window %s", now.Sub(st.idleSince).Round(time.Second), cfg.DownWindow)
	}

	switch {
	case st.Current <= cfg.Min:
		return hold(st, "idle past window but replicas already reaches at min=%d", cfg.Min)
	case now.Sub(st.LastScaleUp) < cfg.UpCooldown:
		return hold(st, "idle past window but in up_cooldown (last_scale_up=%s)", st.LastScaleUp)
	default:
		st.LastScaleDown = now
		st.idleSince = time.Time{} // 缩容中,把idle时间清零
		return Decision{Act: true, Target: st.Current - 1, Reason: fmt.Sprintf("idle for %s >= down_window %s", now.Sub(st.idleSince).Round(time.Second), cfg.DownWindow)}, st
	}
}
