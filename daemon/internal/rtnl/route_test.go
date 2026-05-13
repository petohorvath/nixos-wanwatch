package rtnl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// mkRouteUpdate builds a RouteUpdate shaped like one of the
// kernel's RTM_NEWROUTE/RTM_DELROUTE messages, so handleUpdate
// can be driven without a real netlink socket.
func mkRouteUpdate(msgType uint16, family int, table int, gw net.IP, ifindex int, dst *net.IPNet) netlink.RouteUpdate {
	return netlink.RouteUpdate{
		Type: msgType,
		Route: netlink.Route{
			Family:    family,
			Table:     table,
			Gw:        gw,
			LinkIndex: ifindex,
			Dst:       dst,
		},
	}
}

// mkSub builds a RouteSubscriber whose ifaceLookup resolves
// indexes via `idx2name`. Tests that pass an empty map will not
// resolve any interface — the handler should drop those events.
func mkSub(watched map[string]struct{}, idx2name map[int]string) *RouteSubscriber {
	return &RouteSubscriber{
		Interfaces: watched,
		ifaceLookup: func(idx int) (string, error) {
			if name, ok := idx2name[idx]; ok {
				return name, nil
			}
			return "", fmt.Errorf("no iface at index %d", idx)
		},
	}
}

func TestRouteFamilyStrings(t *testing.T) {
	t.Parallel()
	if RouteFamilyV4.String() != "v4" {
		t.Errorf("RouteFamilyV4 = %q, want v4", RouteFamilyV4.String())
	}
	if RouteFamilyV6.String() != "v6" {
		t.Errorf("RouteFamilyV6 = %q, want v6", RouteFamilyV6.String())
	}
}

func TestRouteEventOpStrings(t *testing.T) {
	t.Parallel()
	if RouteEventAdd.String() != "add" {
		t.Errorf("RouteEventAdd = %q, want add", RouteEventAdd.String())
	}
	if RouteEventDel.String() != "del" {
		t.Errorf("RouteEventDel = %q, want del", RouteEventDel.String())
	}
}

func TestIsDefaultRouteAcceptsNilDst(t *testing.T) {
	t.Parallel()
	if !isDefaultRoute(netlink.Route{Dst: nil}) {
		t.Error("Dst==nil not recognized as default")
	}
}

func TestIsDefaultRouteAcceptsZeroIP(t *testing.T) {
	t.Parallel()
	r := netlink.Route{Dst: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}}
	if !isDefaultRoute(r) {
		t.Error("0.0.0.0/0 not recognized as default")
	}
}

func TestIsDefaultRouteRejectsSpecific(t *testing.T) {
	t.Parallel()
	_, dst, _ := net.ParseCIDR("192.0.2.0/24")
	if isDefaultRoute(netlink.Route{Dst: dst}) {
		t.Error("192.0.2.0/24 misclassified as default")
	}
}

func TestRouteFamilyFromAFV4(t *testing.T) {
	t.Parallel()
	if routeFamilyFromAF(unix.AF_INET) != RouteFamilyV4 {
		t.Error("AF_INET did not map to RouteFamilyV4")
	}
}

func TestRouteFamilyFromAFV6(t *testing.T) {
	t.Parallel()
	if routeFamilyFromAF(unix.AF_INET6) != RouteFamilyV6 {
		t.Error("AF_INET6 did not map to RouteFamilyV6")
	}
}

func TestRouteFamilyFromAFDefaultsV4(t *testing.T) {
	t.Parallel()
	// Unknown AF (future-proofing): default to v4 rather than
	// panicking. Caller drops events of any family it doesn't
	// understand at the next layer.
	if routeFamilyFromAF(99) != RouteFamilyV4 {
		t.Error("unknown AF did not default to v4")
	}
}

func TestRouteHandleUpdateEmitsAdd(t *testing.T) {
	t.Parallel()
	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{3: "eth0"})
	gw := net.ParseIP("192.0.2.1")
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, gw, 3, nil)

	ev, ok := s.handleUpdate(upd)
	if !ok {
		t.Fatal("default-route add did not emit")
	}
	if ev.Op != RouteEventAdd {
		t.Errorf("Op = %v, want Add", ev.Op)
	}
	if ev.Iface != "eth0" {
		t.Errorf("Iface = %q, want eth0", ev.Iface)
	}
	if ev.Family != RouteFamilyV4 {
		t.Errorf("Family = %v, want v4", ev.Family)
	}
	if !ev.Gateway.Equal(gw) {
		t.Errorf("Gateway = %v, want %v", ev.Gateway, gw)
	}
	if ev.Time.IsZero() {
		t.Error("Time = zero; want stamped")
	}
}

func TestRouteHandleUpdateEmitsDel(t *testing.T) {
	t.Parallel()
	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{3: "eth0"})
	upd := mkRouteUpdate(unix.RTM_DELROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("192.0.2.1"), 3, nil)

	ev, ok := s.handleUpdate(upd)
	if !ok {
		t.Fatal("default-route del did not emit")
	}
	if ev.Op != RouteEventDel {
		t.Errorf("Op = %v, want Del", ev.Op)
	}
}

func TestRouteHandleUpdateEmitsV6(t *testing.T) {
	t.Parallel()
	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{3: "eth0"})
	gw := net.ParseIP("2001:db8::1")
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET6, unix.RT_TABLE_MAIN, gw, 3, nil)

	ev, ok := s.handleUpdate(upd)
	if !ok {
		t.Fatal("v6 default-route add did not emit")
	}
	if ev.Family != RouteFamilyV6 {
		t.Errorf("Family = %v, want v6", ev.Family)
	}
	if !ev.Gateway.Equal(gw) {
		t.Errorf("Gateway = %v, want %v", ev.Gateway, gw)
	}
}

func TestRouteHandleUpdateEmitsScopeLink(t *testing.T) {
	t.Parallel()
	// Point-to-point links install scope-link defaults with no
	// Gw set — the subscriber must still surface them (Gateway
	// = nil signals "interface, no next-hop").
	s := mkSub(map[string]struct{}{"wg0": {}}, map[int]string{5: "wg0"})
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, nil, 5, nil)

	ev, ok := s.handleUpdate(upd)
	if !ok {
		t.Fatal("scope-link default did not emit")
	}
	if ev.Gateway != nil {
		t.Errorf("Gateway = %v, want nil for point-to-point", ev.Gateway)
	}
}

func TestRouteHandleUpdateFiltersNonMainTable(t *testing.T) {
	t.Parallel()
	// Routes the daemon itself writes go to per-group tables.
	// Without this filter, the daemon would see its own writes
	// echoed back as discovery events and could loop.
	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{3: "eth0"})
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, 100, net.ParseIP("192.0.2.1"), 3, nil)

	if _, ok := s.handleUpdate(upd); ok {
		t.Error("table=100 emitted; want filtered")
	}
}

func TestRouteHandleUpdateFiltersNonDefault(t *testing.T) {
	t.Parallel()
	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{3: "eth0"})
	_, dst, _ := net.ParseCIDR("192.0.2.0/24")
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("192.0.2.1"), 3, dst)

	if _, ok := s.handleUpdate(upd); ok {
		t.Error("specific-prefix route emitted; want filtered")
	}
}

func TestRouteHandleUpdateFiltersByInterfaceSet(t *testing.T) {
	t.Parallel()
	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{3: "eth0", 4: "wwan0"})
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("100.64.0.1"), 4, nil)

	if _, ok := s.handleUpdate(upd); ok {
		t.Error("wwan0 emitted despite not being in watched set")
	}
}

func TestRouteHandleUpdateNilInterfaceSetMatchesAll(t *testing.T) {
	t.Parallel()
	s := mkSub(nil, map[int]string{3: "eth0"})
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("192.0.2.1"), 3, nil)

	if _, ok := s.handleUpdate(upd); !ok {
		t.Error("nil Interfaces set did not match every interface")
	}
}

func TestRouteHandleUpdateDropsUnknownIfindex(t *testing.T) {
	t.Parallel()
	// LinkByIndex fails for ifindex with no live link. The handler
	// drops these silently — we can't safely emit an event without
	// a name to attribute it to.
	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{})
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("192.0.2.1"), 99, nil)

	if _, ok := s.handleUpdate(upd); ok {
		t.Error("update with unresolvable ifindex emitted; want dropped")
	}
}

func TestRouteHandleUpdateIgnoresUnknownMsgType(t *testing.T) {
	t.Parallel()
	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{3: "eth0"})
	upd := mkRouteUpdate(unix.RTM_GETROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("192.0.2.1"), 3, nil)

	if _, ok := s.handleUpdate(upd); ok {
		t.Error("RTM_GETROUTE emitted; want ignored")
	}
}

func TestRouteHandleUpdateCachesIfaceLookup(t *testing.T) {
	t.Parallel()
	// Each lookup call increments `calls`. After two updates on
	// the same ifindex, ifaceLookup must have been invoked once.
	var calls int
	s := &RouteSubscriber{
		Interfaces: map[string]struct{}{"eth0": {}},
		ifaceLookup: func(int) (string, error) {
			calls++
			return "eth0", nil
		},
	}
	upd := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("192.0.2.1"), 3, nil)

	if _, ok := s.handleUpdate(upd); !ok {
		t.Fatal("first update did not emit")
	}
	if _, ok := s.handleUpdate(upd); !ok {
		t.Fatal("second update did not emit")
	}
	if calls != 1 {
		t.Errorf("ifaceLookup called %d times, want 1 (cache miss only on first)", calls)
	}
}

func TestRouteRunLoopForwardsEvent(t *testing.T) {
	t.Parallel()
	updates := make(chan netlink.RouteUpdate, 1)
	out := make(chan RouteEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := mkSub(map[string]struct{}{"eth0": {}}, map[int]string{3: "eth0"})
	updates <- mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("192.0.2.1"), 3, nil)

	errCh := make(chan error, 1)
	go func() { errCh <- s.runLoop(ctx, updates, out) }()

	select {
	case ev := <-out:
		if ev.Iface != "eth0" || ev.Op != RouteEventAdd {
			t.Errorf("event = %+v, want eth0/add", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runLoop to forward event")
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRouteRunLoopReturnsOnUpdatesClosed(t *testing.T) {
	t.Parallel()
	updates := make(chan netlink.RouteUpdate)
	out := make(chan RouteEvent, 1)
	close(updates)

	err := (&RouteSubscriber{}).runLoop(context.Background(), updates, out)
	if err == nil {
		t.Fatal("runLoop on closed channel returned nil, want error")
	}
}

func TestRouteRunLoopCancelsBetweenUpdates(t *testing.T) {
	t.Parallel()
	updates := make(chan netlink.RouteUpdate)
	out := make(chan RouteEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := (&RouteSubscriber{}).runLoop(ctx, updates, out)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
