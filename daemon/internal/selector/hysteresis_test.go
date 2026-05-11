package selector

import "testing"

func TestHysteresisStartsUnhealthy(t *testing.T) {
	t.Parallel()
	h := NewHysteresisState()
	if h.Healthy() {
		t.Error("fresh HysteresisState reports healthy; want !healthy")
	}
}

// TestHysteresisFlipsUpAfterConsecutiveUp: must observe `consecutiveUp`
// healthy in a row before flipping to true.
func TestHysteresisFlipsUpAfterConsecutiveUp(t *testing.T) {
	t.Parallel()
	const (
		up   = 3
		down = 2
	)

	tests := []struct {
		name         string
		observations []bool
		wantFinal    bool
	}{
		{"one up not enough", []bool{true}, false},
		{"two up not enough", []bool{true, true}, false},
		{"three up flips", []bool{true, true, true}, true},
		{"four up stays up", []bool{true, true, true, true}, true},
		{"flap resets up counter", []bool{true, true, false, true, true}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewHysteresisState()
			var got bool
			for _, o := range tc.observations {
				got = h.Observe(o, up, down)
			}
			if got != tc.wantFinal {
				t.Errorf("after %v: got healthy=%v, want %v", tc.observations, got, tc.wantFinal)
			}
		})
	}
}

// TestHysteresisFlipsDownAfterConsecutiveDown: must observe `consecutiveDown`
// unhealthy in a row before flipping back to false.
func TestHysteresisFlipsDownAfterConsecutiveDown(t *testing.T) {
	t.Parallel()
	const (
		up   = 2
		down = 3
	)

	// Helper: flip a fresh state up via `up` observations, then run the
	// scenario's down observations.
	runFromHealthy := func(downObs []bool) bool {
		h := NewHysteresisState()
		for i := 0; i < up; i++ {
			h.Observe(true, up, down)
		}
		if !h.Healthy() {
			t.Fatal("setup: state not healthy after consecutive-up observations")
		}
		var last bool
		for _, o := range downObs {
			last = h.Observe(o, up, down)
		}
		return last
	}

	tests := []struct {
		name      string
		downObs   []bool
		wantFinal bool
	}{
		{"one down doesn't flip", []bool{false}, true},
		{"two down doesn't flip", []bool{false, false}, true},
		{"three down flips", []bool{false, false, false}, false},
		{"down then recovery resets", []bool{false, true, false, false}, true},
		{"sustained down flips", []bool{false, false, false, false}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runFromHealthy(tc.downObs)
			if got != tc.wantFinal {
				t.Errorf("after %v (from healthy): got healthy=%v, want %v", tc.downObs, got, tc.wantFinal)
			}
		})
	}
}

// TestHysteresisFlapSuppression: rapid up/down/up/down observations
// must not push the verdict past either threshold.
func TestHysteresisFlapSuppression(t *testing.T) {
	t.Parallel()
	h := NewHysteresisState()
	const (
		up   = 3
		down = 3
	)
	// 20 alternating observations starting with healthy.
	for i := 0; i < 20; i++ {
		h.Observe(i%2 == 0, up, down)
	}
	if h.Healthy() {
		t.Error("alternating observations should never reach consecutive-up threshold; healthy=true")
	}
}

func TestHysteresisObserveReturnsCurrentVerdict(t *testing.T) {
	t.Parallel()
	h := NewHysteresisState()
	const (
		up   = 2
		down = 2
	)
	// Single healthy doesn't flip.
	if v := h.Observe(true, up, down); v {
		t.Errorf("after 1 healthy obs: verdict=%v, want false", v)
	}
	// Second healthy flips.
	if v := h.Observe(true, up, down); !v {
		t.Errorf("after 2 healthy obs: verdict=%v, want true", v)
	}
	// Healthy() and Observe agree.
	if h.Healthy() != true {
		t.Errorf("Healthy() = false, want true")
	}
}

// TestHysteresisThresholdsOfOneFlipImmediately: a threshold of 1
// means the first observation in that direction flips the verdict.
// This is the most aggressive setting; useful baseline.
func TestHysteresisThresholdsOfOneFlipImmediately(t *testing.T) {
	t.Parallel()
	h := NewHysteresisState()
	if v := h.Observe(true, 1, 1); !v {
		t.Errorf("threshold=1: first healthy obs did not flip; verdict=%v", v)
	}
	if v := h.Observe(false, 1, 1); v {
		t.Errorf("threshold=1: first unhealthy obs after up did not flip; verdict=%v", v)
	}
}
