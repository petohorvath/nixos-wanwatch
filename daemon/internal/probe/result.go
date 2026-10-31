package probe

import (
	"cmp"
	"math/big"
	"slices"
	"time"
)

// TargetStats is the per-Target slice of a probe-cycle result. A nil
// statistic has no data: RTTMicros and JitterMicros are nil until a
// Sample in the Target's Window succeeds, and ExactLoss and LossRatio
// are nil while the Window holds no Sample.
//
// ExactLoss is the Window's loss as an exact rational, lost / total
// Samples, and LossRatio is the float64 nearest it. Callers treat
// ExactLoss as immutable.
type TargetStats struct {
	Target       string
	RTTMicros    *uint64
	JitterMicros *uint64
	ExactLoss    *big.Rat
	LossRatio    *float64
}

// FamilyStats is the per-(WAN, family) aggregate of all Targets'
// stats. PerTarget retains the breakdown for the per-target
// Prometheus gauges (docs/metrics.md), sorted by Target. Each
// statistic is the unweighted mean over the Targets that have data
// for it, and nil, no data, when none has: RTTMicros and JitterMicros
// until a Sample in the Family's Window succeeds, ExactLoss and
// LossRatio while the Window holds no Sample.
//
// ExactLoss is the loss mean as an exact rational, so the thresholds
// compare it without rounding and independently of Target order.
// LossRatio is the float64 nearest it, the value State and the
// metrics report. A FamilyStats without ExactLoss has no loss data
// for the thresholds, whatever LossRatio holds. Callers treat
// ExactLoss as immutable.
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
	ExactLoss    *big.Rat
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
// The loss mean is exact: the per-Target ratios lost / total are
// summed as rationals, and LossRatio comes from that sum by one
// conversion. The RTT and jitter means are integer means, so the whole
// result is independent of the map's iteration order.
//
// Targets without data still appear in PerTarget so the metrics
// surface remains stable across the daemon's startup ramp-up —
// Prometheus dislikes labels that appear and disappear. PerTarget is
// sorted by Target.
func Aggregate(targets map[string]*WindowStats) FamilyStats {
	if len(targets) == 0 {
		return FamilyStats{}
	}
	per := make([]TargetStats, 0, len(targets))
	var rtt, jitter intMean
	var loss ratMean
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
		if lost, total := w.LossCounts(); total > 0 {
			ts.ExactLoss = big.NewRat(int64(lost), int64(total))
			ts.LossRatio = nearest(ts.ExactLoss)
			loss.add(ts.ExactLoss)
		}
		per = append(per, ts)
		if !w.Filled() {
			allFilled = false
		}
	}
	slices.SortFunc(per, func(a, b TargetStats) int { return cmp.Compare(a.Target, b.Target) })
	exact := loss.mean()
	return FamilyStats{
		RTTMicros:    rtt.mean(),
		JitterMicros: jitter.mean(),
		ExactLoss:    exact,
		LossRatio:    nearest(exact),
		WindowFilled: allFilled,
		PerTarget:    per,
	}
}

// intMean accumulates the unweighted mean of the Targets that have data
// for one integer statistic.
type intMean struct {
	sum, n uint64
}

func (m *intMean) add(v uint64) {
	m.sum += v
	m.n++
}

// mean returns the mean, or nil when no Target had data.
func (m *intMean) mean() *uint64 {
	if m.n == 0 {
		return nil
	}
	v := m.sum / m.n
	return &v
}

// ratMean accumulates the exact unweighted mean of the Targets that
// have data for one rational statistic, the loss.
type ratMean struct {
	sum big.Rat
	n   int64
}

func (m *ratMean) add(r *big.Rat) {
	m.sum.Add(&m.sum, r)
	m.n++
}

// mean returns the exact mean, or nil when no Target had data.
func (m *ratMean) mean() *big.Rat {
	if m.n == 0 {
		return nil
	}
	return new(big.Rat).Quo(&m.sum, big.NewRat(m.n, 1))
}

// nearest returns the float64 nearest r; nil, no data, stays nil.
func nearest(r *big.Rat) *float64 {
	if r == nil {
		return nil
	}
	f, _ := r.Float64()
	return &f
}
