// Package rtnl subscribes to rtnetlink link events and emits
// per-WAN carrier/operstate changes to the daemon's event loop.
//
// Split into two layers so the change-detection contract is
// testable without raw netlink:
//
//   - event.go (this file) — pure types + Diff. No syscalls.
//   - subscriber.go (later in Pass 4) — the netlink-bound socket
//     that drives Diff in production. VM-tier integration tests
//     per PLAN §9.4.
//
// Carrier vs Operstate — the two are not redundant:
//
//   - Carrier tracks the *physical* link state (cable plugged in,
//     PHY negotiated). Equivalent to `/sys/class/net/<if>/carrier`.
//   - Operstate is the kernel's RFC 2863 oper state machine, which
//     includes states like LowerLayerDown, Dormant, Testing. Some
//     drivers don't update carrier promptly but do drive operstate;
//     watching both gives the daemon the earliest possible signal.
package rtnl

import (
	"fmt"
	"time"
)

// Carrier is the physical link state surfaced via rtnetlink
// (IFLA_CARRIER) or `/sys/class/net/<if>/carrier`.
type Carrier int

const (
	// CarrierUnknown is reported before the kernel has classified
	// the interface — typically right after boot, before the link
	// driver finishes initial negotiation.
	CarrierUnknown Carrier = iota
	// CarrierDown means the link is physically down (no cable, no
	// peer, or admin-disabled).
	CarrierDown
	// CarrierUp means the link is physically up and ready to carry
	// frames.
	CarrierUp
)

// String returns "up" / "down" / "unknown". Used for log messages,
// metrics labels, and state.json.
func (c Carrier) String() string {
	switch c {
	case CarrierUp:
		return "up"
	case CarrierDown:
		return "down"
	default:
		return "unknown"
	}
}

// Operstate mirrors the IF_OPER_* enum from `<linux/if.h>`. Values
// are the kernel's, surfaced verbatim so logs match `ip -d link
// show` output.
type Operstate int

const (
	OperstateUnknown        Operstate = 0 // IF_OPER_UNKNOWN
	OperstateNotPresent     Operstate = 1 // IF_OPER_NOTPRESENT
	OperstateDown           Operstate = 2 // IF_OPER_DOWN
	OperstateLowerLayerDown Operstate = 3 // IF_OPER_LOWERLAYERDOWN
	OperstateTesting        Operstate = 4 // IF_OPER_TESTING
	OperstateDormant        Operstate = 5 // IF_OPER_DORMANT
	OperstateUp             Operstate = 6 // IF_OPER_UP
)

// String returns the textual operstate name as printed by
// `ip link show` / `iproute2`. Out-of-range values render as
// `operstate(N)` so a kernel ABI bump surfaces in logs rather than
// silently aliasing to "unknown".
func (o Operstate) String() string {
	switch o {
	case OperstateUnknown:
		return "unknown"
	case OperstateNotPresent:
		return "notpresent"
	case OperstateDown:
		return "down"
	case OperstateLowerLayerDown:
		return "lowerlayerdown"
	case OperstateTesting:
		return "testing"
	case OperstateDormant:
		return "dormant"
	case OperstateUp:
		return "up"
	default:
		return fmt.Sprintf("operstate(%d)", int(o))
	}
}

// LinkState is the per-interface snapshot the daemon tracks
// between netlink updates. Captured fresh from each RTM_NEWLINK
// message; the previous value drives Diff.
type LinkState struct {
	Name      string
	Carrier   Carrier
	Operstate Operstate
}

// LinkEvent records a carrier/operstate transition for a single
// interface. Emitted only when at least one of the fields differs
// from the previously seen state, so consumers can treat every
// event as a real change. Time is the emit timestamp, stamped UTC
// by the subscriber.
type LinkEvent struct {
	Name      string
	Carrier   Carrier
	Operstate Operstate
	Time      time.Time
}

// Diff returns one LinkEvent per interface whose state in `cur`
// differs from `prev`. Interfaces present in `prev` but missing
// from `cur` are *not* emitted — link disappearance is signalled by
// RTM_DELLINK on the netlink wire, which the subscriber handles
// directly. Order of returned events is unspecified.
//
// Event.Time is left zero; the subscriber stamps it on emit so
// pure tests can assert the rest of the value cleanly.
func Diff(prev, cur map[string]LinkState) []LinkEvent {
	events := make([]LinkEvent, 0)
	for name, c := range cur {
		p, seen := prev[name]
		if seen && p.Carrier == c.Carrier && p.Operstate == c.Operstate {
			continue
		}
		events = append(events, LinkEvent{
			Name:      name,
			Carrier:   c.Carrier,
			Operstate: c.Operstate,
		})
	}
	return events
}
