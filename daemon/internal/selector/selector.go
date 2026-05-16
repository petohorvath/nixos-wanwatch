// Package selector decides which Group member should carry traffic
// at any moment.
//
// The package owns three concerns:
//
//  1. **Types** — Group, Member, MemberHealth, Selection (this file).
//  2. **Strategies** — pure functions that map (Group, []MemberHealth)
//     to a Selection. v1 ships `primary-backup` (primarybackup.go);
//     v2 will add `load-balance` when multi-active lands.
//  3. **Hysteresis** — per-WAN consecutive-cycle state machine
//     (hysteresis.go) that suppresses flapping by gating the
//     externally-visible Healthy verdict.
//
// The package is `internal/selector/` rather than `internal/select/`
// because `select` is a Go control-flow keyword and cannot appear as
// a package identifier.
package selector

import (
	"errors"
	"fmt"
)

// ErrUnknownStrategy is returned by Select when g.Strategy doesn't
// match any registered strategy. Sentinel so callers can match with
// `errors.Is`.
var ErrUnknownStrategy = errors.New("selector: unknown strategy")

// Group is the daemon-side view of a Group value. It mirrors the
// JSON shape produced by `wanwatch.group.toJSONValue` so the daemon
// can deserialize directly into this struct.
type Group struct {
	Name     string   `json:"name"`
	Strategy string   `json:"strategy"`
	Table    int      `json:"table"`
	Mark     int      `json:"mark"`
	Members  []Member `json:"members"`
}

// Member is the daemon-side view of a Member value.
type Member struct {
	Wan      string `json:"wan"`
	Weight   int    `json:"weight"`
	Priority int    `json:"priority"`
}

// MemberHealth pairs a Member with its current Health verdict.
// Health is produced upstream by the probe layer (after hysteresis
// has been applied) — the selector only consults the boolean.
type MemberHealth struct {
	Member  Member
	Healthy bool
}

// Selection is the strategy's output: which Member's WAN should
// carry the group's traffic, if any. Active.Has is false when no
// member is healthy (the "all-down" case — Apply layer leaves the
// routing table as-is or installs a sentinel route, depending on
// policy).
type Selection struct {
	Group  string
	Active Active
}

// Active is the resolved choice of a Strategy: the chosen WAN's
// name and a flag for whether one was chosen at all. Comparable
// (Go's `==` works between Actives), which removes the
// `*string` nil-check + deref pattern at every consumer.
type Active struct {
	Wan string
	Has bool
}

// NoActive is the "no member healthy" Selection.Active value.
// Equivalent to the Active zero value, exported for readability
// at call sites.
var NoActive = Active{}

// Strategy chooses an active Member from a Group's MemberHealth list.
// Implementations are deterministic — given the same inputs, they
// must produce the same Selection.
type Strategy func(g Group, members []MemberHealth) Selection

// strategies registers v1's strategies by name. Lookup happens in
// Select; adding a strategy here is the only place the registry
// changes.
var strategies = map[string]Strategy{
	"primary-backup": primaryBackup,
}

// Select looks up the strategy named by g.Strategy and runs it
// against members. Returns an error when the strategy is unknown —
// the config layer should catch that case at startup, but defensive
// here too.
//
// The name is deliberately not "Apply" — that term is reserved by
// the glossary for kernel mutation (route, rule, conntrack writes
// in `internal/apply`). Select produces a Selection; Apply consumes
// the resulting Decision.
func Select(g Group, members []MemberHealth) (Selection, error) {
	s, ok := strategies[g.Strategy]
	if !ok {
		return Selection{}, fmt.Errorf("%w %q for group %q", ErrUnknownStrategy, g.Strategy, g.Name)
	}
	return s(g, members), nil
}

// KnownStrategies returns the names of all registered strategies.
// Exposed for tests, for `wanwatchctl` (Pass 6), and for the
// config-validation layer that wants to cross-check the user's
// Strategy field.
func KnownStrategies() []string {
	out := make([]string, 0, len(strategies))
	for name := range strategies {
		out = append(out, name)
	}
	return out
}
