package probe

import (
	"math"
	"testing"
)

// floatEq compares floats with epsilon tolerance for use in the
// LossRatio assertions (the rest of the API returns uint64).
func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// mustNewWindow is a helper used across the probe-package test
// files: NewWindow now returns (window, error) so happy-path tests
// would otherwise drown in boilerplate.
func mustNewWindow(t *testing.T, capacity int) *WindowStats {
	t.Helper()
	w, err := NewWindow(capacity)
	if err != nil {
		t.Fatalf("NewWindow(%d): %v", capacity, err)
	}
	return w
}

func TestNewWindowRejectsNonPositiveCapacity(t *testing.T) {
	t.Parallel()
	cases := []int{0, -1, -100}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			if _, err := NewWindow(c); err == nil {
				t.Errorf("NewWindow(%d) returned nil error", c)
			}
		})
	}
}

func TestWindowEmpty(t *testing.T) {
	t.Parallel()
	w := mustNewWindow(t, 5)
	if got := w.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if got := w.Capacity(); got != 5 {
		t.Errorf("Capacity() = %d, want 5", got)
	}
	if got := w.LossRatio(); got != 0 {
		t.Errorf("LossRatio() = %f, want 0", got)
	}
	if got := w.MeanRTT(); got != 0 {
		t.Errorf("MeanRTT() = %d, want 0", got)
	}
	if got := w.JitterMicros(); got != 0 {
		t.Errorf("JitterMicros() = %d, want 0", got)
	}
}

// TestWindowScenarios exercises Len / LossRatio / MeanRTT /
// JitterMicros across a representative mix of inputs — empty,
// single-sample, all-loss, all-good, mixed, wrap-around, and the
// capacity=1 degenerate case.
//
// Jitter is the population standard deviation; expected values are
// computed in the table as truncated-uint64 of sqrt(variance):
//
//	values [10 20 30] → mean 20, variance 200/3 ≈ 66.67, stddev ≈ 8.16 → 8.
func TestWindowScenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		capacity   int
		pushes     []Sample
		wantLen    int
		wantLoss   float64
		wantMean   uint64
		wantJitter uint64
	}{
		{
			name:     "single good sample",
			capacity: 5,
			pushes:   []Sample{{RTTMicros: 100}},
			wantLen:  1,
			wantMean: 100,
		},
		{
			name:     "single lost sample",
			capacity: 5,
			pushes:   []Sample{{Lost: true}},
			wantLen:  1,
			wantLoss: 1,
		},
		{
			name:     "three goods varied RTT",
			capacity: 5,
			pushes: []Sample{
				{RTTMicros: 10},
				{RTTMicros: 20},
				{RTTMicros: 30},
			},
			wantLen:    3,
			wantMean:   20,
			wantJitter: 8,
		},
		{
			name:     "constant RTT yields zero jitter",
			capacity: 4,
			pushes: []Sample{
				{RTTMicros: 1000},
				{RTTMicros: 1000},
				{RTTMicros: 1000},
			},
			wantLen:  3,
			wantMean: 1000,
		},
		{
			name:     "all lost",
			capacity: 3,
			pushes: []Sample{
				{Lost: true},
				{Lost: true},
				{Lost: true},
			},
			wantLen:  3,
			wantLoss: 1,
		},
		{
			name:     "mixed lost and good — two of each",
			capacity: 4,
			pushes: []Sample{
				{RTTMicros: 10},
				{Lost: true},
				{RTTMicros: 30},
				{Lost: true},
			},
			wantLen:    4,
			wantLoss:   0.5,
			wantMean:   20,
			wantJitter: 10, // sqrt(((10-20)^2 + (30-20)^2)/2) = sqrt(100) = 10
		},
		{
			name:     "single non-lost among many lost",
			capacity: 5,
			pushes: []Sample{
				{Lost: true},
				{Lost: true},
				{RTTMicros: 50},
				{Lost: true},
			},
			wantLen:  4,
			wantLoss: 0.75,
			wantMean: 50,
			// only 1 non-lost → jitter = 0 (stddev below 2 samples)
		},
		{
			name:     "wrap-around drops oldest",
			capacity: 3,
			pushes: []Sample{
				// First two are pushed out by the last three.
				{RTTMicros: 10},
				{RTTMicros: 20},
				{RTTMicros: 30},
				{RTTMicros: 40},
				{RTTMicros: 50},
			},
			wantLen:    3,
			wantMean:   40, // (30+40+50)/3
			wantJitter: 8,  // same shape as the 10/20/30 case
		},
		{
			name:     "capacity 1 keeps only the latest",
			capacity: 1,
			pushes: []Sample{
				{RTTMicros: 100},
				{RTTMicros: 200},
			},
			wantLen:  1,
			wantMean: 200,
		},
		{
			name:     "fill exactly to capacity",
			capacity: 3,
			pushes: []Sample{
				{RTTMicros: 10},
				{RTTMicros: 20},
				{RTTMicros: 30},
			},
			wantLen:    3,
			wantMean:   20,
			wantJitter: 8,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := mustNewWindow(t, tc.capacity)
			for _, s := range tc.pushes {
				w.Push(s)
			}
			if got := w.Len(); got != tc.wantLen {
				t.Errorf("Len() = %d, want %d", got, tc.wantLen)
			}
			if got := w.LossRatio(); !floatEq(got, tc.wantLoss) {
				t.Errorf("LossRatio() = %f, want %f", got, tc.wantLoss)
			}
			if got := w.MeanRTT(); got != tc.wantMean {
				t.Errorf("MeanRTT() = %d, want %d", got, tc.wantMean)
			}
			if got := w.JitterMicros(); got != tc.wantJitter {
				t.Errorf("JitterMicros() = %d, want %d", got, tc.wantJitter)
			}
		})
	}
}

// TestPushOrderingPreservedAfterWrap asserts the ring-buffer
// invariant: after wrap-around the oldest in-window sample is the
// (capacity+1)th pushed, not the first.
func TestPushOrderingPreservedAfterWrap(t *testing.T) {
	t.Parallel()
	w := mustNewWindow(t, 3)
	w.Push(Sample{RTTMicros: 1}) // dropped by the fourth push
	w.Push(Sample{RTTMicros: 2})
	w.Push(Sample{RTTMicros: 3})
	w.Push(Sample{RTTMicros: 4})
	// Window now holds {2, 3, 4} in oldest-to-newest order.
	if got := w.MeanRTT(); got != 3 {
		t.Errorf("MeanRTT after wrap = %d, want 3 (oldest sample should have been dropped)", got)
	}
}
