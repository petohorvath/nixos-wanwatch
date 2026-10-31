package rtnl

import (
	"context"
	"fmt"
	"net"
	"slices"
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
// returns its default routes as that snapshot, then reconciles live
// notifications onto out. Each matching notification triggers a
// current-route read: its payload may describe history already
// superseded by the snapshot, even when it reaches the receiver after
// Start returns. Replaying that payload could regress consumers that
// immediately Apply the Gateway. Both phases own the interface cache
// sequentially.
//
// Returning the snapshot rather than queuing it lets the caller learn
// every Gateway before its event loop starts, and keeps a large dump
// from blocking on out before anything reads it. The returned channel
// receives one terminal error and closes when ctx is cancelled or the
// subscription fails. Start does not close out.
func (s *RouteSubscriber) Start(ctx context.Context, out chan<- RouteEvent) ([]RouteEvent, <-chan error, error) {
	return s.StartWith(ctx, netlink.RouteSubscribeWithOptions, netlink.RouteList, out)
}

// StartWith runs Start with supplied netlink operations. The subscription must
// close its update channel when done closes, just like RouteSubscribeWithOptions.
// This seam lets integration tests exercise discovery and its consumers together.
func (s *RouteSubscriber) StartWith(ctx context.Context, subscribe routeSubscribeFn, listFn routeListFn, out chan<- RouteEvent) ([]RouteEvent, <-chan error, error) {
	updates := make(chan netlink.RouteUpdate, routeUpdateBuffer)
	done := make(chan struct{})
	var subErr atomic.Pointer[error]
	opts := netlink.RouteSubscribeOptions{
		// ListExisting is deliberately omitted: the library sends
		// RTM_GETROUTE with an IfInfomsg instead of an RtMsg, which
		// recent kernels reject. Use RouteList after subscribing.
		ReceiveBufferSize:      netlinkRcvBufBytes,
		ReceiveBufferForceSize: true,
		ErrorCallback:          func(err error) { subErr.Store(&err) },
	}
	if err := subscribe(updates, done, opts); err != nil {
		close(done)
		return nil, nil, fmt.Errorf("rtnl: RouteSubscribe: %w", err)
	}
	stop := func() { closeAndDrain(done, updates) }
	snapshot, err := s.readSnapshot(ctx, listFn)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		stop()
		return nil, nil, err
	}
	exited := make(chan error, 1)
	go func() {
		defer close(exited)
		err := translateSubClose(s.runLoop(ctx, updates, listFn, out), &subErr, "route")
		stop()
		exited <- err
	}()
	return snapshot, exited, nil
}

// routeListFn matches netlink.RouteList.
type routeListFn func(link netlink.Link, family int) ([]netlink.Route, error)

// routeSubscribeFn matches netlink.RouteSubscribeWithOptions.
type routeSubscribeFn func(ch chan<- netlink.RouteUpdate, done <-chan struct{}, opts netlink.RouteSubscribeOptions) error

// readSnapshot reads the route snapshot before the subscription is
// drained and returns one Add per watched interface and Family: the
// Gateway of its preferred default (see selectDefaults). An
// interrupted dump of a Family is read again.
func (s *RouteSubscriber) readSnapshot(ctx context.Context, listFn routeListFn) ([]RouteEvent, error) {
	if s.ifaceLookup == nil {
		s.ifaceLookup = interfaceNameByIndex
	}
	var snapshot []RouteEvent
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		routes, err := retryDump(ctx, func() ([]netlink.Route, error) { return listFn(nil, family) })
		if err != nil {
			return nil, fmt.Errorf("rtnl: RouteList family=%d: %w", family, err)
		}
		snapshot = append(snapshot, s.selectDefaults(routes)...)
	}
	return snapshot, nil
}

// selectDefaults returns one Add per watched interface and Family in a
// route dump, in the order the pairs first appear. Each carries the
// Gateway of the pair's preferred default: the lowest metric, ties to
// the first listed. For IPv6, the kernel may route through another
// equal-metric default — see docs/architecture.md.
func (s *RouteSubscriber) selectDefaults(routes []netlink.Route) []RouteEvent {
	var selected []RouteEvent
	metrics := make(map[routeKey]int)
	index := make(map[routeKey]int)
	for _, route := range routes {
		for _, c := range s.handleUpdate(netlink.RouteUpdate{Type: unix.RTM_NEWROUTE, Route: route}) {
			key := c.key()
			i, seen := index[key]
			switch {
			case !seen:
				index[key] = len(selected)
				metrics[key] = c.metric
				selected = append(selected, c.RouteEvent)
			case c.metric < metrics[key]:
				metrics[key] = c.metric
				selected[i] = c.RouteEvent
			}
		}
	}
	return selected
}

// runLoop treats matching updates as invalidations. For each watched
// interface an update touches, such as every interface a multipath
// default spans, it reads that interface/family's current default and
// queues that observation on out. It exits on cancellation, a failed
// route read, or subscription closure.
func (s *RouteSubscriber) runLoop(ctx context.Context, updates <-chan netlink.RouteUpdate, listFn routeListFn, out chan<- RouteEvent) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case upd, ok := <-updates:
			if !ok {
				return errSubscriptionClosed
			}
			for _, c := range s.handleUpdate(upd) {
				ev, err := s.reconcileVia(ctx, listFn, c.RouteEvent)
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
}

// reconcileVia reads the current routes of ev's Family and returns an
// Add for the interface's preferred default, the one readSnapshot would
// select, or a Del only if the interface has no default left. A stale
// delete must not clear a replacement, so an interrupted dump is read
// again rather than trusted.
func (s *RouteSubscriber) reconcileVia(ctx context.Context, listFn routeListFn, ev RouteEvent) (RouteEvent, error) {
	if err := ctx.Err(); err != nil {
		return RouteEvent{}, err
	}
	routes, err := retryDump(ctx, func() ([]netlink.Route, error) { return listFn(nil, int(ev.Family)) })
	if err != nil {
		return RouteEvent{}, fmt.Errorf("rtnl: RouteList family=%d: %w", ev.Family, err)
	}
	ev.Op = RouteEventDel
	for _, current := range s.selectDefaults(routes) {
		if current.Iface == ev.Iface && current.Family == ev.Family {
			ev = current
			break
		}
	}
	ev.Time = time.Now().UTC()
	return ev, nil
}

// routeKey identifies one watched interface and Family.
type routeKey struct {
	iface  string
	family RouteFamily
}

// routeCandidate is one default route's next-hop on one watched
// interface, with the route's metric.
type routeCandidate struct {
	RouteEvent
	metric int
}

func (c routeCandidate) key() routeKey { return routeKey{c.Iface, c.Family} }

// handleUpdate folds one RouteUpdate into a routeCandidate per watched
// interface it touches: the route's own LinkIndex for a single-path
// route, or each nexthop's LinkIndex for a multipath route. When several
// nexthops of one route sit on the same interface, the first one listed
// wins. Each candidate carries the route's metric (RTA_PRIORITY).
//
// Filters applied (returning no candidates when any fails):
//
//   - Table must be `RT_TABLE_MAIN` (254). The daemon's own
//     route writes go to per-group tables and would otherwise
//     loop back as discovery events.
//   - Destination must be the family-zero address ("default
//     route" — Dst.IP.IsUnspecified() or Dst == nil).
//   - The message type must be RTM_NEWROUTE or RTM_DELROUTE;
//     others (RTM_GETROUTE responses outside ListExisting) are
//     ignored.
//
// A LinkIndex must also resolve to a watched interface; nexthops on
// unwatchable interfaces (lookup error) are dropped silently, since
// they can't be a WAN we care about.
func (s *RouteSubscriber) handleUpdate(upd netlink.RouteUpdate) []routeCandidate {
	if upd.Type != unix.RTM_NEWROUTE && upd.Type != unix.RTM_DELROUTE {
		return nil
	}
	if upd.Table != unix.RT_TABLE_MAIN {
		return nil
	}
	if !isDefaultRoute(upd.Route) {
		return nil
	}
	op := RouteEventAdd
	if upd.Type == unix.RTM_DELROUTE {
		op = RouteEventDel
	}
	nexthops := upd.MultiPath
	if len(nexthops) == 0 {
		nexthops = []*netlink.NexthopInfo{{LinkIndex: upd.LinkIndex, Gw: upd.Gw}}
	}
	now := time.Now().UTC()
	var candidates []routeCandidate
	for _, nh := range nexthops {
		name, ok := s.resolveIface(nh.LinkIndex)
		if !ok {
			continue
		}
		if _, watch := s.Interfaces[name]; s.Interfaces != nil && !watch {
			continue
		}
		c := routeCandidate{
			RouteEvent: RouteEvent{
				Op:      op,
				Iface:   name,
				Family:  routeFamilyFromAF(upd.Family),
				Gateway: nh.Gw,
				Time:    now,
			},
			metric: upd.Priority,
		}
		if !slices.ContainsFunc(candidates, func(prev routeCandidate) bool { return prev.Iface == name }) {
			candidates = append(candidates, c)
		}
	}
	return candidates
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
