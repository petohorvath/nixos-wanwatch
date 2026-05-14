package selector

// HysteresisState is the per-WAN consecutive-cycle state machine
// that gates the externally-visible Healthy verdict. The probe
// layer feeds raw "observed-healthy this cycle" booleans into
// Observe; the verdict only flips when a configurable number of
// consecutive observations cross the threshold in the new direction.
//
// Initial verdict is `false`. Once observations start, the verdict
// flips to true only after `consecutiveUp` healthy observations in
// a row, and back to false only after `consecutiveDown` unhealthy
// ones. The first observation is special: per PLAN §8 the cold-start
// handoff seeds the verdict straight from the measured Health (see
// Seed), so a (WAN, family) need not climb the up-ramp from scratch
// when its first probe Window lands healthy.
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

// Seed initializes the verdict directly from the first observed
// Health, bypassing the consecutive-cycle ramp — the PLAN §8
// cold-start handoff. Until its first probe Window completes a
// (WAN, family) is trusted via carrier alone; when that Window
// lands, climbing to the measured Health over `consecutiveUp`
// cycles would spuriously flap a healthy WAN down→up during
// warm-up, so the verdict adopts it immediately. Counters are
// zeroed, so the next flip still needs a full consecutive run.
// Call for the first observation only; Observe thereafter. Returns
// the seeded verdict.
func (h *HysteresisState) Seed(observedHealthy bool) bool {
	h.healthy = observedHealthy
	h.healthyCount = 0
	h.unhealthyCount = 0
	return h.healthy
}

// Healthy returns the current externally-visible verdict without
// recording an observation.
func (h *HysteresisState) Healthy() bool { return h.healthy }
