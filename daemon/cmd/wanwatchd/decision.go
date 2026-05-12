package main

import (
	"sort"

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

// combineFamilies aggregates per-family Healthy booleans into a
// per-WAN verdict under `policy`. PLAN §5.1 fixes the two values:
//
//   - "all" — every probed family must be healthy
//   - "any" — at least one probed family must be healthy
//
// `families` carries Nil entries for families the WAN doesn't
// probe (no gateway in that family) — those are skipped, not
// counted against the verdict.
func combineFamilies(families map[probe.Family]*familyState, policy string) bool {
	var probed, healthy int
	for _, f := range families {
		if f == nil {
			continue
		}
		probed++
		if f.healthy {
			healthy++
		}
	}
	if probed == 0 {
		return false
	}
	switch policy {
	case "any":
		return healthy > 0
	default:
		// "all" — also the conservative default for unknown policies.
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
	sort.Slice(out, func(i, j int) bool {
		return out[i].Member.Wan < out[j].Member.Wan
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

// equalStringPtr compares two *string by value — nil == nil,
// both-non-nil compared by content. Used to detect actual
// Selection changes without spurious notifications.
func equalStringPtr(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return *a == *b
}

// decisionReason names the trigger for a Decision; emitted as a
// metric label and in hook env vars.
type decisionReason string

const (
	reasonHealth  decisionReason = "health"
	reasonCarrier decisionReason = "carrier"
)

// hookEventFor maps the old/new active pointers to the hook
// directory the runner should dispatch into:
//
//   - nil → non-nil ⇒ up
//   - non-nil → nil ⇒ down
//   - non-nil → non-nil, different ⇒ switch
//   - otherwise ⇒ "" (no event)
func hookEventFor(old, new_ *string) state.Event {
	switch {
	case old == nil && new_ != nil:
		return state.EventUp
	case old != nil && new_ == nil:
		return state.EventDown
	case old != nil && new_ != nil && *old != *new_:
		return state.EventSwitch
	}
	return ""
}
