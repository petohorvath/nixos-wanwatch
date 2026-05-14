package selector

import (
	"math/rand"
	"testing"
)

func TestHysteresisStartsUnhealthy(t *testing.T) {
	t.Parallel()
	h := NewHysteresisState(3, 3)
	if h.Healthy() {
		t.Error("fresh HysteresisState reports healthy; want !healthy")
	}
}

// TestHysteresisFlipsUpAfterConsecutiveUp: must observe
// `consecutiveUp` healthy in a row before flipping to true.
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
			h := NewHysteresisState(up, down)
			var got bool
			for _, o := range tc.observations {
				got = h.Observe(o)
			}
			if got != tc.wantFinal {
				t.Errorf("after %v: got healthy=%v, want %v", tc.observations, got, tc.wantFinal)
			}
		})
	}
}

// TestHysteresisFlipsDownAfterConsecutiveDown: must observe
// `consecutiveDown` unhealthy in a row before flipping back to
// false.
func TestHysteresisFlipsDownAfterConsecutiveDown(t *testing.T) {
	t.Parallel()
	const (
		up   = 2
		down = 3
	)

	// Helper: flip a fresh state up via `up` observations, then run
	// the scenario's down observations.
	runFromHealthy := func(downObs []bool) bool {
		h := NewHysteresisState(up, down)
		for i := 0; i < up; i++ {
			h.Observe(true)
		}
		if !h.Healthy() {
			t.Fatal("setup: state not healthy after consecutive-up observations")
		}
		var last bool
		for _, o := range downObs {
			last = h.Observe(o)
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
	h := NewHysteresisState(3, 3)
	// 20 alternating observations starting with healthy.
	for i := 0; i < 20; i++ {
		h.Observe(i%2 == 0)
	}
	if h.Healthy() {
		t.Error("alternating observations should never reach consecutive-up threshold; healthy=true")
	}
}

func TestHysteresisObserveReturnsCurrentVerdict(t *testing.T) {
	t.Parallel()
	h := NewHysteresisState(2, 2)
	// Single healthy doesn't flip.
	if v := h.Observe(true); v {
		t.Errorf("after 1 healthy obs: verdict=%v, want false", v)
	}
	// Second healthy flips.
	if v := h.Observe(true); !v {
		t.Errorf("after 2 healthy obs: verdict=%v, want true", v)
	}
	// Healthy() and Observe agree.
	if !h.Healthy() {
		t.Errorf("Healthy() = false, want true")
	}
}

// TestHysteresisThresholdsOfOneFlipImmediately: a threshold of 1
// means the first observation in that direction flips the verdict.
// This is the most aggressive setting; useful baseline.
func TestHysteresisThresholdsOfOneFlipImmediately(t *testing.T) {
	t.Parallel()
	h := NewHysteresisState(1, 1)
	if v := h.Observe(true); !v {
		t.Errorf("threshold=1: first healthy obs did not flip; verdict=%v", v)
	}
	if v := h.Observe(false); v {
		t.Errorf("threshold=1: first unhealthy obs after up did not flip; verdict=%v", v)
	}
}

// TestHysteresisClampsNonPositiveThresholds: ≤0 inputs are clamped
// to 1 — defensive guard since the Nix layer is the real validator
// but a hand-edited config could slip a 0 through.
func TestHysteresisClampsNonPositiveThresholds(t *testing.T) {
	t.Parallel()
	h := NewHysteresisState(0, -5)
	if v := h.Observe(true); !v {
		t.Errorf("ctor-clamp: first healthy obs should flip; verdict=%v", v)
	}
	if v := h.Observe(false); v {
		t.Errorf("ctor-clamp: first unhealthy obs should flip back; verdict=%v", v)
	}
}

// TestHysteresisDeterminismProperty: a HysteresisState is a pure
// function over (consecutiveUp, consecutiveDown, observation
// sequence). Two fresh states fed identical inputs must produce
// identical verdict trajectories — every step, not just the
// final answer. Replays 50 random sequences as a property check.
func TestHysteresisDeterminismProperty(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(0xCAFE))
	for trial := 0; trial < 50; trial++ {
		up := 1 + rng.Intn(5)
		down := 1 + rng.Intn(5)
		seq := make([]bool, 50)
		for i := range seq {
			seq[i] = rng.Intn(2) == 0
		}

		a := NewHysteresisState(up, down)
		b := NewHysteresisState(up, down)
		for i, obs := range seq {
			va := a.Observe(obs)
			vb := b.Observe(obs)
			if va != vb {
				t.Fatalf("trial %d step %d: A=%v B=%v (up=%d down=%d, seq=%v)",
					trial, i, va, vb, up, down, seq[:i+1])
			}
		}
	}
}

// TestHysteresisFlipRateBound: a sequence of length N can produce
// at most ceil(N / min(up,down)) verdict flips. Catches a
// regression where a flap-suppression bug starts emitting flips
// proportional to the observation rate.
func TestHysteresisFlipRateBound(t *testing.T) {
	t.Parallel()
	const (
		up   = 3
		down = 3
	)
	rng := rand.New(rand.NewSource(0xBEEF))
	const n = 200
	h := NewHysteresisState(up, down)
	prev := h.Healthy()
	flips := 0
	for i := 0; i < n; i++ {
		verdict := h.Observe(rng.Intn(2) == 0)
		if verdict != prev {
			flips++
			prev = verdict
		}
	}
	// Tightest theoretical bound for `min(up,down)=3` is
	// `ceil(n / 3) = 67`. Real flip count is much lower because
	// random sequences rarely produce 3-in-a-row runs alternating.
	maxFlips := (n + 2) / 3
	if flips > maxFlips {
		t.Errorf("flips = %d, want ≤ %d (min-threshold bound on length %d)", flips, maxFlips, n)
	}
}

// TestHysteresisFlipsOnlyAfterRequiredRun: the function flips only
// after consecutive observations cross the configured threshold —
// any earlier flip is a bug. Audit every flip in a random trace
// and assert the previous K observations match the flip direction.
func TestHysteresisFlipsOnlyAfterRequiredRun(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(0x1337))
	for trial := 0; trial < 20; trial++ {
		up := 1 + rng.Intn(4)
		down := 1 + rng.Intn(4)
		seq := make([]bool, 100)
		for i := range seq {
			seq[i] = rng.Intn(2) == 0
		}

		h := NewHysteresisState(up, down)
		prev := h.Healthy()
		for i, obs := range seq {
			verdict := h.Observe(obs)
			if verdict == prev {
				continue
			}
			// Flip happened at index i. The required-run direction:
			// flipped up ⇒ last `up` observations must all be true;
			// flipped down ⇒ last `down` must all be false.
			required := up
			expected := true
			if !verdict {
				required = down
				expected = false
			}
			if i+1 < required {
				t.Errorf("trial %d: flip at i=%d but only %d obs seen", trial, i, i+1)
				continue
			}
			tail := seq[i+1-required : i+1]
			for j, o := range tail {
				if o != expected {
					t.Errorf("trial %d: flip at i=%d (to %v) but obs[%d]=%v in tail %v",
						trial, i, verdict, i+1-required+j, o, tail)
					break
				}
			}
			prev = verdict
		}
	}
}

// TestHysteresisSeedBypassesRamp: Seed sets the verdict directly
// from the first observation — no consecutive-cycle ramp. A state
// with consecutiveUp=3 seeded healthy is healthy at once, where
// Observe would need three healthy observations. This is the PLAN
// §8 cold-start handoff that keeps a healthy WAN from flapping
// during warm-up.
func TestHysteresisSeedBypassesRamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seed bool
	}{
		{"seed healthy", true},
		{"seed unhealthy", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewHysteresisState(3, 3)
			if got := h.Seed(tc.seed); got != tc.seed {
				t.Errorf("Seed(%v) = %v, want %v", tc.seed, got, tc.seed)
			}
			if h.Healthy() != tc.seed {
				t.Errorf("after Seed(%v): Healthy() = %v, want %v", tc.seed, h.Healthy(), tc.seed)
			}
		})
	}
}

// TestHysteresisSeedThenObserveRampsNormally: Seed sets the verdict
// but must leave the counters clean, so the next flip still needs a
// full consecutive run — a seeded-healthy state takes a whole
// `consecutiveDown` run to flip down, a seeded-unhealthy one a whole
// `consecutiveUp` run to flip up.
func TestHysteresisSeedThenObserveRampsNormally(t *testing.T) {
	t.Parallel()

	t.Run("seeded healthy needs full down-ramp", func(t *testing.T) {
		t.Parallel()
		h := NewHysteresisState(2, 3)
		h.Seed(true)
		if v := h.Observe(false); !v {
			t.Errorf("1 unhealthy after Seed(true): verdict=%v, want true", v)
		}
		if v := h.Observe(false); !v {
			t.Errorf("2 unhealthy after Seed(true): verdict=%v, want true", v)
		}
		if v := h.Observe(false); v {
			t.Errorf("3 unhealthy after Seed(true): verdict=%v, want false (flipped down)", v)
		}
	})

	t.Run("seeded unhealthy needs full up-ramp", func(t *testing.T) {
		t.Parallel()
		h := NewHysteresisState(3, 2)
		h.Seed(false)
		if v := h.Observe(true); v {
			t.Errorf("1 healthy after Seed(false): verdict=%v, want false", v)
		}
		if v := h.Observe(true); v {
			t.Errorf("2 healthy after Seed(false): verdict=%v, want false", v)
		}
		if v := h.Observe(true); !v {
			t.Errorf("3 healthy after Seed(false): verdict=%v, want true (flipped up)", v)
		}
	})
}
