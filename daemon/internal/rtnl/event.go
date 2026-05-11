// Package rtnl subscribes to rtnetlink link events and emits
// per-WAN carrier/operstate changes to the daemon's event loop.
//
// Carrier vs Operstate — the two are not redundant. Carrier
// tracks the physical link (cable plugged in, PHY negotiated);
// Operstate is the kernel's RFC 2863 oper state machine, which
// includes states like LowerLayerDown / Dormant / Testing. Some
// drivers don't update carrier promptly but do drive operstate,
// so watching both gives the daemon the earliest possible signal.
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
	OperstateUnknown        Operstate = 0
	OperstateNotPresent     Operstate = 1
	OperstateDown           Operstate = 2
	OperstateLowerLayerDown Operstate = 3
	OperstateTesting        Operstate = 4
	OperstateDormant        Operstate = 5
	OperstateUp             Operstate = 6
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

// LinkState is the per-interface snapshot recorded between
// netlink updates.
type LinkState struct {
	Name      string
	Carrier   Carrier
	Operstate Operstate
}

// LinkEvent is a carrier/operstate transition. The subscriber only
// emits one when the state actually differs from the previous
// observation, so consumers can treat every event as a real change.
type LinkEvent struct {
	Name      string
	Carrier   Carrier
	Operstate Operstate
	Time      time.Time
}
