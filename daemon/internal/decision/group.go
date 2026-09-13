// Package decision owns each Group's Selection and its progression through
// Apply. Strategy remains pure in selector; callers publish only the commits
// returned here, without coordinating pending Selections or retry scope.
package decision

import (
	"context"
	"log/slog"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// ApplyRoutes writes a WAN's default routes for this Group. An empty family
// list means every probed family; an explicit list limits Gateway refreshes.
// Missing Gateways are soft skips and must return nil. Hard failures return
// an error, leaving the Decision uncommitted even if some families succeeded.
type ApplyRoutes func(context.Context, string, ...probe.Family) error

// Commit records a completed Apply. At stamps both State publication and
// Hook delivery so consumers can correlate them. A commit does not guarantee
// a new route in every family: missing Gateways leave existing routes intact.
type Commit struct {
	Old selector.Active
	New selector.Active
	At  time.Time
}

// Group owns pending and committed Selections. Its methods run synchronously
// on the daemon's event-loop goroutine; it starts no goroutines or timers.
type Group struct {
	cfg            selector.Group
	applyRoutes    ApplyRoutes
	metrics        *metrics.Registry
	logger         *slog.Logger
	active         selector.Active
	activeSince    time.Time
	decisionsTotal int
	pending        *selector.Active
}

// New constructs a Group with no Selection and performs no I/O. applyRoutes
// is the existing kernel Apply adapter, scoped to cfg's routing table.
func New(cfg selector.Group, applyRoutes ApplyRoutes, mreg *metrics.Registry, logger *slog.Logger) *Group {
	return &Group{cfg: cfg, applyRoutes: applyRoutes, metrics: mreg, logger: logger}
}

// Recompute selects from current Member Health. A changed target replaces
// any pending Decision and is counted once, even if Apply fails. An unchanged
// target does nothing; Probe and GatewayChanged drive retries.
func (g *Group) Recompute(ctx context.Context, members []selector.MemberHealth, reason string) *Commit {
	sel, err := selector.Select(g.cfg, members)
	if err != nil {
		g.logger.Error("selector", "group", g.cfg.Name, "err", err)
		return nil
	}
	if sel.Active == g.intent() {
		return nil
	}
	g.logger.Info("decision", "group", g.cfg.Name, "reason", reason,
		"old", g.active.Wan, "new", sel.Active.Wan)
	g.pending = &sel.Active
	g.decisionsTotal++
	g.metrics.GroupDecisions.WithLabelValues(g.cfg.Name, reason).Inc()
	return g.commit(ctx)
}

// Probe retries Apply only when the pending Selection targets wan. Every
// probe cycle can drive this, including partial Windows during cold start.
func (g *Group) Probe(ctx context.Context, wan string) *Commit {
	if g.pending == nil || !g.pending.Has || g.pending.Wan != wan {
		return nil
	}
	return g.commit(ctx)
}

// GatewayChanged handles a Gateway-cache mutation for wan. A pending
// Decision retries all its families; a committed Selection refreshes only
// the changed family. A refresh produces no Decision or commit, even on
// failure; the Apply adapter logs and meters route failures itself.
func (g *Group) GatewayChanged(ctx context.Context, wan string, family probe.Family) *Commit {
	want := g.intent()
	if !want.Has || want.Wan != wan {
		return nil
	}
	if g.pending != nil {
		return g.commit(ctx)
	}
	_ = g.applyRoutes(ctx, wan, family)
	return nil
}

// Snapshot returns the Group's externalized State. Active and ActiveSince
// describe the last commit; DecisionsTotal also includes pending and
// superseded targets. Pointer fields are copied so callers cannot mutate
// the Group through the snapshot.
func (g *Group) Snapshot() state.Group {
	snap := state.Group{Strategy: g.cfg.Strategy, DecisionsTotal: g.decisionsTotal}
	if g.active.Has {
		wan := g.active.Wan
		snap.Active = &wan
	}
	if !g.activeSince.IsZero() {
		since := g.activeSince
		snap.ActiveSince = &since
	}
	return snap
}

func (g *Group) intent() selector.Active {
	if g.pending != nil {
		return *g.pending
	}
	return g.active
}

func (g *Group) commit(ctx context.Context) *Commit {
	next := *g.pending
	if next.Has {
		if err := g.applyRoutes(ctx, next.Wan); err != nil {
			g.logger.Warn("decision apply incomplete; will retry",
				"group", g.cfg.Name, "wan", next.Wan, "err", err)
			return nil
		}
	}
	committed := &Commit{Old: g.active, New: next, At: time.Now().UTC()}
	g.active = next
	g.pending = nil
	if next.Has {
		g.activeSince = committed.At
	}
	for _, m := range g.cfg.Members {
		v := 0.0
		if next.Has && next.Wan == m.Wan {
			v = 1
		}
		g.metrics.GroupActive.WithLabelValues(g.cfg.Name, m.Wan).Set(v)
	}
	return committed
}
