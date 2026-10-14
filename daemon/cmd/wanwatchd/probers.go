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
// reply-misroute at runtime (docs/specs/probe-algorithm.md).
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
			Targets:    wan.Probe.Targets.For(k.Family),
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
		for _, fam := range cfg.Wans[name].Probe.Targets.Families() {
			keys = append(keys, probe.IdentKey{Wan: name, Family: fam})
		}
	}
	return keys
}
