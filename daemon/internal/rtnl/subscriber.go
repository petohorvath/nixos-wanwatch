package rtnl

import (
	"context"
	"fmt"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Subscriber owns a netlink RTNLGRP_LINK subscription and emits one
// LinkEvent on its output channel every time a watched interface's
// carrier or operstate changes.
//
// One Subscriber per daemon instance. Concurrent calls to Run are
// not supported; the caller serializes (the daemon's event loop
// owns the single goroutine that drives this).
type Subscriber struct {
	// Interfaces restricts emission to the named set. A nil map
	// means "emit for every interface" — convenient for tests and
	// debugging; the daemon configures the WAN interface set.
	Interfaces map[string]struct{}
}

// updateChanBuffer is the netlink-update channel capacity. Sized
// generously so a burst of link flaps doesn't drop messages while
// the daemon is mid-Decision. Each entry is a small struct, so the
// memory cost is negligible.
const updateChanBuffer = 64

// Run subscribes to RTNLGRP_LINK and pushes one LinkEvent onto
// `out` for every real carrier/operstate change on a watched
// interface. Returns when ctx is cancelled or the netlink
// subscription fails.
//
// `out` is *not* closed on return; callers can retry Run with a
// fresh goroutine and reuse the same channel after a transient
// failure.
func (s *Subscriber) Run(ctx context.Context, out chan<- LinkEvent) error {
	updates := make(chan netlink.LinkUpdate, updateChanBuffer)
	done := make(chan struct{})
	defer close(done)

	if err := netlink.LinkSubscribe(updates, done); err != nil {
		return fmt.Errorf("rtnl: LinkSubscribe: %w", err)
	}
	return s.runLoop(ctx, updates, out)
}

// runLoop is the channel-pumping body of Run, split out so tests
// can drive it with a fake update channel without opening a real
// netlink socket. Behavior: drain `updates`, fold each via
// handleUpdate, push the resulting events to `out`. Exits on ctx
// cancellation or when `updates` closes.
func (s *Subscriber) runLoop(ctx context.Context, updates <-chan netlink.LinkUpdate, out chan<- LinkEvent) error {
	state := make(map[string]LinkState)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case upd, ok := <-updates:
			if !ok {
				return fmt.Errorf("rtnl: subscription channel closed")
			}
			ev, emit := s.handleUpdate(state, upd)
			if !emit {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// handleUpdate folds a single netlink update into the running state
// map and returns the LinkEvent to emit (if any). Pure modulo the
// state map mutation — exposed for direct testing without spinning
// up a netlink socket.
func (s *Subscriber) handleUpdate(state map[string]LinkState, upd netlink.LinkUpdate) (LinkEvent, bool) {
	name := upd.Link.Attrs().Name
	if s.Interfaces != nil {
		if _, watch := s.Interfaces[name]; !watch {
			return LinkEvent{}, false
		}
	}
	cur := LinkState{
		Name:      name,
		Carrier:   carrierFromFlags(uint32(upd.IfInfomsg.Flags)),
		Operstate: Operstate(upd.Link.Attrs().OperState),
	}
	prev, seen := state[name]
	state[name] = cur
	if seen && prev.Carrier == cur.Carrier && prev.Operstate == cur.Operstate {
		return LinkEvent{}, false
	}
	return LinkEvent{
		Name:      cur.Name,
		Carrier:   cur.Carrier,
		Operstate: cur.Operstate,
		Time:      time.Now().UTC(),
	}, true
}

// carrierFromFlags maps the IFF_* bitmask carried in IfInfomsg.Flags
// to a Carrier value. IFF_LOWER_UP is the kernel's "link layer is
// operationally up" signal — the same bit `ip -d link show` prints
// as `LOWER_UP`. We deliberately *don't* check IFF_UP, which only
// reflects the admin-up flag set by `ip link set <if> up`.
func carrierFromFlags(flags uint32) Carrier {
	if flags&unix.IFF_LOWER_UP != 0 {
		return CarrierUp
	}
	return CarrierDown
}
