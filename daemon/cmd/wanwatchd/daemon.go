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
	// startedAt is State's startedAt, fixed when the daemon is built.
	startedAt time.Time
}

// newDaemon constructs the runtime state from `cfg` — cold WAN
// Health and one Group Decision module per Group — over the given
// kernel and Hook sink. It performs no I/O and starts no goroutines.
func newDaemon(cfg *config.Config, k kernel, hooks hookSink, mreg *metrics.Registry, logger *slog.Logger) *daemon {
	d := &daemon{
		cfg:       cfg,
		kernel:    k,
		hooks:     hooks,
		metrics:   mreg,
		stateW:    &state.Writer{Path: cfg.Global.StatePath},
		logger:    logger,
		health:    health.New(cfg.Wans, mreg),
		groups:    make(map[string]*decision.Group, len(cfg.Groups)),
		gateways:  newGatewayCache(),
		startedAt: time.Now().UTC(),
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

// prime folds the startup snapshots in before the event loop handles
// any event. It learns every Gateway first, so the first Decision
// writes its routes and its Hook carries them. It then folds every
// LinkEvent into WAN Health before recomputing any Group, so a Group
// makes at most one startup Decision, onto its preferred healthy
// Member, whatever order the link snapshot lists the WANs in. A snapshot that changes the Gateway cache or Health
// republishes State once at the end, so it shows every Gateway even
// when no Decision fires. A cancelled ctx primes nothing.
func (d *daemon) prime(ctx context.Context, snapshot startupSnapshot) {
	if ctx.Err() != nil {
		return
	}
	// The route snapshot lists only installed routes, so every event
	// is an add.
	for _, e := range snapshot.routes {
		d.gateways.set(e.Iface, e.Family, e.Gateway)
	}
	change := health.Change{Republish: len(snapshot.routes) > 0}
	for _, e := range snapshot.links {
		c := d.health.Link(e)
		change.Flipped = append(change.Flipped, c.Flipped...)
		change.Republish = change.Republish || c.Republish
	}
	d.publishHealthChange(ctx, change, reasonCarrier)
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
// module. Failed Apply and route refreshes return no commit. The module
// has already updated its snapshot and gauges before publication begins.
// Every commit publishes State, because its Decision was counted when it
// became pending; only a commit that changes the Selection runs Hooks,
// and only a switch flushes conntrack.
func (d *daemon) publishDecision(ctx context.Context, g selector.Group, committed *decision.Commit) {
	if committed == nil {
		return
	}
	event := committed.Event()
	if event == state.EventSwitch {
		d.flushSwitchedConntrack(ctx, g, committed.Old)
	}
	d.writeStateSnapshot(committed.At)
	if event != "" {
		d.notifyHooks(g, event, committed)
	}
}

// flushSwitchedConntrack clears the conntrack entries pinned to the
// vacated WAN's source addresses, so flows that were SNATted out the
// old WAN re-establish via the new one instead of being black-holed
// until their conntrack entries time out.
//
// Switch-only: publishDecision calls it only for a switch. On a
// `down` there is no healthy successor and the old default route is
// left in place, and a commit that keeps its WAN vacates nothing, so a
// flush would only churn or reset live flows. Best-effort per
// docs/wan-monitoring.md — a resolve or flush failure is logged and
// metered but never fails the Decision; the routes have already
// converged.
func (d *daemon) flushSwitchedConntrack(ctx context.Context, g selector.Group, old selector.Active) {
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
		log := d.logger.With("group", g.Name, "wan", old.Wan, "family", family, "ip", ip)
		res, err := d.kernel.FlushConntrack(ctx, family, ip)
		if err != nil {
			log.Warn("conntrack flush", "flush", res, "err", err)
			d.metrics.ApplyOpErrors.WithLabelValues(g.Name, "conntrack_flush").Inc()
			continue
		}
		log.Info("conntrack flushed", "flush", res)
	}
}

// retryGroupDecisions forwards each Probe cycle. Groups decide whether
// this WAN can advance a pending Decision or a due Repair; callers never
// inspect intent.
func (d *daemon) retryGroupDecisions(ctx context.Context, wan string) {
	for name, g := range d.groups {
		d.publishDecision(ctx, d.cfg.Groups[name], g.Probe(ctx, wan))
	}
}

// applyRoutes writes the default route per family of selection, the
// WAN of the Selection being applied. With no `families` argument it
// writes every family the WAN probes. The Decision module limits a
// route refresh to the observed family. Families the WAN doesn't
// probe are skipped either way.
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
func (d *daemon) applyRoutes(ctx context.Context, g selector.Group, selection string, families ...probe.Family) error {
	wan, ok := d.cfg.Wans[selection]
	if !ok {
		return fmt.Errorf("apply routes: unknown wan %q", selection)
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
					"group", g.Name, "wan", selection, "family", famLabel,
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
		return fmt.Errorf("apply routes: %d route write(s) failed for wan %q", failed, selection)
	}
	return nil
}

// handleRouteEvent absorbs an rtnetlink default-route observation
// into the gateway cache and forwards it to the Group Decision module
// for each WAN on the affected interface, even when the Gateway is
// unchanged: the kernel flushes IPv4 routes through a device that goes
// down without an RTM_DELROUTE, so a re-added main-table default with
// the same Gateway may be the only sign that a Group-table route is
// gone (docs/adr/0017-repair-committed-group-table.md). The module
// owns whether to retry a Decision or refresh a committed Selection.
func (d *daemon) handleRouteEvent(ctx context.Context, e rtnl.RouteEvent) {
	prev, hadPrev := d.gateways.get(e.Iface, e.Family)
	switch e.Op {
	case rtnl.RouteEventAdd:
		d.gateways.set(e.Iface, e.Family, e.Gateway)
	case rtnl.RouteEventDel:
		d.gateways.clear(e.Iface, e.Family)
	}

	for wanName, wan := range d.cfg.Wans {
		if wan.Interface != e.Iface {
			continue
		}
		for name, g := range d.groups {
			committed := g.RouteObserved(ctx, wanName, probe.Family(e.Family))
			d.publishDecision(ctx, d.cfg.Groups[name], committed)
		}
	}
	// Republish state.json on any gateway-cache mutation. A change
	// that completes a pending Decision was captured by
	// publishDecision above; everything else (a new gateway on the
	// WAN of a Selection, a gateway disappearing on the standby)
	// wouldn't otherwise update state.json, leaving the
	// wans[<name>].gateways[v4|v6] fields stale.
	changed := !hadPrev || e.Op == rtnl.RouteEventDel || !prev.Equal(e.Gateway)
	if changed {
		d.writeStateSnapshot(time.Time{})
	}
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
		Version:   version,
		StartedAt: d.startedAt,
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
func (d *daemon) notifyHooks(g selector.Group, event state.Event, committed *decision.Commit) {
	old, next := committed.Old, committed.New
	interfaceOld := interfaceFor(d.cfg.Wans, old)
	interfaceNew := interfaceFor(d.cfg.Wans, next)
	hookCtx := state.HookContext{
		Event:        event,
		Group:        g.Name,
		WanOld:       old.Wan,
		WanNew:       next.Wan,
		InterfaceOld: interfaceOld,
		InterfaceNew: interfaceNew,
		// Gateway env vars come from the discovery cache. They're
		// blank when (a) the interface has no cached default route yet,
		// or (b) the route is scope-link (point-to-point) so there
		// is no gateway to surface.
		GatewayV4Old: d.gateways.string(interfaceOld, rtnl.RouteFamilyV4),
		GatewayV4New: d.gateways.string(interfaceNew, rtnl.RouteFamilyV4),
		GatewayV6Old: d.gateways.string(interfaceOld, rtnl.RouteFamilyV6),
		GatewayV6New: d.gateways.string(interfaceNew, rtnl.RouteFamilyV6),
		FamiliesOld:  probedFamiliesFor(d.cfg.Wans, old),
		FamiliesNew:  probedFamiliesFor(d.cfg.Wans, next),
		Table:        g.Table,
		Mark:         g.Mark,
		Timestamp:    committed.At,
	}
	d.hooks.Notify(hookCtx)
}
