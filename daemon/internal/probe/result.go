package probe

import "time"

// TargetStats is the per-Target slice of a probe-cycle result. A nil
// statistic has no data: RTTMicros and JitterMicros are nil until a
// Sample in the Target's Window succeeds, and LossRatio is nil while
// the Window holds no Sample.
type TargetStats struct {
	Target       string
	RTTMicros    *uint64
	JitterMicros *uint64
	LossRatio    *float64
}

// FamilyStats is the per-(WAN, family) aggregate of all Targets'
// stats. PerTarget retains the breakdown for the per-target
// Prometheus gauges (docs/metrics.md). Each statistic is the
// unweighted mean over the Targets that have data for it, and nil,
// no data, when none has: RTTMicros and JitterMicros until a Sample
// in the Family's Window succeeds, LossRatio while the Window holds
// no Sample.
//
// WindowFilled reports whether every per-target sliding window has
// reached its configured capacity. The daemon uses it to honour the
// docs/specs/failover.md contract that hysteresis seeds from the first probe
// *Window*, not the first probe *Sample* — without this gate a Lost
// first sample (probe loop fires before the kernel routes / target
// host responds) seeds the verdict unhealthy and triggers a
// spurious down→up flap once probes converge.
type FamilyStats struct {
	RTTMicros    *uint64
	JitterMicros *uint64
	LossRatio    *float64
	WindowFilled bool
	PerTarget    []TargetStats
}

// ProbeResult is what a Pinger emits on its output channel after
// each probe cycle. Consumed by the selector + state writer. The
// `Probe` prefix is deliberate vocabulary, not stuttering — every
// caller reads `probe.ProbeResult` as "the result of a probe".
//
//nolint:revive // see comment above for the naming rationale
type ProbeResult struct {
	Wan    string
	Family Family
	Stats  FamilyStats
	Time   time.Time
}

// Aggregate reduces a set of per-Target WindowStats into a single
// FamilyStats. Each target's mean RTT, jitter, and loss feed into
// the family aggregate as an unweighted mean — docs/specs/failover.md doesn't
// specify a weighting scheme so the simplest interpretation wins
// for v1. A Target without data for a statistic stays out of that
// statistic's mean, so neither an empty Window nor a fully lost one
// drags the Family's RTT toward 0.
//
// Targets without data still appear in PerTarget so the metrics
// surface remains stable across the daemon's startup ramp-up —
// Prometheus dislikes labels that appear and disappear.
func Aggregate(targets map[string]*WindowStats) FamilyStats {
	if len(targets) == 0 {
		return FamilyStats{}
	}
	per := make([]TargetStats, 0, len(targets))
	var rtt, jitter meanOf[uint64]
	var loss meanOf[float64]
	allFilled := true
	for name, w := range targets {
		ts := TargetStats{Target: name}
		if v, ok := w.MeanRTT(); ok {
			ts.RTTMicros = &v
			rtt.add(v)
		}
		if v, ok := w.JitterMicros(); ok {
			ts.JitterMicros = &v
			jitter.add(v)
		}
		if v, ok := w.LossRatio(); ok {
			ts.LossRatio = &v
			loss.add(v)
		}
		per = append(per, ts)
		if !w.Filled() {
			allFilled = false
		}
	}
	return FamilyStats{
		RTTMicros:    rtt.mean(),
		JitterMicros: jitter.mean(),
		LossRatio:    loss.mean(),
		WindowFilled: allFilled,
		PerTarget:    per,
	}
}

// meanOf accumulates the unweighted mean of the Targets that have data
// for one statistic.
type meanOf[T uint64 | float64] struct {
	sum T
	n   int
}

func (m *meanOf[T]) add(v T) {
	m.sum += v
	m.n++
}

// mean returns the mean, or nil when no Target had data.
func (m *meanOf[T]) mean() *T {
	if m.n == 0 {
		return nil
	}
	v := m.sum / T(m.n)
	return &v
}
