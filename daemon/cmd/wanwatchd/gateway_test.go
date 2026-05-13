package main

import (
	"net"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

func TestGatewayCacheGetEmpty(t *testing.T) {
	t.Parallel()
	c := NewGatewayCache()
	gw, ok := c.Get("eth0", rtnl.RouteFamilyV4)
	if ok {
		t.Errorf("Get on empty cache: ok=%v gw=%v, want ok=false", ok, gw)
	}
}

func TestGatewayCacheSetGet(t *testing.T) {
	t.Parallel()
	c := NewGatewayCache()
	want := net.ParseIP("192.0.2.1")
	c.Set("eth0", rtnl.RouteFamilyV4, want)

	got, ok := c.Get("eth0", rtnl.RouteFamilyV4)
	if !ok {
		t.Fatal("Get after Set: ok=false")
	}
	if !got.Equal(want) {
		t.Errorf("Get = %v, want %v", got, want)
	}
}

func TestGatewayCacheSetRecordsScopeLink(t *testing.T) {
	t.Parallel()
	// Scope-link routes carry a nil gateway — the cache must
	// distinguish "no entry" from "entry with nil gw".
	c := NewGatewayCache()
	c.Set("wg0", rtnl.RouteFamilyV4, nil)

	gw, ok := c.Get("wg0", rtnl.RouteFamilyV4)
	if !ok {
		t.Fatal("Get on scope-link entry: ok=false; want true with nil gw")
	}
	if gw != nil {
		t.Errorf("scope-link gw = %v, want nil", gw)
	}
}

func TestGatewayCacheKeysByFamily(t *testing.T) {
	t.Parallel()
	c := NewGatewayCache()
	c.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	c.Set("eth0", rtnl.RouteFamilyV6, net.ParseIP("2001:db8::1"))

	v4, _ := c.Get("eth0", rtnl.RouteFamilyV4)
	v6, _ := c.Get("eth0", rtnl.RouteFamilyV6)
	if v4.String() != "192.0.2.1" {
		t.Errorf("eth0/v4 = %v, want 192.0.2.1", v4)
	}
	if v6.String() != "2001:db8::1" {
		t.Errorf("eth0/v6 = %v, want 2001:db8::1", v6)
	}
}

func TestGatewayCacheClear(t *testing.T) {
	t.Parallel()
	c := NewGatewayCache()
	c.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	c.Clear("eth0", rtnl.RouteFamilyV4)

	if _, ok := c.Get("eth0", rtnl.RouteFamilyV4); ok {
		t.Error("entry still present after Clear")
	}
}

func TestSnapshotStringFormats(t *testing.T) {
	t.Parallel()
	c := NewGatewayCache()
	c.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	c.Set("wg0", rtnl.RouteFamilyV4, nil) // scope-link
	s := c.Snapshot()

	if got := s.String("eth0", rtnl.RouteFamilyV4); got != "192.0.2.1" {
		t.Errorf("String(eth0/v4) = %q, want 192.0.2.1", got)
	}
	if got := s.String("wg0", rtnl.RouteFamilyV4); got != "" {
		t.Errorf("String(wg0/v4 scope-link) = %q, want empty", got)
	}
	if got := s.String("missing", rtnl.RouteFamilyV4); got != "" {
		t.Errorf("String(missing) = %q, want empty", got)
	}
}

func TestSnapshotIsolatedFromCacheMutation(t *testing.T) {
	t.Parallel()
	c := NewGatewayCache()
	c.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	snap := c.Snapshot()

	c.Clear("eth0", rtnl.RouteFamilyV4)
	c.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("198.51.100.1"))

	if got := snap.String("eth0", rtnl.RouteFamilyV4); got != "192.0.2.1" {
		t.Errorf("snap reflects post-snapshot mutation: got %q", got)
	}
}

func TestProbeFamilyToRoute(t *testing.T) {
	t.Parallel()
	if probeFamilyToRoute(probe.FamilyV4) != rtnl.RouteFamilyV4 {
		t.Error("probe.FamilyV4 did not map to rtnl.RouteFamilyV4")
	}
	if probeFamilyToRoute(probe.FamilyV6) != rtnl.RouteFamilyV6 {
		t.Error("probe.FamilyV6 did not map to rtnl.RouteFamilyV6")
	}
}

func TestHandleRouteEventUpdatesCacheOnAdd(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())
	gw := net.ParseIP("192.0.2.1")

	d.handleRouteEvent(rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "eth0",
		Family:  rtnl.RouteFamilyV4,
		Gateway: gw,
	})

	got, ok := d.gateways.Get("eth0", rtnl.RouteFamilyV4)
	if !ok {
		t.Fatal("cache not populated")
	}
	if !got.Equal(gw) {
		t.Errorf("cache eth0/v4 = %v, want %v", got, gw)
	}
}

func TestHandleRouteEventClearsCacheOnDel(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())
	d.gateways.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))

	d.handleRouteEvent(rtnl.RouteEvent{
		Op:     rtnl.RouteEventDel,
		Iface:  "eth0",
		Family: rtnl.RouteFamilyV4,
	})

	if _, ok := d.gateways.Get("eth0", rtnl.RouteFamilyV4); ok {
		t.Error("cache entry survived RouteEventDel")
	}
}

func TestHandleRouteEventIgnoresUnrelatedInterface(t *testing.T) {
	t.Parallel()
	// A RouteEvent on an interface that is not the active WAN of
	// any group still populates the cache (the cache is keyed by
	// iface name, independent of who's active) — but no reapply
	// should fire. We can't easily assert "no reapply"; we just
	// verify the cache update happens for an iface no WAN uses.
	d := testDaemon(t, testCfg())
	d.handleRouteEvent(rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "lo",
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("127.0.0.1"),
	})
	if _, ok := d.gateways.Get("lo", rtnl.RouteFamilyV4); !ok {
		t.Error("lo gateway not recorded")
	}
}
