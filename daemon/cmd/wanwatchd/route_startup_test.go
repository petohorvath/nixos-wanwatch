package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestRouteStartupDoesNotApplyObsoleteHistory(t *testing.T) {
	t.Parallel()
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		for _, delayed := range []bool{false, true} {
			name := rtnl.RouteFamily(family).String() + "/buffered"
			if delayed {
				name = rtnl.RouteFamily(family).String() + "/delivered_after_Start"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				testRouteStartupHistory(t, family, delayed)
			})
		}
	}
}

func testRouteStartupHistory(t *testing.T, family int, delayed bool) {
	t.Helper()
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	old := netlink.RouteUpdate{
		Type: unix.RTM_NEWROUTE,
		Route: netlink.Route{
			LinkIndex: lo.Index, Family: family,
			Table: unix.RT_TABLE_MAIN, Gw: net.ParseIP("fd00:1::1"),
		},
	}
	removed := old
	removed.Type = unix.RTM_DELROUTE
	current := old
	current.Gw = net.ParseIP("fd00:1::3")
	if family == unix.AF_INET {
		old.Gw = net.ParseIP("192.0.2.1")
		removed.Gw = old.Gw
		current.Gw = net.ParseIP("192.0.2.3")
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var updates chan<- netlink.RouteUpdate
	subscribe := func(ch chan<- netlink.RouteUpdate, _ <-chan struct{}, _ netlink.RouteSubscribeOptions) error {
		updates = ch
		return nil
	}
	sendHistory := func() {
		updates <- old
		updates <- removed
		updates <- current
		close(updates)
	}
	listed := false
	listRoutes := func(_ netlink.Link, af int) ([]netlink.Route, error) {
		if af != family {
			return nil, nil
		}
		if !listed {
			listed = true
			// The snapshot already includes B while A's add/delete and
			// B's add are still waiting in the subscription receiver.
			if !delayed {
				sendHistory()
			}
		}
		return []netlink.Route{current.Route}, nil
	}
	s := &rtnl.RouteSubscriber{Interfaces: map[string]struct{}{lo.Name: {}}}
	events := make(chan rtnl.RouteEvent, 8)
	exited, err := s.StartWith(ctx, subscribe, listRoutes, events)
	if err != nil {
		t.Fatal(err)
	}
	if delayed {
		// These messages were not in the Go channel during the
		// snapshot. A one-off startup drain cannot reconcile them.
		sendHistory()
	}
	select {
	case err := <-exited:
		if err == nil {
			t.Fatal("closed subscription did not report an error")
		}
	case <-ctx.Done():
		t.Fatal("subscriber did not finish processing startup history")
	}

	cfg := testCfgWithGroup()
	wan := cfg.Wans["primary"]
	wan.Interface = lo.Name
	cfg.Wans["primary"] = wan
	d := testDaemon(t, cfg)
	var writes []string
	d.writeRoute = func(_ context.Context, route apply.DefaultRoute) error {
		writes = append(writes, route.Gateway.String())
		if route.Table != 100 || !route.Gateway.Equal(current.Gw) {
			t.Errorf("Apply wrote obsolete route %+v; want table 100 via %s", route, current.Gw)
		}
		return nil
	}
	d.handleLinkEvent(t.Context(), rtnl.LinkEvent{
		Name: lo.Name, Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp,
	})
	for len(events) > 0 {
		event := <-events
		d.handleRouteEvent(t.Context(), event)
		snapshot := readPublishedState(t, d)
		gateway := snapshot.Wans["primary"].Gateways.V6
		if family == unix.AF_INET {
			gateway = snapshot.Wans["primary"].Gateways.V4
		}
		if gateway != current.Gw.String() {
			t.Errorf("State Gateway = %q after %+v; want %s", gateway, event, current.Gw)
		}
		if active := snapshot.Groups["home"].Active; active == nil || *active != "primary" {
			t.Errorf("Selection = %v; want primary", active)
		}
	}
	if len(writes) != 1 {
		t.Errorf("Apply Gateways = %v; want only the current Gateway %s", writes, current.Gw)
	}
}
