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
//
// Once ctx is cancelled it dispatches nothing more: select picks
// at random among ready cases, so an event still queued at
// cancellation is dropped rather than handled with the cancelled
// ctx, which would fail its route writes and publish State during
// shutdown.
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
			if ctx.Err() != nil {
				return
			}
			d.handleProbeResult(ctx, r)
		case e := <-linkEvents:
			if ctx.Err() != nil {
				return
			}
			d.handleLinkEvent(ctx, e)
		case e := <-routeEvents:
			if ctx.Err() != nil {
				return
			}
			d.handleRouteEvent(ctx, e)
		}
	}
}

// startupSnapshot holds the link and route snapshots the subscribers
// read before the event loop starts; their channels carry only later
// changes.
type startupSnapshot struct {
	links  []rtnl.LinkEvent
	routes []rtnl.RouteEvent
}

// serve runs the event loop until ctx is cancelled, then closes Hook
// delivery. run() calls it once the daemon is primed and READY is
// sent; the test harness drives the same sequence.
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
