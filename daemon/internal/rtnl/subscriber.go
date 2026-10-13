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
// subscriptions on the same LinkSubscriber are not supported.
type LinkSubscriber struct {
	// Interfaces restricts emission to the named set. A nil map
	// means "emit for every interface".
	Interfaces map[string]struct{}
}

// updateChanBuffer is the netlink-update channel capacity. Sized
// to absorb a flap burst — many links transitioning at once when an
// uplink renegotiates — without the library's receive goroutine
// blocking. Notifications queue here during the startup snapshot and
// trigger current-link reads afterward.
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

// Start subscribes to RTNLGRP_LINK before taking the initial link
// snapshot, queues one LinkEvent per watched interface on out, then
// reconciles live notifications. Subscribing first means a carrier
// change during startup cannot fall between the dump and the
// subscription. Each matching notification triggers a current-link
// read: a notification queued before the snapshot may describe
// history the snapshot already supersedes, and replaying it would
// show the selector a transient flap. Both phases share one state
// map, so a read that matches the last emitted state emits nothing.
//
// Start returns after queuing the snapshot, so each WAN's carrier
// and operstate is known on the event loop's first iteration.
// Without it the daemon starts every WAN at carrier-unknown — which
// it treats as down — so a probe result arriving before the first
// transition would leave an up WAN unselected. Unless a consumer is
// already reading, out must have room for every watched interface.
// The returned channel receives one terminal error and closes when
// ctx is cancelled or the subscription fails. Start does not close
// out.
func (s *LinkSubscriber) Start(ctx context.Context, out chan<- LinkEvent) (<-chan error, error) {
	return s.StartWith(ctx, netlink.LinkSubscribeWithOptions, netlink.LinkList, netlink.LinkByName, out)
}

// StartWith runs Start with supplied netlink operations. The
// subscription must close its update channel when done closes, just
// like LinkSubscribeWithOptions, and getFn must report a missing
// link with netlink.LinkNotFoundError, just like LinkByName.
func (s *LinkSubscriber) StartWith(ctx context.Context, subscribe linkSubscribeFn, listFn linkListFn, getFn linkByNameFn, out chan<- LinkEvent) (<-chan error, error) {
	updates := make(chan netlink.LinkUpdate, updateChanBuffer)
	done := make(chan struct{})
	// subErr captures the netlink library's fatal error from its
	// receive goroutine; translateSubClose reads it after the close.
	var subErr atomic.Pointer[error]
	opts := netlink.LinkSubscribeOptions{
		// ListExisting is omitted: the snapshot comes from LinkList
		// after subscribing, so it is queued before Start returns.
		ReceiveBufferSize: netlinkRcvBufBytes,
		ErrorCallback:     func(err error) { subErr.Store(&err) },
	}
	if err := subscribe(updates, done, opts); err != nil {
		close(done)
		return nil, fmt.Errorf("rtnl: LinkSubscribe: %w", err)
	}
	stop := func() { closeAndDrain(done, updates) }
	state := make(map[string]LinkState)
	if err := s.primeVia(ctx, listFn, state, out); err != nil {
		stop()
		return nil, err
	}
	exited := make(chan error, 1)
	go func() {
		defer close(exited)
		err := translateSubClose(s.runLoop(ctx, updates, getFn, state, out), &subErr, "link")
		stop()
		exited <- err
	}()
	return exited, nil
}

// linkSubscribeFn matches netlink.LinkSubscribeWithOptions.
type linkSubscribeFn func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, opts netlink.LinkSubscribeOptions) error

// linkListFn matches netlink.LinkList.
type linkListFn func() ([]netlink.Link, error)

// linkByNameFn matches netlink.LinkByName.
type linkByNameFn func(name string) (netlink.Link, error)

// primeVia queues snapshot events before the subscription is
// drained, recording each watched link in state.
func (s *LinkSubscriber) primeVia(ctx context.Context, listFn linkListFn, state map[string]LinkState, out chan<- LinkEvent) error {
	links, err := listFn()
	if err != nil {
		return fmt.Errorf("rtnl: LinkList: %w", err)
	}
	for _, link := range links {
		ev, emit := s.handleUpdate(state, linkUpdateFromLink(link))
		if !emit {
			continue
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// runLoop treats each update on a watched interface as an
// invalidation, reads that link's current state, and queues a
// LinkEvent when it differs from state. It exits on cancellation, a
// failed link read, or subscription closure.
func (s *LinkSubscriber) runLoop(ctx context.Context, updates <-chan netlink.LinkUpdate, getFn linkByNameFn, state map[string]LinkState, out chan<- LinkEvent) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case upd, ok := <-updates:
			if !ok {
				return errSubscriptionClosed
			}
			name := upd.Attrs().Name
			if !s.watches(name) {
				continue
			}
			current, err := reconcileVia(ctx, getFn, name)
			if err != nil {
				return err
			}
			ev, emit := s.handleUpdate(state, current)
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

// reconcileVia reads the named link's current state as a LinkUpdate.
// A link that no longer exists reads as RTM_DELLINK, so a stale
// delete cannot hide a re-created link and a stale add cannot revive
// a deleted one.
func reconcileVia(ctx context.Context, getFn linkByNameFn, name string) (netlink.LinkUpdate, error) {
	if err := ctx.Err(); err != nil {
		return netlink.LinkUpdate{}, err
	}
	link, err := getFn(name)
	var notFound netlink.LinkNotFoundError
	if errors.As(err, &notFound) {
		return netlink.LinkUpdate{
			Header: unix.NlMsghdr{Type: unix.RTM_DELLINK},
			Link:   &netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: name}},
		}, nil
	}
	if err != nil {
		return netlink.LinkUpdate{}, fmt.Errorf("rtnl: LinkByName %s: %w", name, err)
	}
	return linkUpdateFromLink(link), nil
}

// linkUpdateFromLink wraps a link read from LinkList or LinkByName as
// an RTM_NEWLINK update. Those reads carry the IFF_* flags in
// Attrs().RawFlags, whereas the subscription delivers them in the
// message header, so copy RawFlags into the IfInfomsg field
// handleUpdate reads.
func linkUpdateFromLink(link netlink.Link) netlink.LinkUpdate {
	upd := netlink.LinkUpdate{
		Header: unix.NlMsghdr{Type: unix.RTM_NEWLINK},
		Link:   link,
	}
	upd.Flags = link.Attrs().RawFlags
	return upd
}

// watches reports whether name is in the watched set; a nil set
// watches every interface.
func (s *LinkSubscriber) watches(name string) bool {
	if s.Interfaces == nil {
		return true
	}
	_, ok := s.Interfaces[name]
	return ok
}

// closeAndDrain stops a netlink subscription by closing done, then
// drains updates until the library closes it. The library sends
// decoded messages without selecting on done, so a full buffer would
// otherwise leave its receiver blocked after the socket is closed.
func closeAndDrain[T any](done chan<- struct{}, updates <-chan T) {
	close(done)
	for {
		if _, ok := <-updates; !ok {
			return
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
// transient interfaces (veth, dummy, …) come and go. A delete for an
// interface absent from `state` emits nothing: it was never sighted,
// or its removal was already reported, and runLoop reads a link that
// stays deleted as RTM_DELLINK on every later notification.
func (s *LinkSubscriber) handleUpdate(state map[string]LinkState, upd netlink.LinkUpdate) (LinkEvent, bool) {
	attrs := upd.Attrs()
	name := attrs.Name
	if !s.watches(name) {
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
	if deleted && !seen {
		return LinkEvent{}, false
	}
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
