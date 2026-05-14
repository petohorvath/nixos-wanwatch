package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"slices"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// startProbers spins up a Pinger goroutine for every (WAN, family)
// pair that has a gateway in `cfg`. Returns the shared result
// channel they all push to.
//
// Idents are allocated once up front so a hash collision between
// (WAN, family) keys is surfaced at startup rather than as a silent
// reply-misroute at runtime (PLAN §8).
func startProbers(ctx context.Context, cfg *config.Config, logger *slog.Logger) (<-chan probe.ProbeResult, error) {
	keys := identKeysFor(cfg)
	idents, err := probe.AllocateIdents(keys)
	if err != nil {
		return nil, fmt.Errorf("allocate idents: %w", err)
	}

	results := make(chan probe.ProbeResult, len(keys)*2)
	for _, k := range keys {
		wan := cfg.Wans[k.Wan]
		p := &probe.Pinger{
			Wan:        k.Wan,
			Family:     k.Family,
			Interface:  wan.Interface,
			Targets:    targetsFor(wan, k.Family),
			Ident:      idents[k],
			Interval:   time.Duration(wan.Probe.IntervalMs) * time.Millisecond,
			Timeout:    time.Duration(wan.Probe.TimeoutMs) * time.Millisecond,
			WindowSize: wan.Probe.WindowSize,
		}
		go func(p *probe.Pinger) {
			err := p.Run(ctx, results)
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("pinger exited",
					"wan", p.Wan, "family", p.Family, "err", err,
				)
			}
		}(p)
	}
	logger.Info("probers started", "count", len(keys))
	return results, nil
}

// identKeysFor enumerates (WAN, family) pairs the daemon needs a
// Pinger for — derived from probe.targets, the sole declaration of
// which families a WAN serves. Iteration is sorted by WAN name so
// the ident assignment is reproducible.
func identKeysFor(cfg *config.Config) []probe.IdentKey {
	names := slices.Sorted(maps.Keys(cfg.Wans))
	keys := make([]probe.IdentKey, 0, 2*len(names))
	for _, name := range names {
		wan := cfg.Wans[name]
		fams := familiesFromTargets(wan.Probe.Targets)
		if fams.v4 {
			keys = append(keys, probe.IdentKey{Wan: name, Family: probe.FamilyV4})
		}
		if fams.v6 {
			keys = append(keys, probe.IdentKey{Wan: name, Family: probe.FamilyV6})
		}
	}
	return keys
}

// familiesFromTargets walks a probe-targets list and reports which
// IP families appear. Mirrors `wanwatch.probe.families` on the Nix
// side — both use libnet's predicates conceptually; here we use
// net.ParseIP since the daemon receives targets as strings.
func familiesFromTargets(targets []string) struct{ v4, v6 bool } {
	var out struct{ v4, v6 bool }
	for _, t := range targets {
		ip := net.ParseIP(t)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			out.v4 = true
		} else {
			out.v6 = true
		}
	}
	return out
}

// targetsFor selects the targets to probe for `wan` in `family`
// by filtering `wan.Probe.Targets` to the literals of that family.
// Mixed-family lists are the norm (a v4+v6 WAN declares both
// upstream); the daemon spawns one pinger per (WAN, family) and
// each pinger expects only same-family targets — handing it
// cross-family literals trips `probe.Pinger.Run`'s assert.
//
// Per-family target lists (a future `probe.targetsV4 /
// targetsV6` override) would replace this filter; tracked in
// TODO.md.
func targetsFor(wan config.Wan, family probe.Family) []string {
	out := make([]string, 0, len(wan.Probe.Targets))
	for _, t := range wan.Probe.Targets {
		ip := net.ParseIP(t)
		if ip == nil {
			continue
		}
		isV4 := ip.To4() != nil
		if (family == probe.FamilyV4) == isV4 {
			out = append(out, t)
		}
	}
	return out
}

// startLinkSubscriber opens an rtnetlink subscription filtered to the
// daemon's WAN interfaces and returns the LinkEvent channel.
func startLinkSubscriber(ctx context.Context, cfg *config.Config, logger *slog.Logger) <-chan rtnl.LinkEvent {
	watched := watchedInterfaces(cfg)
	s := &rtnl.LinkSubscriber{Interfaces: watched}
	events := make(chan rtnl.LinkEvent, 64)
	go func() {
		err := s.Run(ctx, events)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("rtnl subscriber exited", "err", err)
		}
	}()
	logger.Info("rtnl subscriber started", "interfaces", len(watched))
	return events
}

// startRouteSubscriber opens an rtnetlink route subscription filtered
// to the daemon's WAN interfaces and returns the RouteEvent channel.
// The daemon uses these events to learn the current default-route
// gateway on each WAN's interface from the kernel's main RIB.
//
// Prime runs synchronously before the subscriber goroutine spawns so
// that any default routes already present in the kernel (the common
// case — systemd-networkd has typically finished by the time
// wanwatchd starts) are visible to the event loop on its very first
// iteration. Without this, a link-event arriving before the
// subscriber dumps would drive an applyRoutes call that finds an
// empty cache and skips the route write.
func startRouteSubscriber(ctx context.Context, cfg *config.Config, logger *slog.Logger) (<-chan rtnl.RouteEvent, error) {
	watched := watchedInterfaces(cfg)
	s := &rtnl.RouteSubscriber{Interfaces: watched}
	events := make(chan rtnl.RouteEvent, 64)
	if err := s.Prime(ctx, events); err != nil {
		return nil, fmt.Errorf("rtnl route subscriber prime: %w", err)
	}
	go func() {
		err := s.Run(ctx, events)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("rtnl route subscriber exited", "err", err)
		}
	}()
	logger.Info("rtnl route subscriber started", "interfaces", len(watched), "primed", len(events))
	return events, nil
}

// watchedInterfaces is the set of interface names the daemon
// subscribes to — both link and route channels filter through this.
func watchedInterfaces(cfg *config.Config) map[string]struct{} {
	out := make(map[string]struct{}, len(cfg.Wans))
	for _, wan := range cfg.Wans {
		out[wan.Interface] = struct{}{}
	}
	return out
}

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
