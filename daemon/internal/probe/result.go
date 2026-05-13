package probe

import "time"

// TargetStats is the per-Target slice of a probe-cycle result.
type TargetStats struct {
	Target       string
	RTTMicros    uint64
	JitterMicros uint64
	LossRatio    float64
}

// FamilyStats is the per-(WAN, family) aggregate of all Targets'
// stats. PerTarget retains the breakdown for the per-target
// Prometheus gauges (PLAN §7.2).
type FamilyStats struct {
	RTTMicros    uint64
	JitterMicros uint64
	LossRatio    float64
	PerTarget    []TargetStats
}

// ProbeResult is what a Pinger emits on its output channel after
// each probe cycle. Consumed by the selector + state writer.
//
//nolint:revive // ProbeResult reads cleaner than probe.Result at
// every consumer; the stuttering is a deliberate vocabulary choice.
type ProbeResult struct {
	Wan    string
	Family Family
	Stats  FamilyStats
	Time   time.Time
}

// Aggregate reduces a set of per-Target WindowStats into a single
// FamilyStats. Each target's mean RTT, jitter, and loss feed into
// the family aggregate as an unweighted mean — PLAN §8 doesn't
// specify a weighting scheme so the simplest interpretation wins
// for v1.
//
// Targets with empty windows still appear in PerTarget (as zeros)
// so the metrics surface remains stable across the daemon's
// startup ramp-up — Prometheus dislikes labels that appear and
// disappear.
func Aggregate(targets map[string]*WindowStats) FamilyStats {
	if len(targets) == 0 {
		return FamilyStats{}
	}
	per := make([]TargetStats, 0, len(targets))
	var rttSum, jitterSum uint64
	var lossSum float64
	var n int
	for name, w := range targets {
		ts := TargetStats{
			Target:       name,
			RTTMicros:    w.MeanRTT(),
			JitterMicros: w.JitterMicros(),
			LossRatio:    w.LossRatio(),
		}
		per = append(per, ts)
		if w.Len() == 0 {
			continue
		}
		rttSum += ts.RTTMicros
		jitterSum += ts.JitterMicros
		lossSum += ts.LossRatio
		n++
	}
	out := FamilyStats{PerTarget: per}
	if n > 0 {
		out.RTTMicros = rttSum / uint64(n)
		out.JitterMicros = jitterSum / uint64(n)
		out.LossRatio = lossSum / float64(n)
	}
	return out
}
