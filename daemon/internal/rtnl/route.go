package rtnl

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// routeUpdateBuffer is the netlink RouteUpdate channel capacity.
// Sized like updateChanBuffer: enough to absorb a route-flap burst
// when an uplink renegotiates without the library's receive
// goroutine blocking. Notifications queue here during the startup
// snapshot and trigger current-route reads afterward.
const routeUpdateBuffer = 256

// RouteSubscriber owns an rtnetlink RTNLGRP_IPV4_ROUTE +
// RTNLGRP_IPV6_ROUTE subscription. It filters down to default
// routes installed in the main routing table on watched interfaces
// and emits RouteEvents describing their current state.
//
// Concurrent subscriptions on the same RouteSubscriber are not supported.
type RouteSubscriber struct {
	// Interfaces restricts emission to the named set. A nil map
	// means "emit for every interface" — useful for tests and for
	// the rare deployment where every link is potentially a WAN.
	Interfaces map[string]struct{}

	// ifaceLookup resolves a LinkIndex to its interface name. The
	// production value is `interfaceNameByIndex` (calls
	// net.InterfaceByIndex); tests inject a map-backed stub.
	ifaceLookup func(int) (string, error)

	// ifaceCache memoizes ifindex → name so the route
	// startup dump doesn't pay a syscall per route message. An
	// interface rename leaves a stale entry that surfaces a wrong
	// name; the daemon drops events naming a wan it doesn't know
	// about, so the worst case is one silently-skipped flap.
	ifaceCache map[int]string
}

// Start subscribes before taking the initial v4 + v6 route snapshot,
// queues its default routes on out, then reconciles live notifications.
// Each matching notification triggers a current-route read: its payload may
// describe history already superseded by the snapshot, even when it reaches
// the receiver after Start returns. Replaying that payload could regress
// consumers that immediately Apply the Gateway. Both phases own the
// interface cache sequentially.
//
// Start returns after queuing the snapshot. Unless a consumer is already
// reading, out must have room for every matching route. The returned
// channel receives one terminal error and closes when ctx is cancelled
// or the subscription fails. Start does not close out.
func (s *RouteSubscriber) Start(ctx context.Context, out chan<- RouteEvent) (<-chan error, error) {
	return s.StartWith(ctx, netlink.RouteSubscribeWithOptions, netlink.RouteList, out)
}

// StartWith runs Start with supplied netlink operations. The subscription must
// close its update channel when done closes, just like RouteSubscribeWithOptions.
// This seam lets integration tests exercise discovery and its consumers together.
func (s *RouteSubscriber) StartWith(ctx context.Context, subscribe routeSubscribeFn, listFn routeListFn, out chan<- RouteEvent) (<-chan error, error) {
	updates := make(chan netlink.RouteUpdate, routeUpdateBuffer)
	done := make(chan struct{})
	var subErr atomic.Pointer[error]
	opts := netlink.RouteSubscribeOptions{
		// ListExisting is deliberately omitted: the library sends
		// RTM_GETROUTE with an IfInfomsg instead of an RtMsg, which
		// recent kernels reject. Use RouteList after subscribing.
		ReceiveBufferSize: netlinkRcvBufBytes,
		ErrorCallback:     func(err error) { subErr.Store(&err) },
	}
	if err := subscribe(updates, done, opts); err != nil {
		close(done)
		return nil, fmt.Errorf("rtnl: RouteSubscribe: %w", err)
	}
	stop := func() {
		close(done)
		// The library sends decoded messages without selecting on
		// done. Drain until it closes updates so a full buffer cannot
		// leave its receiver blocked after the socket is closed.
		for {
			if _, ok := <-updates; !ok {
				return
			}
		}
	}
	if err := s.primeVia(ctx, listFn, out); err != nil {
		stop()
		return nil, err
	}
	exited := make(chan error, 1)
	go func() {
		defer close(exited)
		err := translateSubClose(s.runLoop(ctx, updates, listFn, out), &subErr, "route")
		stop()
		exited <- err
	}()
	return exited, nil
}

// routeListFn matches netlink.RouteList.
type routeListFn func(link netlink.Link, family int) ([]netlink.Route, error)

// routeSubscribeFn matches netlink.RouteSubscribeWithOptions.
type routeSubscribeFn func(ch chan<- netlink.RouteUpdate, done <-chan struct{}, opts netlink.RouteSubscribeOptions) error

// primeVia queues snapshot events before the subscription is drained.
func (s *RouteSubscriber) primeVia(ctx context.Context, listFn routeListFn, out chan<- RouteEvent) error {
	if s.ifaceLookup == nil {
		s.ifaceLookup = interfaceNameByIndex
	}
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		routes, err := listFn(nil, family)
		if err != nil {
			return fmt.Errorf("rtnl: RouteList family=%d: %w", family, err)
		}
		for _, r := range routes {
			ev, emit := s.handleUpdate(netlink.RouteUpdate{
				Type:  unix.RTM_NEWROUTE,
				Route: r,
			})
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
	return nil
}

// runLoop treats matching updates as invalidations, reads the affected
// interface/family's current default, and queues that observation on out.
// It exits on cancellation, a failed route read, or subscription closure.
func (s *RouteSubscriber) runLoop(ctx context.Context, updates <-chan netlink.RouteUpdate, listFn routeListFn, out chan<- RouteEvent) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case upd, ok := <-updates:
			if !ok {
				return errSubscriptionClosed
			}
			ev, emit := s.handleUpdate(upd)
			if !emit {
				continue
			}
			ev, err := s.reconcileVia(ctx, listFn, ev)
			if err != nil {
				return err
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// reconcileVia returns an Add for the current default, or a Del only if
// the interface/family has none. A stale delete must not clear a replacement.
func (s *RouteSubscriber) reconcileVia(ctx context.Context, listFn routeListFn, ev RouteEvent) (RouteEvent, error) {
	if err := ctx.Err(); err != nil {
		return RouteEvent{}, err
	}
	routes, err := listFn(nil, int(ev.Family))
	if err != nil {
		return RouteEvent{}, fmt.Errorf("rtnl: RouteList family=%d: %w", ev.Family, err)
	}
	ev.Op = RouteEventDel
	for _, route := range routes {
		current, emit := s.handleUpdate(netlink.RouteUpdate{Type: unix.RTM_NEWROUTE, Route: route})
		if emit && current.Iface == ev.Iface && current.Family == ev.Family {
			// Match the snapshot's last default for this pair when the
			// kernel lists more than one, without emitting each candidate.
			ev = current
		}
	}
	ev.Time = time.Now().UTC()
	return ev, nil
}

// handleUpdate folds one RouteUpdate into a RouteEvent.
//
// Filters applied (returning emit=false when any fails):
//
//   - Table must be `RT_TABLE_MAIN` (254). The daemon's own
//     route writes go to per-group tables and would otherwise
//     loop back as discovery events.
//   - Destination must be the family-zero address ("default
//     route" — Dst.IP.IsUnspecified() or Dst == nil).
//   - The route's LinkIndex must resolve to a watched
//     interface. Unwatchable interfaces (LinkByIndex error)
//     are dropped silently — they can't be a WAN we care about.
//   - The message type must be RTM_NEWROUTE or RTM_DELROUTE;
//     others (RTM_GETROUTE responses outside ListExisting) are
//     ignored.
func (s *RouteSubscriber) handleUpdate(upd netlink.RouteUpdate) (RouteEvent, bool) {
	if upd.Type != unix.RTM_NEWROUTE && upd.Type != unix.RTM_DELROUTE {
		return RouteEvent{}, false
	}
	if upd.Table != unix.RT_TABLE_MAIN {
		return RouteEvent{}, false
	}
	if !isDefaultRoute(upd.Route) {
		return RouteEvent{}, false
	}
	name, ok := s.resolveIface(upd.LinkIndex)
	if !ok {
		return RouteEvent{}, false
	}
	if _, watch := s.Interfaces[name]; s.Interfaces != nil && !watch {
		return RouteEvent{}, false
	}
	op := RouteEventAdd
	if upd.Type == unix.RTM_DELROUTE {
		op = RouteEventDel
	}
	return RouteEvent{
		Op:      op,
		Iface:   name,
		Family:  routeFamilyFromAF(upd.Family),
		Gateway: upd.Gw,
		Time:    time.Now().UTC(),
	}, true
}

// resolveIface returns the interface name for `idx`, consulting
// the per-subscriber cache before falling back to the lookup
// function. Returns (_, false) if the lookup fails (idx not on a
// live link).
func (s *RouteSubscriber) resolveIface(idx int) (string, bool) {
	if name, ok := s.ifaceCache[idx]; ok {
		return name, true
	}
	name, err := s.ifaceLookup(idx)
	if err != nil {
		return "", false
	}
	if s.ifaceCache == nil {
		s.ifaceCache = make(map[int]string)
	}
	s.ifaceCache[idx] = name
	return name, true
}

// isDefaultRoute returns true iff `r` is the default route for its
// family. The kernel encodes "default" two ways depending on the
// netlink message path: Dst==nil, or Dst.IP being the all-zero
// address with a zero-bit prefix. Both must be accepted.
func isDefaultRoute(r netlink.Route) bool {
	if r.Dst == nil {
		return true
	}
	if r.Dst.IP == nil {
		return true
	}
	ones, _ := r.Dst.Mask.Size()
	return ones == 0 && r.Dst.IP.IsUnspecified()
}

// routeFamilyFromAF converts the kernel's AF_INET / AF_INET6
// integer (carried in netlink.Route.Family) into our enum. Any
// unknown value defaults to v4 — the caller will have to drop the
// event downstream if it surfaces an unsupported family, but a
// dropped event is better than a panic on a future ABI bump.
func routeFamilyFromAF(family int) RouteFamily {
	if family == unix.AF_INET6 {
		return RouteFamilyV6
	}
	return RouteFamilyV4
}

// interfaceNameByIndex is the production ifaceLookup. Wrapped over
// net.InterfaceByIndex so tests can inject a stub without dragging
// in a real netlink socket.
func interfaceNameByIndex(idx int) (string, error) {
	link, err := net.InterfaceByIndex(idx)
	if err != nil {
		return "", err
	}
	return link.Name, nil
}
