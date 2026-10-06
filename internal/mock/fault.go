package mock

import (
	"math/rand"
	"time"
)

// RandomJitter returns a random duration in [0, maxMs] milliseconds — split
// out as its own pure function so its bounds are unit-testable without an
// HTTP round-trip.
func RandomJitter(maxMs int) time.Duration {
	if maxMs <= 0 {
		return 0
	}
	return time.Duration(rand.Intn(maxMs+1)) * time.Millisecond
}

// RollFault decides, for one request, whether FaultConfig fires: "timeout"
// (drop the connection instead of responding), "error" (substitute one of
// ErrorStatusCodes for the real response), or "" (no fault this time — the
// real response, still possibly delayed by LatencyJitterMs, proceeds).
// Timeout is checked before error since a dropped connection preempts any
// status code that would otherwise have been written.
func RollFault(cfg *FaultConfig) string {
	if cfg == nil {
		return ""
	}
	if cfg.TimeoutRatePercent > 0 && rand.Float64()*100 < cfg.TimeoutRatePercent {
		return "timeout"
	}
	if cfg.ErrorRatePercent > 0 && rand.Float64()*100 < cfg.ErrorRatePercent {
		return "error"
	}
	return ""
}

// PickErrorStatus chooses one of FaultConfig's configured error codes at
// random, defaulting to 500 if none were configured.
func PickErrorStatus(cfg *FaultConfig) int {
	if len(cfg.ErrorStatusCodes) == 0 {
		return 500
	}
	return cfg.ErrorStatusCodes[rand.Intn(len(cfg.ErrorStatusCodes))]
}
