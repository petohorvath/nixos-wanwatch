package rtnl

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// routeUpdateBuffer is the netlink RouteUpdate channel capacity.
// Sized like the link subscriber's: enough to absorb the ListExisting
// startup dump (one update per existing default route across both
// families and every table on a busy host) plus subsequent route
// flap bursts when an uplink renegotiates.
const routeUpdateBuffer = 256

// RouteSubscriber owns an rtnetlink RTNLGRP_IPV4_ROUTE +
// RTNLGRP_IPV6_ROUTE subscription. It filters down to default
// routes installed in the main routing table on watched interfaces
// and emits a `RouteEvent` for every add/del.
//
// Concurrent calls to Run are not supported.
type RouteSubscriber struct {
	// Interfaces restricts emission to the named set. A nil map
	// means "emit for every interface" — useful for tests and for
	// the rare deployment where every link is potentially a WAN.
	Interfaces map[string]struct{}

	// ifaceLookup resolves a LinkIndex to its interface name. The
	// production value is `interfaceNameByIndex` (calls
	// net.InterfaceByIndex); tests inject a map-backed stub.
	ifaceLookup func(int) (string, error)

	// ifaceCache memoizes ifindex → name so the ListExisting
	// startup dump doesn't pay a syscall per route message. An
	// interface rename leaves a stale entry that surfaces a wrong
	// name; the daemon drops events naming a wan it doesn't know
	// about, so the worst case is one silently-skipped flap.
	ifaceCache map[int]string
}

// Run subscribes to RTNLGRP_IPV4_ROUTE + RTNLGRP_IPV6_ROUTE and
// pushes one `RouteEvent` onto `out` for every default-route
// add/del on a watched interface in the main routing table.
// Returns when ctx is cancelled or the netlink subscription
// fails.
//
// `out` is *not* closed on return; callers can retry Run with a
// fresh goroutine and reuse the same channel after a transient
// failure.
func (s *RouteSubscriber) Run(ctx context.Context, out chan<- RouteEvent) error {
	updates := make(chan netlink.RouteUpdate, routeUpdateBuffer)
	done := make(chan struct{})
	defer close(done)

	// ListExisting dumps every current route so the daemon can
	// populate its gateway cache at boot without waiting for a
	// link to flap. RTNLGRP_*_ROUTE multicast deliveries follow.
	opts := netlink.RouteSubscribeOptions{ListExisting: true}
	if err := netlink.RouteSubscribeWithOptions(updates, done, opts); err != nil {
		return fmt.Errorf("rtnl: RouteSubscribe: %w", err)
	}
	if s.ifaceLookup == nil {
		s.ifaceLookup = interfaceNameByIndex
	}
	return s.runLoop(ctx, updates, out)
}

// runLoop drains `updates`, folds each via handleUpdate, and
// pushes resulting events to `out`. Exits on ctx cancellation or
// when `updates` closes. Split from Run so tests can drive it
// without a netlink socket.
func (s *RouteSubscriber) runLoop(ctx context.Context, updates <-chan netlink.RouteUpdate, out chan<- RouteEvent) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case upd, ok := <-updates:
			if !ok {
				return fmt.Errorf("rtnl: route subscription channel closed")
			}
			ev, emit := s.handleUpdate(upd)
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
	if upd.Route.Table != unix.RT_TABLE_MAIN {
		return RouteEvent{}, false
	}
	if !isDefaultRoute(upd.Route) {
		return RouteEvent{}, false
	}
	name, ok := s.resolveIface(upd.Route.LinkIndex)
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
		Family:  routeFamilyFromAF(upd.Route.Family),
		Gateway: upd.Route.Gw,
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
