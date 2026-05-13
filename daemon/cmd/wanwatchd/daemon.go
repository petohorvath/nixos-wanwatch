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

// targetsFor selects the targets to probe for `wan` in `family`.
// v1: the same targets list is used for both families (config-render
// time validation guarantees their literals match the family). A
// per-family target list lands in v0.2 if operators need it.
func targetsFor(wan config.Wan, _ probe.Family) []string {
	return wan.Probe.Targets
}

// startSubscriber opens an rtnetlink subscription filtered to the
// daemon's WAN interfaces and returns the LinkEvent channel.
func startSubscriber(ctx context.Context, cfg *config.Config, logger *slog.Logger) <-chan rtnl.LinkEvent {
	watched := make(map[string]struct{}, len(cfg.Wans))
	for _, wan := range cfg.Wans {
		watched[wan.Interface] = struct{}{}
	}
	s := &rtnl.Subscriber{Interfaces: watched}
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

// eventLoop is the daemon's central dispatch. Routes each
// ProbeResult / LinkEvent through `d`'s Decision pipeline.
func eventLoop(
	ctx context.Context,
	d *daemon,
	probeResults <-chan probe.ProbeResult,
	linkEvents <-chan rtnl.LinkEvent,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-probeResults:
			d.handleProbeResult(r)
		case e := <-linkEvents:
			d.handleLinkEvent(e)
		}
	}
}
