package rtnl

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// LinkSubscriber owns a netlink RTNLGRP_LINK subscription. Concurrent
// calls to Run are not supported.
type LinkSubscriber struct {
	// Interfaces restricts emission to the named set. A nil map
	// means "emit for every interface".
	Interfaces map[string]struct{}
}

// updateChanBuffer is the netlink-update channel capacity. Sized
// to absorb both the ListExisting startup dump (one update per
// existing link — bridges, vlans, dockers can push this past 64
// on a busy host) and subsequent flap bursts.
const updateChanBuffer = 256

// errSubscriptionClosed is returned by runLoop when the netlink
// update channel closes — i.e. the library's receive goroutine
// exited. translateSubClose maps it to the concrete cause captured
// by the ErrorCallback (an ENOBUFS overflow, a socket error, …) so a
// dead subscription is diagnosable rather than a bare "channel
// closed".
var errSubscriptionClosed = errors.New("rtnl: subscription channel closed")

// netlinkRcvBufBytes is the SO_RCVBUF size requested for the
// rtnetlink subscription sockets. The kernel drops messages with
// ENOBUFS when the socket buffer overflows during a flap storm —
// and the library reports that as a fatal error — so size the
// buffer up front to make overflow rare. Shared by both subscribers.
const netlinkRcvBufBytes = 1 << 20

// translateSubClose maps runLoop's errSubscriptionClosed sentinel to
// the concrete failure the netlink library reported through its
// ErrorCallback (captured in subErr). `label` names the subscription
// ("link" / "route") in the wrapped message. Any other error — and a
// sentinel with no captured cause — passes through unchanged.
//
// subErr is written from the library's receive goroutine and read
// here only after runLoop has observed `updates` closed; that close
// is the happens-before edge, and atomic.Pointer makes it explicit.
func translateSubClose(err error, subErr *atomic.Pointer[error], label string) error {
	if errors.Is(err, errSubscriptionClosed) {
		if cause := subErr.Load(); cause != nil {
			return fmt.Errorf("rtnl: %s subscription ended: %w", label, *cause)
		}
	}
	return err
}

// Run subscribes to RTNLGRP_LINK and pushes one LinkEvent onto
// `out` for every real carrier/operstate change on a watched
// interface. Returns when ctx is cancelled or the netlink
// subscription fails.
//
// `out` is *not* closed on return; callers can retry Run with a
// fresh goroutine and reuse the same channel after a transient
// failure.
func (s *LinkSubscriber) Run(ctx context.Context, out chan<- LinkEvent) error {
	return s.runVia(ctx, netlink.LinkSubscribeWithOptions, out)
}

// linkSubscribeFn matches netlink.LinkSubscribeWithOptions and is
// the seam LinkSubscriber.runVia exposes for tests: real production
// uses the netlink call; tests inject a stub that either errors
// or fills the `updates` channel with synthetic LinkUpdates.
type linkSubscribeFn func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, opts netlink.LinkSubscribeOptions) error

// runVia is Run parameterized on the subscription function — the
// only piece of Run that needs a netlink socket. Tests drive this
// directly to cover the error wrapping and the runLoop wire-up.
func (s *LinkSubscriber) runVia(ctx context.Context, subscribe linkSubscribeFn, out chan<- LinkEvent) error {
	updates := make(chan netlink.LinkUpdate, updateChanBuffer)
	done := make(chan struct{})
	defer close(done)

	// subErr captures the netlink library's fatal error from its
	// receive goroutine; translateSubClose reads it after the close.
	var subErr atomic.Pointer[error]
	opts := netlink.LinkSubscribeOptions{
		// ListExisting dumps every current link as an RTM_NEWLINK so
		// the daemon learns carrier/operstate at boot without waiting
		// for the first transition.
		ListExisting:      true,
		ReceiveBufferSize: netlinkRcvBufBytes,
		ErrorCallback:     func(err error) { subErr.Store(&err) },
	}
	if err := subscribe(updates, done, opts); err != nil {
		return fmt.Errorf("rtnl: LinkSubscribe: %w", err)
	}
	return translateSubClose(s.runLoop(ctx, updates, out), &subErr, "link")
}

// runLoop drains `updates`, folds each via handleUpdate, and pushes
// resulting events to `out`. Exits on ctx cancellation or when
// `updates` closes. Split from Run so tests can drive it without a
// netlink socket.
func (s *LinkSubscriber) runLoop(ctx context.Context, updates <-chan netlink.LinkUpdate, out chan<- LinkEvent) error {
	state := make(map[string]LinkState)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case upd, ok := <-updates:
			if !ok {
				return errSubscriptionClosed
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
//
// RTM_DELLINK is reported as carrier=down / operstate=notpresent
// rather than dropped silently — a vanished WAN must drive the
// selector out of that member, not leave a stale snapshot. The
// state entry is removed either way so the map stays bounded as
// transient interfaces (veth, dummy, …) come and go.
func (s *LinkSubscriber) handleUpdate(state map[string]LinkState, upd netlink.LinkUpdate) (LinkEvent, bool) {
	attrs := upd.Attrs()
	name := attrs.Name
	if _, watch := s.Interfaces[name]; s.Interfaces != nil && !watch {
		return LinkEvent{}, false
	}

	deleted := upd.Header.Type == unix.RTM_DELLINK
	carrier := CarrierDown
	operstate := OperstateNotPresent
	if !deleted {
		carrier = carrierFromFlags(upd.Flags)
		operstate = Operstate(attrs.OperState)
	}

	prev, seen := state[name]
	unchanged := seen && prev.Carrier == carrier && prev.Operstate == operstate
	if deleted {
		delete(state, name)
	} else if !unchanged {
		state[name] = LinkState{Name: name, Carrier: carrier, Operstate: operstate}
	}
	if unchanged {
		return LinkEvent{}, false
	}
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
