package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// waitConsumed polls a buffered channel until its length drops to
// zero, signalling that the eventLoop's select picked the event up.
// Bounded so a stuck event loop fails the test loudly instead of
// hanging until the suite's outer timeout.
//
// Direct polling of the daemon's mutated state (e.g. `d.wans[...]`)
// from the test goroutine while the eventLoop concurrently writes
// it is a Go-memory-model data race that fails under `go test
// -race`. Polling channel length instead is safe (the runtime
// reads atomically) and tight: typically a single 1ms tick.
func waitConsumed[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(ch) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("event loop never consumed the queued event (channel still has %d items)", len(ch))
		}
		time.Sleep(1 * time.Millisecond)
	}
	// The channel receive happens-before the handler's mutation,
	// but len()==0 fires the instant the receive completes — before
	// the handler may have finished its write. Give it a small
	// window so the cancel+done synchronization below sees a
	// completed mutation. 20ms is generous on any sane runner and
	// covers a hot-stop+restart on a loaded CI.
	time.Sleep(20 * time.Millisecond)
}

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
	waitConsumed(t, probeResults)
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
	waitConsumed(t, linkEvents)
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
	waitConsumed(t, routeEvents)
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
