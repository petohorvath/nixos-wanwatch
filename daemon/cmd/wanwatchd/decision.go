package main

import (
	"cmp"
	"slices"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// evaluateThresholds is a two-threshold band-pass: above Down→down,
// below Up→up, in-band hold. The Nix-side option type guarantees
// Up < Down so the band is always non-empty.
func evaluateThresholds(prev bool, stats probe.FamilyStats, t config.Thresholds) bool {
	lossPct := stats.LossRatio * 100
	rttMs := float64(stats.RTTMicros) / 1000
	if prev {
		if lossPct >= float64(t.LossPctDown) || rttMs >= float64(t.RttMsDown) {
			return false
		}
		return true
	}
	if lossPct <= float64(t.LossPctUp) && rttMs <= float64(t.RttMsUp) {
		return true
	}
	return false
}

// FamilyHealthPolicy values per PLAN §5.1. Strings (not an enum)
// because they're a config wire-format contract; the Nix side
// emits the same literals into the daemon-config JSON.
const (
	familyPolicyAll = "all"
	familyPolicyAny = "any"
)

// combineFamilies aggregates per-family Healthy booleans into a
// per-WAN verdict under `policy`:
//
//   - familyPolicyAll — every probed family must be healthy
//   - familyPolicyAny — at least one probed family must be healthy
//
// A family that hasn't received its first ProbeResult yet (`cooked
// = false`) is treated as healthy — PLAN §8 cold-start says
// "health is unknown but carrier is at least known", so we trust
// carrier alone until the first sample arrives.
func combineFamilies(families map[probe.Family]*familyState, policy string) bool {
	var probed, healthy int
	for _, f := range families {
		if f == nil {
			continue
		}
		probed++
		if !f.cooked || f.healthy {
			healthy++
		}
	}
	if probed == 0 {
		return false
	}
	switch policy {
	case familyPolicyAny:
		return healthy > 0
	default:
		// familyPolicyAll — also the conservative default for
		// unknown / unset policy strings.
		return healthy == probed
	}
}

// buildMemberHealth produces a stable, sorted slice of
// selector.MemberHealth for `g` by looking each member's WAN up in
// `wans`. Members whose WAN is missing or whose carrier is down
// are recorded as Healthy=false — both conditions defeat any
// recent probe signal.
func buildMemberHealth(g selector.Group, wans map[string]*wanState) []selector.MemberHealth {
	out := make([]selector.MemberHealth, 0, len(g.Members))
	for _, m := range g.Members {
		w, ok := wans[m.Wan]
		healthy := ok && w.carrierUp() && w.healthy
		out = append(out, selector.MemberHealth{Member: m, Healthy: healthy})
	}
	// Stable order so logs / hooks / state.json present members
	// the same way every cycle.
	slices.SortFunc(out, func(a, b selector.MemberHealth) int {
		return cmp.Compare(a.Member.Wan, b.Member.Wan)
	})
	return out
}

// groupContainsWAN reports whether `wan` is a member of `g`.
func groupContainsWAN(g selector.Group, wan string) bool {
	for _, m := range g.Members {
		if m.Wan == wan {
			return true
		}
	}
	return false
}

// decisionReason names the trigger for a Decision; emitted as a
// metric label and in hook env vars.
type decisionReason string

const (
	reasonHealth  decisionReason = "health"
	reasonCarrier decisionReason = "carrier"
)

// hookEventFor maps the old/next Active to the hook directory the
// runner should dispatch into:
//
//   - absent → present       ⇒ up
//   - present → absent       ⇒ down
//   - present → present (≠)  ⇒ switch
//   - otherwise              ⇒ "" (no event)
func hookEventFor(old, next selector.Active) state.Event {
	switch {
	case !old.Has && next.Has:
		return state.EventUp
	case old.Has && !next.Has:
		return state.EventDown
	case old.Has && next.Has && old.Wan != next.Wan:
		return state.EventSwitch
	}
	return ""
}
