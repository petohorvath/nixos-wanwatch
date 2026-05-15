package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// startProbers spins up a Pinger goroutine for every (WAN, family)
// pair that has a gateway in `cfg`. Returns the shared result
// channel they all push to.
//
// Idents are allocated once up front so a hash collision between
// (WAN, family) keys is surfaced at startup rather than as a silent
// reply-misroute at runtime (PLAN §8).
//
// A pinger that exits with anything other than context cancellation
// calls `cancel` with the cause, taking the whole daemon down so
// systemd restarts it — a probe layer that silently stopped would
// leave failover blind.
func startProbers(ctx context.Context, cancel context.CancelCauseFunc, cfg *config.Config, logger *slog.Logger) (<-chan probe.ProbeResult, error) {
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
			Logger:     logger,
		}
		go func(p *probe.Pinger) {
			err := p.Run(ctx, results)
			onSubsystemExit(cancel, logger, fmt.Sprintf("prober %s/%s", p.Wan, p.Family), err)
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

// familiesFromTargets reports which IP families the WAN serves
// (which Pinger goroutines to spawn for it). Mirrors
// `wanwatch.probe.families` on the Nix side — both reduce to "is
// the per-family bucket non-empty?" now that the config layer keeps
// the buckets disjoint.
func familiesFromTargets(t config.Targets) struct{ v4, v6 bool } {
	return struct{ v4, v6 bool }{
		v4: len(t.V4) > 0,
		v6: len(t.V6) > 0,
	}
}

// targetsFor returns the probe targets for `wan` in `family` —
// straight from the per-family bucket. The lib + config.Validate
// enforce that each bucket only holds same-family literals, so no
// runtime filtering is needed here.
func targetsFor(wan config.Wan, family probe.Family) []string {
	if family == probe.FamilyV4 {
		return wan.Probe.Targets.V4
	}
	return wan.Probe.Targets.V6
}
