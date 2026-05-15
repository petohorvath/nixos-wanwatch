package probe

import "testing"

func TestAggregateEmptyTargets(t *testing.T) {
	t.Parallel()
	got := Aggregate(nil)
	if got.RTTMicros != 0 || got.JitterMicros != 0 || got.LossRatio != 0 {
		t.Errorf("Aggregate(nil) = %+v, want zeros", got)
	}
	if len(got.PerTarget) != 0 {
		t.Errorf("len(PerTarget) = %d, want 0", len(got.PerTarget))
	}
}

func TestAggregateSingleTarget(t *testing.T) {
	t.Parallel()
	w := mustNewWindow(t, 4)
	w.Push(Sample{RTTMicros: 10000})
	w.Push(Sample{RTTMicros: 20000})
	w.Push(Sample{RTTMicros: 30000})

	got := Aggregate(map[string]*WindowStats{"1.1.1.1": w})
	if got.RTTMicros != 20000 {
		t.Errorf("RTT = %d, want 20000 (mean of 10/20/30 ms)", got.RTTMicros)
	}
	if len(got.PerTarget) != 1 || got.PerTarget[0].Target != "1.1.1.1" {
		t.Errorf("PerTarget = %+v, want one entry for 1.1.1.1", got.PerTarget)
	}
}

func TestAggregateMeanAcrossTargets(t *testing.T) {
	t.Parallel()
	a := mustNewWindow(t, 2)
	a.Push(Sample{RTTMicros: 10000})
	a.Push(Sample{RTTMicros: 10000})
	b := mustNewWindow(t, 2)
	b.Push(Sample{RTTMicros: 20000})
	b.Push(Sample{RTTMicros: 20000})

	got := Aggregate(map[string]*WindowStats{"a": a, "b": b})
	if got.RTTMicros != 15000 {
		t.Errorf("aggregate RTT = %d, want 15000 (mean of 10ms + 20ms)", got.RTTMicros)
	}
}

func TestAggregateSkipsEmptyWindowsInMean(t *testing.T) {
	t.Parallel()
	// Empty windows still appear in PerTarget (zeros) so the
	// scrape surface is stable, but they don't drag the aggregate
	// loss/RTT toward zero during the daemon's ramp-up.
	a := mustNewWindow(t, 2)
	a.Push(Sample{Lost: true})
	a.Push(Sample{Lost: true})
	b := mustNewWindow(t, 2) // empty

	got := Aggregate(map[string]*WindowStats{"a": a, "b": b})
	if got.LossRatio != 1.0 {
		t.Errorf("LossRatio = %v, want 1.0 (only a counted)", got.LossRatio)
	}
	if len(got.PerTarget) != 2 {
		t.Errorf("PerTarget len = %d, want 2 (both targets surfaced)", len(got.PerTarget))
	}
}

func TestAggregateLossRatioMeanIsBoundedByOne(t *testing.T) {
	t.Parallel()
	// 2 fully-lost targets → loss ratio 1.0, not 2.0. Catches a
	// "sum" instead of "mean" implementation bug.
	a := mustNewWindow(t, 2)
	a.Push(Sample{Lost: true})
	a.Push(Sample{Lost: true})
	b := mustNewWindow(t, 2)
	b.Push(Sample{Lost: true})
	b.Push(Sample{Lost: true})

	got := Aggregate(map[string]*WindowStats{"a": a, "b": b})
	if got.LossRatio != 1.0 {
		t.Errorf("LossRatio = %v, want 1.0", got.LossRatio)
	}
}

// TestAggregateWindowFilledAllFull: when every target's window has
// wrapped at least once, the aggregate reports WindowFilled = true
// — the signal the daemon's cold-start gate keys on to seed
// hysteresis from a real Window rather than a partial one.
func TestAggregateWindowFilledAllFull(t *testing.T) {
	t.Parallel()
	a := mustNewWindow(t, 2)
	a.Push(Sample{RTTMicros: 1})
	a.Push(Sample{RTTMicros: 2})
	b := mustNewWindow(t, 2)
	b.Push(Sample{RTTMicros: 3})
	b.Push(Sample{RTTMicros: 4})

	got := Aggregate(map[string]*WindowStats{"a": a, "b": b})
	if !got.WindowFilled {
		t.Error("WindowFilled = false, want true (both windows full)")
	}
}

// TestAggregateWindowFilledAnyPartial: one target's window short of
// capacity drops WindowFilled to false even when other targets are
// full. Avoids seeding the daemon's hysteresis off a verdict that
// only some targets have contributed to.
func TestAggregateWindowFilledAnyPartial(t *testing.T) {
	t.Parallel()
	a := mustNewWindow(t, 2)
	a.Push(Sample{RTTMicros: 1})
	a.Push(Sample{RTTMicros: 2}) // filled
	b := mustNewWindow(t, 2)
	b.Push(Sample{RTTMicros: 3}) // 1 of 2 — not yet filled

	got := Aggregate(map[string]*WindowStats{"a": a, "b": b})
	if got.WindowFilled {
		t.Error("WindowFilled = true, want false (b is partial)")
	}
}

// TestAggregateWindowFilledEmpty: no Samples anywhere → not filled.
// Boundary that exercises the first probe cycle's emission, where
// every window is still empty after sampling once.
func TestAggregateWindowFilledEmpty(t *testing.T) {
	t.Parallel()
	a := mustNewWindow(t, 2) // empty

	got := Aggregate(map[string]*WindowStats{"a": a})
	if got.WindowFilled {
		t.Error("WindowFilled = true on an empty window, want false")
	}
}
