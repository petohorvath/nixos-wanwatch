package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

func TestEventLoopRoutesProbeResultToDaemon(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)
	probeResults <- probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats:  probe.FamilyStats{LossRatio: 0.5, RTTMicros: 12000},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := d.wans["primary"].families[probe.FamilyV4].stats.LossRatio; got != 0.5 {
		t.Errorf("primary v4 LossRatio = %v, want 0.5", got)
	}
}

func TestEventLoopRoutesLinkEventToDaemon(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)
	linkEvents <- rtnl.LinkEvent{
		Name:      "eth0",
		Carrier:   rtnl.CarrierUp,
		Operstate: rtnl.OperstateUp,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if d.wans["primary"].carrier != rtnl.CarrierUp {
		t.Errorf("primary carrier = %v, want up", d.wans["primary"].carrier)
	}
}

func TestEventLoopRoutesRouteEventToDaemon(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)
	routeEvents <- rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "eth0",
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("192.0.2.1"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	gw, ok := d.gateways.get("eth0", rtnl.RouteFamilyV4)
	if !ok {
		t.Fatal("eth0/v4 gateway not in cache after RouteEvent")
	}
	if gw.String() != "192.0.2.1" {
		t.Errorf("cache eth0/v4 = %v, want 192.0.2.1", gw)
	}
}

func TestEventLoopReturnsOnCtxCancel(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	ctx, cancel := context.WithCancel(context.Background())
	probeResults := make(chan probe.ProbeResult)
	linkEvents := make(chan rtnl.LinkEvent)
	routeEvents := make(chan rtnl.RouteEvent)

	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not return within 1s of ctx cancel")
	}
}
