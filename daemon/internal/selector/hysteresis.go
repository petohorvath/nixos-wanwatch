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
	healthyCount    int
	unhealthyCount  int
	healthy         bool
	consecutiveUp   int
	consecutiveDown int
}

// NewHysteresisState returns a fresh state with the given
// thresholds. Each threshold is clamped to ≥ 1 so a misconfigured
// (≤ 0) value collapses to "flip on the first observation"
// rather than corrupting the comparison; the Nix layer is the
// authoritative validator.
func NewHysteresisState(consecutiveUp, consecutiveDown int) *HysteresisState {
	if consecutiveUp < 1 {
		consecutiveUp = 1
	}
	if consecutiveDown < 1 {
		consecutiveDown = 1
	}
	return &HysteresisState{
		consecutiveUp:   consecutiveUp,
		consecutiveDown: consecutiveDown,
	}
}

// Observe records one observation and returns the
// externally-visible verdict after it. The verdict changes only
// when this observation crosses the threshold configured at
// construction.
func (h *HysteresisState) Observe(observedHealthy bool) bool {
	if observedHealthy {
		h.unhealthyCount = 0
		h.healthyCount++
		if !h.healthy && h.healthyCount >= h.consecutiveUp {
			h.healthy = true
		}
	} else {
		h.healthyCount = 0
		h.unhealthyCount++
		if h.healthy && h.unhealthyCount >= h.consecutiveDown {
			h.healthy = false
		}
	}
	return h.healthy
}

// Healthy returns the current externally-visible verdict without
// recording an observation.
func (h *HysteresisState) Healthy() bool { return h.healthy }
