package mock

import "testing"

func TestRandomJitterStaysWithinConfiguredBounds(t *testing.T) {
	const maxMs = 25
	for i := 0; i < 500; i++ {
		d := RandomJitter(maxMs)
		if d < 0 {
			t.Fatalf("jitter must never be negative, got %v", d)
		}
		if d.Milliseconds() > int64(maxMs) {
			t.Fatalf("jitter %v exceeds configured max %dms", d, maxMs)
		}
	}
}

func TestRandomJitterZeroWhenUnconfigured(t *testing.T) {
	if d := RandomJitter(0); d != 0 {
		t.Fatalf("expected 0 jitter for maxMs=0, got %v", d)
	}
	if d := RandomJitter(-5); d != 0 {
		t.Fatalf("expected 0 jitter for a negative maxMs, got %v", d)
	}
}

func TestPickErrorStatusDefaultsTo500WhenUnconfigured(t *testing.T) {
	if got := PickErrorStatus(&FaultConfig{}); got != 500 {
		t.Fatalf("expected default 500, got %d", got)
	}
}

func TestPickErrorStatusChoosesFromConfiguredCodes(t *testing.T) {
	codes := []int{502, 503, 504}
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		got := PickErrorStatus(&FaultConfig{ErrorStatusCodes: codes})
		found := false
		for _, c := range codes {
			if got == c {
				found = true
			}
		}
		if !found {
			t.Fatalf("PickErrorStatus returned %d, not one of %v", got, codes)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected some variety across 200 picks from %v, only saw %v", codes, seen)
	}
}

func TestRollFaultRespectsPrecedenceAndZeroRates(t *testing.T) {
	if got := RollFault(nil); got != "" {
		t.Fatalf("expected no fault for a nil config, got %q", got)
	}
	if got := RollFault(&FaultConfig{}); got != "" {
		t.Fatalf("expected no fault when both rates are 0, got %q", got)
	}
	if got := RollFault(&FaultConfig{TimeoutRatePercent: 100}); got != "timeout" {
		t.Fatalf("expected a guaranteed timeout at 100%%, got %q", got)
	}
	if got := RollFault(&FaultConfig{ErrorRatePercent: 100}); got != "error" {
		t.Fatalf("expected a guaranteed error at 100%%, got %q", got)
	}
	// Timeout takes precedence when both are maxed out — a dropped
	// connection preempts any status code that would otherwise be written.
	if got := RollFault(&FaultConfig{TimeoutRatePercent: 100, ErrorRatePercent: 100}); got != "timeout" {
		t.Fatalf("expected timeout to take precedence, got %q", got)
	}
}
