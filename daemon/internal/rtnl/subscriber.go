package rtnl

import (
	"context"
	"fmt"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Subscriber owns a netlink RTNLGRP_LINK subscription. Concurrent
// calls to Run are not supported.
type Subscriber struct {
	// Interfaces restricts emission to the named set. A nil map
	// means "emit for every interface".
	Interfaces map[string]struct{}
}

// updateChanBuffer is the netlink-update channel capacity —
// sized to absorb a flap burst without back-pressuring the kernel.
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

// runLoop drains `updates`, folds each via handleUpdate, and pushes
// resulting events to `out`. Exits on ctx cancellation or when
// `updates` closes. Split from Run so tests can drive it without a
// netlink socket.
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

// handleUpdate folds one netlink update into `state` and returns
// the LinkEvent to emit, if any.
func (s *Subscriber) handleUpdate(state map[string]LinkState, upd netlink.LinkUpdate) (LinkEvent, bool) {
	attrs := upd.Link.Attrs()
	name := attrs.Name
	if _, watch := s.Interfaces[name]; s.Interfaces != nil && !watch {
		return LinkEvent{}, false
	}
	carrier := carrierFromFlags(upd.IfInfomsg.Flags)
	operstate := Operstate(attrs.OperState)
	if prev, seen := state[name]; seen && prev.Carrier == carrier && prev.Operstate == operstate {
		return LinkEvent{}, false
	}
	state[name] = LinkState{Name: name, Carrier: carrier, Operstate: operstate}
	return LinkEvent{
		Name:      name,
		Carrier:   carrier,
		Operstate: operstate,
		Time:      time.Now().UTC(),
	}, true
}

// carrierFromFlags maps IFF_* into a Carrier. IFF_LOWER_UP is the
// physical-link signal (`ip -d link show` shows it as `LOWER_UP`);
// IFF_UP is admin-up and deliberately ignored — an admin-up
// interface with no cable is `down` for our purposes.
func carrierFromFlags(flags uint32) Carrier {
	if flags&unix.IFF_LOWER_UP != 0 {
		return CarrierUp
	}
	return CarrierDown
}
