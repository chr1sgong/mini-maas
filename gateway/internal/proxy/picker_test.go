package proxy

import (
	"sync"
	"testing"
)

func TestRoundRobinPickerPick(t *testing.T) {
	picker := NewRoundRobinPicker()
	endpoints := []string{"a", "b", "c"}

	if got := picker.Pick("model-a", endpoints, nil); got != "a" {
		t.Fatalf("first pick = %q, want %q", got, "a")
	}
	if got := picker.Pick("model-b", endpoints, nil); got != "a" {
		t.Fatalf("first pick for another model = %q, want %q", got, "a")
	}
	if got := picker.Pick("model-a", endpoints, nil); got != "b" {
		t.Fatalf("second pick = %q, want %q", got, "b")
	}
	if got := picker.Pick("model-a", nil, nil); got != "" {
		t.Fatalf("pick with no endpoints = %q, want empty string", got)
	}
	if got := picker.Pick("model-a", endpoints, nil); got != "c" {
		t.Fatalf("third pick = %q, want %q", got, "c")
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
			endpoint := picker.Pick("model", endpoints, nil)
			countsMu.Lock()
			counts[endpoint]++
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
