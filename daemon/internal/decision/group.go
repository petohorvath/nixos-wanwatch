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
// list means every probed family; an explicit list limits route refreshes.
// Missing Gateways are soft skips and must return nil. Hard failures return
// an error, leaving the Decision uncommitted even if some families succeeded.
type ApplyRoutes func(context.Context, string, ...probe.Family) error

// Commit records a completed Apply. At stamps both State publication and
// Hook delivery so consumers can correlate them. A commit does not guarantee
// a new route in every family: missing Gateways leave existing routes intact.
// A commit may leave the Selection unchanged, when a pending target is
// superseded by the committed one or by none; Event classifies it.
type Commit struct {
	Old selector.Active
	New selector.Active
	At  time.Time
}

// Event classifies the commit by its Selection change: up from none to a
// WAN, down from a WAN to none, switch between two WANs, and "" when the
// Selection is unchanged. "" is deliberately not a named state.Event:
// the Events mirror the WANWATCH_EVENT values in docs/specs/daemon-state.md,
// and an unchanged Selection runs no Hook.
func (c Commit) Event() state.Event {
	switch {
	case !c.Old.Has && c.New.Has:
		return state.EventUp
	case c.Old.Has && !c.New.Has:
		return state.EventDown
	case c.Old.Has && c.New.Has && c.Old.Wan != c.New.Wan:
		return state.EventSwitch
	}
	return ""
}

// Group owns pending and committed Selections and keeps the committed
// Selection's Group-table routes converged
// (docs/adr/0017-repair-committed-group-table.md). Its methods run
// synchronously on the daemon's event-loop goroutine; it starts no
// goroutines or timers.
type Group struct {
	cfg            selector.Group
	applyRoutes    ApplyRoutes
	metrics        *metrics.Registry
	logger         *slog.Logger
	active         selector.Active
	activeSince    time.Time
	decisionsTotal int
	pending        *selector.Active
	// repairDue records a failed refresh of the committed Selection's
	// routes. The next refresh rewrites every family; a commit clears it.
	repairDue bool
}

// New constructs a Group with no Selection and performs no I/O. applyRoutes
// is the existing kernel Apply adapter, scoped to cfg's routing table.
func New(cfg selector.Group, applyRoutes ApplyRoutes, mreg *metrics.Registry, logger *slog.Logger) *Group {
	return &Group{cfg: cfg, applyRoutes: applyRoutes, metrics: mreg, logger: logger}
}

// Recompute selects from current Member Health. A changed target replaces
// any pending Decision and is counted once, even if Apply fails. An unchanged
// target does nothing; Probe and RouteObserved drive retries.
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

// Probe retries Apply when the pending Selection targets wan, and repairs
// the committed Selection's routes when wan is selected and a Repair is
// due. Every probe cycle can drive this, including partial Windows during
// cold start. A pending Decision holds a due Repair until it commits.
func (g *Group) Probe(ctx context.Context, wan string) *Commit {
	if g.pending != nil {
		if !g.pending.Has || g.pending.Wan != wan {
			return nil
		}
		return g.commit(ctx)
	}
	if g.repairDue && g.active.Wan == wan {
		g.refresh(ctx)
	}
	return nil
}

// RouteObserved handles a main-table default-route observation for wan,
// whether or not it changed the Gateway: the kernel can flush a Group-table
// route without a notification, so any observation may mean that route is
// gone. A pending Decision retries all its families; a committed Selection
// refreshes the observed family, or every family when a Repair is due. A
// refresh produces no Decision or commit, even on failure; a failed one
// leaves a Repair due, and the Apply adapter meters its route failures.
func (g *Group) RouteObserved(ctx context.Context, wan string, family probe.Family) *Commit {
	want := g.intent()
	if !want.Has || want.Wan != wan {
		return nil
	}
	if g.pending != nil {
		return g.commit(ctx)
	}
	g.refresh(ctx, family)
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

// refresh rewrites the committed Selection's routes for families, or for
// every family when families is empty or a Repair is due. A failure leaves
// a Repair due.
func (g *Group) refresh(ctx context.Context, families ...probe.Family) {
	if g.repairDue {
		families = nil
	}
	if err := g.applyRoutes(ctx, g.active.Wan, families...); err != nil {
		g.logger.Warn("route refresh incomplete; will retry",
			"group", g.cfg.Name, "wan", g.active.Wan, "err", err)
		g.repairDue = true
		return
	}
	if g.repairDue {
		g.logger.Info("routes repaired", "group", g.cfg.Name, "wan", g.active.Wan)
		g.repairDue = false
	}
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
	// A commit wrote every family of its WAN, or left a down's routes in
	// place on purpose, so no earlier Repair remains due.
	g.repairDue = false
	// Every Selection change restamps, a down included. A commit that
	// keeps its WAN keeps its Selection's start.
	if committed.Event() != "" {
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
