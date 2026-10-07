package proxy

import (
	"testing"

	"github.com/mini-maas/gateway/internal/openai"
)

func testParams(model string, key uint64) *openai.InferenceParams {
	return &openai.InferenceParams{Model: model, AffinityKey: key}
}

func TestNewPickerUnknownStrategy(t *testing.T) {
	if _, err := NewPicker("bogus", NewInflightTracker(), 8); err == nil {
		t.Fatal("unknown strategy should fail at startup")
	}
	for _, s := range []string{"round_robin", "least_inflight", "prefix_affinity"} {
		if _, err := NewPicker(s, NewInflightTracker(), 8); err != nil {
			t.Fatalf("strategy %q should construct: %v", s, err)
		}
	}
}
