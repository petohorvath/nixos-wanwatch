package probe

import (
	"fmt"
	"math"
	"slices"
	"testing"
)

// show renders an optional statistic for failure messages.
func show[T any](p *T) string {
	if p == nil {
		return "no data"
	}
	return fmt.Sprint(*p)
}

// assertStat checks that the optional statistic got is want, with nil
// meaning no data.
func assertStat[T comparable](tb testing.TB, what string, got, want *T) {
	tb.Helper()
	if (got == nil) != (want == nil) || (got != nil && *got != *want) {
		tb.Errorf("%s = %s, want %s", what, show(got), show(want))
	}
}

func ptr[T any](v T) *T { return &v }

func TestAggregateEmptyTargets(t *testing.T) {
	t.Parallel()
	got := Aggregate(nil)
	assertStat(t, "RTT", got.RTTMicros, nil)
	assertStat(t, "jitter", got.JitterMicros, nil)
	assertStat(t, "loss", got.LossRatio, nil)
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
	assertStat(t, "RTT (mean of 10/20/30 ms)", got.RTTMicros, ptr[uint64](20000))
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
	assertStat(t, "aggregate RTT (mean of 10ms + 20ms)", got.RTTMicros, ptr[uint64](15000))
}

func TestAggregateSkipsEmptyWindowsInMean(t *testing.T) {
	t.Parallel()
	// Empty windows still appear in PerTarget (with no data) so the
	// scrape surface is stable, but they don't drag the aggregate
	// loss/RTT toward zero during the daemon's ramp-up.
	a := mustNewWindow(t, 2)
	a.Push(Sample{Lost: true})
	a.Push(Sample{Lost: true})
	b := mustNewWindow(t, 2) // empty

	got := Aggregate(map[string]*WindowStats{"a": a, "b": b})
	assertStat(t, "LossRatio (only a counted)", got.LossRatio, ptr(1.0))
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
	assertStat(t, "LossRatio", got.LossRatio, ptr(1.0))
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

// TestAggregateNoData pins the "no data" statistics of a Family and its
// Targets: RTT and jitter have none until a Sample succeeds, loss has
// none until the Window holds a Sample, and one successful Sample has a
// jitter of 0. A Target without data does not count toward the
// Family's mean of that statistic.
func TestAggregateNoData(t *testing.T) {
	t.Parallel()
	type stats struct {
		rtt, jitter *uint64
		loss        *float64
	}
	for _, tc := range []struct {
		name    string
		targets map[string][]Sample
		family  stats
		target  map[string]stats
	}{
		{
			name:    "empty Window",
			targets: map[string][]Sample{"a": nil},
			target:  map[string]stats{"a": {}},
		},
		{
			name:    "fully lost Window",
			targets: map[string][]Sample{"a": {{Lost: true}, {Lost: true}}},
			family:  stats{loss: ptr(1.0)},
			target:  map[string]stats{"a": {loss: ptr(1.0)}},
		},
		{
			name:    "one successful Sample",
			targets: map[string][]Sample{"a": {{Lost: true}, {RTTMicros: 7000}}},
			family:  stats{rtt: ptr[uint64](7000), jitter: ptr[uint64](0), loss: ptr(0.5)},
			target:  map[string]stats{"a": {rtt: ptr[uint64](7000), jitter: ptr[uint64](0), loss: ptr(0.5)}},
		},
		{
			name: "a lost Target leaves the RTT and jitter means to the others",
			targets: map[string][]Sample{
				"a": {{Lost: true}, {Lost: true}},
				"b": {{RTTMicros: 10000}, {RTTMicros: 30000}},
				"c": nil,
			},
			family: stats{rtt: ptr[uint64](20000), jitter: ptr[uint64](10000), loss: ptr(0.5)},
			target: map[string]stats{
				"a": {loss: ptr(1.0)},
				"b": {rtt: ptr[uint64](20000), jitter: ptr[uint64](10000), loss: ptr(0.0)},
				"c": {},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			windows := make(map[string]*WindowStats, len(tc.targets))
			for name, samples := range tc.targets {
				w := mustNewWindow(t, 2)
				for _, s := range samples {
					w.Push(s)
				}
				windows[name] = w
			}

			got := Aggregate(windows)
			assertStat(t, "Family RTT", got.RTTMicros, tc.family.rtt)
			assertStat(t, "Family jitter", got.JitterMicros, tc.family.jitter)
			assertStat(t, "Family loss", got.LossRatio, tc.family.loss)
			if len(got.PerTarget) != len(tc.target) {
				t.Fatalf("PerTarget = %+v, want %d Targets", got.PerTarget, len(tc.target))
			}
			for _, ts := range got.PerTarget {
				want, ok := tc.target[ts.Target]
				if !ok {
					t.Errorf("unexpected Target %q", ts.Target)
					continue
				}
				assertStat(t, ts.Target+" RTT", ts.RTTMicros, want.rtt)
				assertStat(t, ts.Target+" jitter", ts.JitterMicros, want.jitter)
				assertStat(t, ts.Target+" loss", ts.LossRatio, want.loss)
			}
		})
	}
}

// lossyWindows returns one full Window of the given capacity per entry
// of lost, keyed "t0", "t1", ..., whose Window holds lost[i] Lost
// Samples followed by successful ones.
func lossyWindows(tb testing.TB, capacity int, lost ...int) map[string]*WindowStats {
	tb.Helper()
	windows := make(map[string]*WindowStats, len(lost))
	for i, l := range lost {
		w, err := NewWindow(capacity)
		if err != nil {
			tb.Fatalf("NewWindow(%d): %v", capacity, err)
		}
		for j := range capacity {
			w.Push(Sample{RTTMicros: 10_000, Lost: j < l})
		}
		windows[fmt.Sprintf("t%d", i)] = w
	}
	return windows
}

// TestAggregateLossRatioIsNearestToExactMean: three Targets each
// losing 1 of 10 Samples report exactly 0.1, the float64 nearest the
// exact mean, not the 0.10000000000000002 that summing the per-Target
// floats gives.
func TestAggregateLossRatioIsNearestToExactMean(t *testing.T) {
	t.Parallel()
	got := Aggregate(lossyWindows(t, 10, 1, 1, 1))
	assertStat(t, "LossRatio", got.LossRatio, ptr(0.1))
	for _, ts := range got.PerTarget {
		assertStat(t, ts.Target+" LossRatio", ts.LossRatio, ptr(0.1))
	}
}

// TestAggregateIsDeterministic: Aggregate over the same Windows gives a
// bit-identical LossRatio and the same PerTarget order on every call,
// whatever order the map yields its Targets in.
func TestAggregateIsDeterministic(t *testing.T) {
	t.Parallel()
	windows := lossyWindows(t, 10, 1, 2, 3)
	wantTargets := []string{"t0", "t1", "t2"}
	for i := range 200 {
		got := Aggregate(windows)
		if got.LossRatio == nil || math.Float64bits(*got.LossRatio) != math.Float64bits(0.2) {
			t.Fatalf("run %d: LossRatio = %s, want 0.2 bit for bit", i, show(got.LossRatio))
		}
		targets := make([]string, 0, len(got.PerTarget))
		for _, ts := range got.PerTarget {
			targets = append(targets, ts.Target)
		}
		if !slices.Equal(targets, wantTargets) {
			t.Fatalf("run %d: PerTarget order = %v, want %v", i, targets, wantTargets)
		}
	}
}
