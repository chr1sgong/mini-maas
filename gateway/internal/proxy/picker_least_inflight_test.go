package proxy

import "testing"

func TestLeastInflightPicker(t *testing.T) {
	tracker := NewInflightTracker()
	picker := &LeastInflightPicker{tracker: tracker}
	endpoints := []string{"a", "b"}

	// 全 0 平局 → 端列表首个（行为确定）
	d1 := picker.Pick(testParams("m", 0), endpoints)
	if d1.Endpoint != "a" || d1.Reason != ReasonLeastInflight {
		t.Fatalf("first pick = %q (%q), want a (%q)", d1.Endpoint, d1.Reason, ReasonLeastInflight)
	}
	// a=1 → 选 b
	d2 := picker.Pick(testParams("m", 0), endpoints)
	if d2.Endpoint != "b" {
		t.Fatalf("second pick = %q, want b", d2.Endpoint)
	}
	// a 释放后 a=0,b=1 → 回到 a
	d1.ReleaseFn()
	d3 := picker.Pick(testParams("m", 0), endpoints)
	if d3.Endpoint != "a" {
		t.Fatalf("third pick = %q, want a after release", d3.Endpoint)
	}

	d2.ReleaseFn()
	d3.ReleaseFn()
	for ep, n := range tracker.Snapshot() {
		if n != 0 {
			t.Fatalf("inflight leak on %q: %d", ep, n)
		}
	}
}
