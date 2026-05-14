package main

import (
	"context"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// eventLoop is the daemon's central dispatch. Routes each
// ProbeResult / LinkEvent / RouteEvent through `d`'s Decision
// pipeline.
func eventLoop(
	ctx context.Context,
	d *daemon,
	probeResults <-chan probe.ProbeResult,
	linkEvents <-chan rtnl.LinkEvent,
	routeEvents <-chan rtnl.RouteEvent,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-probeResults:
			d.handleProbeResult(ctx, r)
		case e := <-linkEvents:
			d.handleLinkEvent(ctx, e)
		case e := <-routeEvents:
			d.handleRouteEvent(ctx, e)
		}
	}
}
