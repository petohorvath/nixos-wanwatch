package selector

// HysteresisState is the per-WAN consecutive-cycle state machine
// that gates the externally-visible Healthy verdict. The probe
// layer feeds raw "observed-healthy this cycle" booleans into
// Observe; the verdict only flips when a configurable number of
// consecutive observations cross the threshold in the new direction.
//
// Initial verdict is `false` (not healthy). The first
// `consecutiveUp` healthy observations are required before the
// verdict flips to true. Once true, `consecutiveDown` unhealthy
// observations are required to flip it back. This matches the PLAN
// §8 "Cold-start behavior" — we don't ship traffic via an unproven
// WAN.
//
// Not safe for concurrent use. Wrap externally if multiple
// goroutines observe the same state.
type HysteresisState struct {
	healthyCount   int
	unhealthyCount int
	healthy        bool
}

// NewHysteresisState returns a fresh state.
func NewHysteresisState() *HysteresisState {
	return &HysteresisState{}
}

// Observe records one observation and returns the externally-visible
// verdict after it. The verdict changes only when this observation
// crosses the configured threshold.
//
// `consecutiveUp` and `consecutiveDown` come from the per-WAN
// `probe.hysteresis` config and must be ≥ 1; values ≤ 0 collapse
// the threshold so verdicts flip on the first observation in that
// direction.
func (h *HysteresisState) Observe(observedHealthy bool, consecutiveUp, consecutiveDown int) bool {
	if observedHealthy {
		h.unhealthyCount = 0
		h.healthyCount++
		if !h.healthy && h.healthyCount >= consecutiveUp {
			h.healthy = true
		}
	} else {
		h.healthyCount = 0
		h.unhealthyCount++
		if h.healthy && h.unhealthyCount >= consecutiveDown {
			h.healthy = false
		}
	}
	return h.healthy
}

// Healthy returns the current externally-visible verdict without
// recording an observation.
func (h *HysteresisState) Healthy() bool { return h.healthy }
