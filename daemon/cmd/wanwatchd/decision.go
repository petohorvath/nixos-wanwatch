package main

import (
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

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
