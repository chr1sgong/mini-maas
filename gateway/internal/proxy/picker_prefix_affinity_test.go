package proxy

import "testing"

func TestPrefixAffinityStickySameKey(t *testing.T) {
	tracker := NewInflightTracker()
	picker := &PrefixAffinityPicker{tracker: tracker, maxInflight: 8}
	endpoints := []string{"a", "b", "c"}

	first := ""
	for range 5 {
		d := picker.Pick(testParams("m", 42), endpoints)
		if first == "" {
			first = d.Endpoint
		}
		if d.Endpoint != first {
			t.Fatalf("same key scattered: %q vs %q", d.Endpoint, first)
		}
		if d.Reason != ReasonAffinity {
			t.Fatalf("reason = %q, want %q", d.Reason, ReasonAffinity)
		}
		d.ReleaseFn()
	}
}

func TestPrefixAffinitySpreadsAcrossKeys(t *testing.T) {
	tracker := NewInflightTracker()
	picker := &PrefixAffinityPicker{tracker: tracker, maxInflight: 8}
	endpoints := []string{"a", "b", "c"}

	seen := map[string]int{}
	for k := uint64(1); k <= 100; k++ {
		d := picker.Pick(testParams("m", k*7919), endpoints) // 乘质数打散
		seen[d.Endpoint]++
		d.ReleaseFn()
	}
	if len(seen) < 2 {
		t.Fatalf("100 distinct keys landed on only %d endpoint(s)", len(seen))
	}
}

// TestRendezvousMinimalDisruption 严格断言 HRW 的核心性质：
// 摘掉一个端时，原属存活端的 key 一个都不许动，只有被摘端的 key 重新分配
// （且占比 ~1/N）。这是 D11 扩容时保住缓存命中率的关键，hash%N 做不到。
func TestRendezvousMinimalDisruption(t *testing.T) {
	three := []string{"a", "b", "c"}
	two := []string{"a", "b"} // 摘 c

	const keys = 3000
	moved := 0
	for k := uint64(0); k < keys; k++ {
		before := rendezvous(k, three)
		after := rendezvous(k, two)
		if before != after {
			moved++
			if before != "c" {
				t.Fatalf("key %d moved off surviving endpoint %q -> %q: HRW property broken",
					k, before, after)
			}
		}
	}
	if ratio := float64(moved) / keys; ratio < 0.2 || ratio > 0.5 {
		t.Fatalf("moved ratio %.2f, want roughly 1/3", ratio)
	}
}

func TestPrefixAffinityFallbackWhenSaturated(t *testing.T) {
	tracker := NewInflightTracker()
	picker := &PrefixAffinityPicker{tracker: tracker, maxInflight: 2}
	endpoints := []string{"a", "b"}
	const key = 42
	preferred := rendezvous(key, endpoints)

	// 同 key 把首选端压到 cap
	hold := make([]func(), 0, 2)
	for range 2 {
		d := picker.Pick(testParams("m", key), endpoints)
		if d.Endpoint != preferred || d.Reason != ReasonAffinity {
			t.Fatalf("warm-up pick = %q (%q), want %q (%q)", d.Endpoint, d.Reason, preferred, ReasonAffinity)
		}
		hold = append(hold, d.ReleaseFn)
	}

	// 首选端已满：降级 least-inflight 扫全表，必须落到另一端
	d := picker.Pick(testParams("m", key), endpoints)
	if d.Reason != ReasonAffinityFallback {
		t.Fatalf("reason = %q, want %q", d.Reason, ReasonAffinityFallback)
	}
	if d.Endpoint == preferred {
		t.Fatalf("saturated endpoint still picked")
	}
	d.ReleaseFn()

	for _, release := range hold {
		release()
	}
	if n := tracker.Snapshot()[preferred]; n != 0 {
		t.Fatalf("leak on preferred: %d", n)
	}
}

// key==0（无可亲和内容）退化为 least-inflight，reason 如实写——
// D13 统计亲和命中率时这类请求不算命中也不算 fallback。
func TestPrefixAffinityNoKey(t *testing.T) {
	tracker := NewInflightTracker()
	picker := &PrefixAffinityPicker{tracker: tracker, maxInflight: 8}

	d := picker.Pick(testParams("m", 0), []string{"a", "b"})
	if d.Reason != ReasonLeastInflight {
		t.Fatalf("reason = %q, want %q", d.Reason, ReasonLeastInflight)
	}
	d.ReleaseFn()
}
