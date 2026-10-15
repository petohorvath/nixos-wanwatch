package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/decision"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/health"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// hookSink receives the Decision data the daemon captures for Hooks.
// *state.HookNotifier is the production adapter; tests record
// notifications synchronously.
type hookSink interface {
	// Notify submits one notification and reports whether it was accepted.
	Notify(hookCtx state.HookContext) bool
	// Close stops accepting notifications and finishes delivery.
	Close()
}

// daemon bundles the runtime state and subsystem handles. Wired
// once in run(), then driven by eventLoop's dispatch.
type daemon struct {
	cfg      *config.Config
	kernel   kernel
	hooks    hookSink
	metrics  *metrics.Registry
	stateW   *state.Writer
	logger   *slog.Logger
	health   *health.Wans
	groups   map[string]*decision.Group
	gateways *gatewayCache
}

// newDaemon constructs the runtime state from `cfg` — cold WAN
// Health and one Group Decision module per Group — over the given
// kernel and Hook sink. It performs no I/O and starts no goroutines.
func newDaemon(cfg *config.Config, k kernel, hooks hookSink, mreg *metrics.Registry, logger *slog.Logger) *daemon {
	d := &daemon{
		cfg:      cfg,
		kernel:   k,
		hooks:    hooks,
		metrics:  mreg,
		stateW:   &state.Writer{Path: cfg.Global.StatePath},
		logger:   logger,
		health:   health.New(cfg.Wans, mreg),
		groups:   make(map[string]*decision.Group, len(cfg.Groups)),
		gateways: newGatewayCache(),
	}
	for name, g := range cfg.Groups {
		d.groups[name] = decision.New(g, func(ctx context.Context, wan string, families ...probe.Family) error {
			return d.applyRoutes(ctx, g, wan, families...)
		}, mreg, logger)
	}
	return d
}

// bootstrap installs the fwmark policy-routing rules for every
// group + family combo. Runs once at startup before any Decision —
// the rules survive across daemon restarts (EnsureRule swallows
// EEXIST), so re-running is a no-op.
func (d *daemon) bootstrap(ctx context.Context) error {
	for _, g := range d.cfg.Groups {
		for _, fam := range probe.AllFamilies {
			if err := d.kernel.EnsureRule(ctx, apply.FwmarkRule{
				Family: fam,
				Mark:   g.Mark,
				Table:  g.Table,
			}); err != nil {
				d.metrics.ApplyOpErrors.WithLabelValues(g.Name, "rule_install").Inc()
				return err
			}
		}
	}
	d.logger.Info("fwmark rules installed", "groups", len(d.cfg.Groups))

	// Publish an initial state.json so consumers (state-readers,
	// wanwatch_state_publications_total, integration checks) see
	// the daemon's view from the very first scrape — even before
	// any probe sample lands. docs/specs/failover.md cold-start.
	d.writeStateSnapshot(time.Time{})
	return nil
}

// handleProbeResult forwards a per-cycle result to WAN Health, then
// retries r.Wan's pending Decisions, including during cold start: a
// probe result means the WAN is reachable. Health goes first so a
// retry never moves a Selection onto a WAN this result made unhealthy.
// When r.Wan flipped, the recompute has already settled its Decisions:
// it moved away from an unhealthy WAN or attempted Apply onto a healthy
// one.
func (d *daemon) handleProbeResult(ctx context.Context, r probe.ProbeResult) {
	change := d.health.Probe(r)
	d.publishHealthChange(ctx, change, reasonHealth)
	if !slices.Contains(change.Flipped, r.Wan) {
		d.retryGroupDecisions(ctx, r.Wan)
	}
}

// handleLinkEvent forwards carrier/operstate to WAN Health, which
// folds the event into every WAN on the interface before reporting.
// Carrier-down fast-tracks those WANs to unhealthy without waiting
// for the probe to time out.
func (d *daemon) handleLinkEvent(ctx context.Context, e rtnl.LinkEvent) {
	d.publishHealthChange(ctx, d.health.Link(e), reasonCarrier)
}

// publishHealthChange recomputes every Group containing a WAN whose
// Health flipped, then republishes state.json for any transition
// docs/specs/daemon-state.md lists — including one that moves no
// Decision, such as v4 dropping while v6 holds under
// familyHealthPolicy=any.
func (d *daemon) publishHealthChange(ctx context.Context, c health.Change, reason decisionReason) {
	for _, wan := range c.Flipped {
		d.recomputeAffectedGroups(ctx, wan, reason)
	}
	if c.Republish {
		d.writeStateSnapshot(time.Time{})
	}
}

// recomputeAffectedGroups forwards current Member Health to every Group
// containing wan. The Group owns Selection, retries and commit readiness.
func (d *daemon) recomputeAffectedGroups(ctx context.Context, wan string, reason decisionReason) {
	for name, g := range d.cfg.Groups {
		if !groupContainsWAN(g, wan) {
			continue
		}
		committed := d.groups[name].Recompute(ctx, d.health.MemberHealth(g), string(reason))
		d.publishDecision(ctx, g, committed)
	}
}

// publishDecision externalizes a commit returned by the Group Decision
// module. Failed Apply and Gateway refreshes return no commit. The module
// has already updated its snapshot and gauges before publication begins.
func (d *daemon) publishDecision(ctx context.Context, g selector.Group, committed *decision.Commit) {
	if committed == nil {
		return
	}
	d.flushSwitchedConntrack(ctx, g, committed.Old, committed.New)
	d.writeStateSnapshot(committed.At)
	d.notifyHooks(g, committed.Old, committed.New, committed.At)
}

// flushSwitchedConntrack clears the conntrack entries pinned to the
// vacated WAN's source addresses, so flows that were SNATted out the
// old WAN re-establish via the new one instead of being black-holed
// until their conntrack entries time out.
//
// Switch-only: it runs when both old and next are present. On a
// `down` there is no healthy successor and the old default route is
// left in place, so a flush would only churn. Best-effort per
// docs/nftzones-integration.md — a resolve or flush failure is logged and metered but
// never fails the Decision; the routes have already converged.
func (d *daemon) flushSwitchedConntrack(ctx context.Context, g selector.Group, old, next selector.Active) {
	if !old.Has || !next.Has {
		return
	}
	wan, ok := d.cfg.Wans[old.Wan]
	if !ok {
		return
	}
	addrs, err := d.kernel.InterfaceAddrs(wan.Interface)
	if err != nil {
		d.logger.Warn("conntrack flush: resolve vacated WAN addresses",
			"group", g.Name, "wan", old.Wan, "iface", wan.Interface, "err", err)
		d.metrics.ApplyOpErrors.WithLabelValues(g.Name, "conntrack_flush").Inc()
		return
	}
	for _, ip := range addrs {
		family := probe.FamilyV4
		if ip.To4() == nil {
			family = probe.FamilyV6
		}
		n, err := d.kernel.FlushConntrack(ctx, family, ip)
		if err != nil {
			d.logger.Warn("conntrack flush",
				"group", g.Name, "wan", old.Wan, "family", family, "ip", ip, "err", err)
			d.metrics.ApplyOpErrors.WithLabelValues(g.Name, "conntrack_flush").Inc()
			continue
		}
		d.logger.Info("conntrack flushed",
			"group", g.Name, "wan", old.Wan, "family", family, "ip", ip, "entries", n)
	}
}

// retryGroupDecisions forwards each Probe cycle. Groups decide whether
// this WAN can advance a pending Decision; callers never inspect intent.
func (d *daemon) retryGroupDecisions(ctx context.Context, wan string) {
	for name, g := range d.groups {
		d.publishDecision(ctx, d.cfg.Groups[name], g.Probe(ctx, wan))
	}
}

// applyRoutes writes the default route per family of the active
// WAN. With no `families` argument it writes every family the WAN
// probes. The Decision module limits Gateway refreshes to the changed
// family. Families the WAN doesn't probe are skipped either way.
//
// PointToPoint WANs get scope-link routes (no gateway needed);
// non-PtP WANs use the gateway the gatewayCache learned from the
// kernel's main routing table.
//
// It returns an error if any family *hard*-fails — the ifindex
// lookup, or a netlink write — so the Decision module can hold the
// Decision pending and retry. A family with no gateway cached yet
// is *not* a failure: that write is intentionally deferred (docs/adr/0004-runtime-gateway-discovery.md),
// and handleRouteEvent reapplies it once the gateway is
// discovered.
func (d *daemon) applyRoutes(ctx context.Context, g selector.Group, activeWan string, families ...probe.Family) error {
	wan, ok := d.cfg.Wans[activeWan]
	if !ok {
		return fmt.Errorf("apply routes: unknown wan %q", activeWan)
	}
	ifindex, err := d.kernel.IfIndex(wan.Interface)
	if err != nil {
		d.logger.Error("ifindex lookup", "iface", wan.Interface, "err", err)
		d.metrics.ApplyOpErrors.WithLabelValues(g.Name, "ifindex_lookup").Inc()
		return fmt.Errorf("apply routes: ifindex %q: %w", wan.Interface, err)
	}

	// writeOne does the per-family route build + write + error
	// handling — extracted as a closure so the full-pass and the
	// single-family RouteEvent reapply share one body. Returns true
	// on a hard write failure, false on a soft skip (no cached
	// gateway) or success.
	writeOne := func(fam probe.Family) bool {
		famLabel := fam.String()
		route := apply.DefaultRoute{
			Family:  fam,
			Table:   g.Table,
			IfIndex: ifindex,
		}
		switch {
		case wan.PointToPoint:
			route.PointToPoint = true
		default:
			gw, ok := d.gateways.get(wan.Interface, rtnl.RouteFamily(fam))
			if !ok || gw == nil {
				d.logger.Info("no gateway in cache; skipping route write (will reapply on discovery)",
					"group", g.Name, "wan", activeWan, "family", famLabel,
					"iface", wan.Interface)
				return false
			}
			route.Gateway = gw
		}
		started := time.Now()
		err := d.kernel.WriteRoute(ctx, route)
		d.metrics.ApplyRouteDuration.WithLabelValues(g.Name, famLabel).Observe(time.Since(started).Seconds())
		if err != nil {
			d.logger.Error("route write", "group", g.Name, "family", famLabel, "err", err)
			d.metrics.ApplyRouteErrors.WithLabelValues(g.Name, famLabel).Inc()
			return true
		}
		return false
	}

	probed := wan.Probe.Targets.Families()
	if len(families) == 0 {
		families = probed
	}
	var failed int
	for _, fam := range families {
		// An explicit Family set (the RouteEvent reapply path) may
		// name a Family the WAN doesn't probe; the daemon has no route
		// to maintain for it.
		if !slices.Contains(probed, fam) {
			continue
		}
		if writeOne(fam) {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("apply routes: %d route write(s) failed for wan %q", failed, activeWan)
	}
	return nil
}

// handleRouteEvent absorbs an rtnetlink default-route observation
// into the gateway cache and forwards changes to the Group Decision
// module for each WAN on the affected interface. The module owns
// whether to retry a Decision or refresh a committed Selection.
func (d *daemon) handleRouteEvent(ctx context.Context, e rtnl.RouteEvent) {
	prev, hadPrev := d.gateways.get(e.Iface, e.Family)
	switch e.Op {
	case rtnl.RouteEventAdd:
		d.gateways.set(e.Iface, e.Family, e.Gateway)
	case rtnl.RouteEventDel:
		d.gateways.clear(e.Iface, e.Family)
	}

	changed := !hadPrev || e.Op == rtnl.RouteEventDel || !prev.Equal(e.Gateway)
	if !changed {
		return
	}

	for wanName, wan := range d.cfg.Wans {
		if wan.Interface != e.Iface {
			continue
		}
		for name, g := range d.groups {
			committed := g.GatewayChanged(ctx, wanName, probe.Family(e.Family))
			d.publishDecision(ctx, d.cfg.Groups[name], committed)
		}
	}
	// Republish state.json on any gateway-cache mutation. A change
	// that completes a pending Decision was captured by
	// publishDecision above; everything else (a new gateway on the
	// already-active WAN, a gateway disappearing on the standby)
	// wouldn't otherwise update state.json, leaving the
	// wans[<name>].gateways[v4|v6] fields stale.
	d.writeStateSnapshot(time.Time{})
}

// writeStateSnapshot serializes the runtime state into the form
// state.Writer expects, then writes atomically. Increments
// `state_publications_total` on success.
//
// `now` controls state.json's `updatedAt`: the zero value defers
// to state.Writer (which falls back to `time.Now().UTC()` at write
// time), while a non-zero value pins the stamp so it can match a
// concurrent hook invocation (publishDecision).
func (d *daemon) writeStateSnapshot(now time.Time) {
	snap := state.State{
		UpdatedAt: now,
		Wans:      make(map[string]state.Wan, len(d.cfg.Wans)),
		Groups:    make(map[string]state.Group, len(d.groups)),
	}
	for name, wan := range d.health.Snapshot() {
		wan.Gateways = state.Gateways{
			V4: d.gateways.address(wan.Interface, rtnl.RouteFamilyV4),
			V6: d.gateways.address(wan.Interface, rtnl.RouteFamilyV6),
		}
		snap.Wans[name] = wan
	}
	for name, g := range d.groups {
		snap.Groups[name] = g.Snapshot()
	}
	if err := d.stateW.Write(snap); err != nil {
		d.logger.Error("state write", "err", err)
		return
	}
	d.metrics.StatePublications.Inc()
}

// notifyHooks captures the Decision data on the event-loop goroutine before
// submitting it to the notifier. The worker never reads daemon-owned state.
func (d *daemon) notifyHooks(g selector.Group, old, next selector.Active, now time.Time) {
	event := hookEventFor(old, next)
	if event == "" {
		return
	}

	oldIface := ifaceFor(d.cfg.Wans, old)
	nextIface := ifaceFor(d.cfg.Wans, next)
	hookCtx := state.HookContext{
		Event:    event,
		Group:    g.Name,
		WanOld:   old.Wan,
		WanNew:   next.Wan,
		IfaceOld: oldIface,
		IfaceNew: nextIface,
		// Gateway env vars come from the discovery cache. They're
		// blank when (a) the iface has no cached default route yet,
		// or (b) the route is scope-link (point-to-point) so there
		// is no gateway to surface.
		GatewayV4Old: d.gateways.string(oldIface, rtnl.RouteFamilyV4),
		GatewayV4New: d.gateways.string(nextIface, rtnl.RouteFamilyV4),
		GatewayV6Old: d.gateways.string(oldIface, rtnl.RouteFamilyV6),
		GatewayV6New: d.gateways.string(nextIface, rtnl.RouteFamilyV6),
		Families:     probedFamiliesFor(d.cfg.Wans, next),
		Table:        g.Table,
		Mark:         g.Mark,
		Timestamp:    now,
	}
	d.hooks.Notify(hookCtx)
}
