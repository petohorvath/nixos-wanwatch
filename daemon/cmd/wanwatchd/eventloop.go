package main

import (
	"context"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// eventLoop is the daemon's central dispatch. Routes each
// ProbeResult / LinkEvent / RouteEvent through `d`'s Decision
// pipeline and acknowledges watchdog challenges only while this
// goroutine is able to service its select loop.
func eventLoop(
	ctx context.Context,
	d *daemon,
	probeResults <-chan probe.ProbeResult,
	linkEvents <-chan rtnl.LinkEvent,
	routeEvents <-chan rtnl.RouteEvent,
	watchdogChallenges <-chan watchdogChallenge,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case challenge := <-watchdogChallenges:
			close(challenge.ack)
		case r := <-probeResults:
			d.handleProbeResult(ctx, r)
		case e := <-linkEvents:
			d.handleLinkEvent(ctx, e)
		case e := <-routeEvents:
			d.handleRouteEvent(ctx, e)
		}
	}
}

// serve runs the event loop until ctx is cancelled, then closes Hook
// delivery. run() calls it once every subsystem is wired; the test
// harness drives the same sequence.
func serve(
	ctx context.Context,
	d *daemon,
	probeResults <-chan probe.ProbeResult,
	linkEvents <-chan rtnl.LinkEvent,
	routeEvents <-chan rtnl.RouteEvent,
	watchdogChallenges <-chan watchdogChallenge,
) {
	eventLoop(ctx, d, probeResults, linkEvents, routeEvents, watchdogChallenges)
	d.hooks.Close()
}
