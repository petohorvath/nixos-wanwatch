package main

import (
	"context"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

func TestEventLoopReturnsOnCtxCancel(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	ctx, cancel := context.WithCancel(context.Background())
	probeResults := make(chan probe.ProbeResult)
	linkEvents := make(chan rtnl.LinkEvent)
	routeEvents := make(chan rtnl.RouteEvent)

	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents, nil)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not return within 1s of ctx cancel")
	}
}

func TestEventLoopAcknowledgesWatchdogChallenge(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probeResults := make(chan probe.ProbeResult)
	linkEvents := make(chan rtnl.LinkEvent)
	routeEvents := make(chan rtnl.RouteEvent)
	challenges := make(chan watchdogChallenge)
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents, challenges)
		close(done)
	}()

	ack := make(chan struct{})
	select {
	case challenges <- watchdogChallenge{ack: ack}:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not receive watchdog challenge")
	}
	select {
	case <-ack:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not acknowledge watchdog challenge")
	}

	cancel()
	<-done
}
