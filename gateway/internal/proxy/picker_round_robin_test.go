package proxy

import (
	"sync"
	"testing"
)

func TestRoundRobinPickerPick(t *testing.T) {
	picker := NewRoundRobinPicker()
	endpoints := []string{"a", "b", "c"}

	cases := []struct {
		model string
		want  string
	}{
		{"model-a", "a"}, {"model-b", "a"}, {"model-a", "b"}, {"model-a", "c"}, {"model-a", "a"},
	}
	for i, c := range cases {
		d := picker.Pick(testParams(c.model, 0), endpoints)
		if d.Endpoint != c.want {
			t.Fatalf("pick #%d = %q, want %q", i, d.Endpoint, c.want)
		}
		if d.Reason != ReasonRoundRobin {
			t.Fatalf("reason = %q, want %q", d.Reason, ReasonRoundRobin)
		}
		d.ReleaseFn() // no-op，但必须调用
	}

	if d := picker.Pick(testParams("model-a", 0), nil); d.Endpoint != "" {
		t.Fatalf("pick with no endpoints = %q, want empty", d.Endpoint)
	}
}

func TestRoundRobinPickerConcurrent(t *testing.T) {
	const requests = 3000
	picker := NewRoundRobinPicker()
	endpoints := []string{"a", "b", "c"}
	counts := make(map[string]int, len(endpoints))
	var countsMu sync.Mutex
	var wg sync.WaitGroup

	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := picker.Pick(testParams("model", 0), endpoints)
			countsMu.Lock()
			counts[d.Endpoint]++
			countsMu.Unlock()
		}()
	}
	wg.Wait()

	for _, endpoint := range endpoints {
		if got := counts[endpoint]; got != requests/len(endpoints) {
			t.Errorf("pick count for %q = %d, want %d", endpoint, got, requests/len(endpoints))
		}
	}
}
