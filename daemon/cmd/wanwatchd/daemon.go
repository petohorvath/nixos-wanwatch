package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// familyState is the per-(WAN, family) slice of runtime state. One
// per Pinger goroutine; updated when a ProbeResult arrives.
//
// `cooked` flips to true on the first ProbeResult — until then,
// PLAN §8 cold-start grants the family healthy-via-carrier
// (handled in combineFamilies). Without this, an interface that
// boots before its first probe cycle would be unhealthy and the
// daemon would publish no Selection even when carrier is fine.
type familyState struct {
	family  probe.Family
	stats   probe.FamilyStats
	hyst    *selector.HysteresisState
	healthy bool
	cooked  bool
}

// wanState is the per-WAN slice — carrier/operstate (from rtnl)
// plus the per-family probe verdicts. Effective Health is computed
// on demand by healthy(), never stored.
type wanState struct {
	name      string
	cfg       config.Wan
	carrier   rtnl.Carrier
	operstate rtnl.Operstate
	families  map[probe.Family]*familyState
}

// carrierUp returns whether the WAN's interface is currently
// operationally up at the link layer — gates everything downstream.
//
// Carrier and operstate are OR'd, not AND'd: either signal saying
// "up" is enough to count the link as ready. Dummy / loopback /
// some tunnel drivers leave operstate at "unknown" forever (RFC
// 2863 explicitly allows this for virtual interfaces) yet drive
// carrier via IFF_LOWER_UP; conversely some hardware drivers
// drive operstate before carrier propagates. We additionally
// reject the explicit "down-ish" operstates so an admin-down
// link isn't selected just because the cable happens to be live.
func (w *wanState) carrierUp() bool {
	if w.carrier == rtnl.CarrierDown {
		return false
	}
	switch w.operstate {
	case rtnl.OperstateDown, rtnl.OperstateLowerLayerDown, rtnl.OperstateNotPresent:
		return false
	}
	return w.carrier == rtnl.CarrierUp || w.operstate == rtnl.OperstateUp
}

// healthy is the WAN's effective Health: carrier up AND the
// per-family probe verdicts agreeing under the configured policy.
// Computed, never stored — it derives from two independent event
// streams (carrier from rtnl, probes from the pinger loop), and a
// stored field would inevitably go stale when one updated without
// the other. combineFamilies counts an uncooked family as healthy,
// so before the first probe Window this reduces to carrierUp() —
// the PLAN §8 cold-start rule.
func (w *wanState) healthy() bool {
	return w.carrierUp() && combineFamilies(w.families, w.cfg.Probe.FamilyHealthPolicy)
}

// groupState is the per-group runtime slice.
//
// `active` is the last Decision whose routes actually landed in the
// kernel — what state.json reports and what hooks fire for. When the
// selector makes a Decision whose apply hasn't fully converged yet,
// the target is held in `pendingActive` with `applyPending` set;
// state.json and hooks stay deferred until it converges, so they
// never report a switch the kernel hasn't made.
type groupState struct {
	cfg            selector.Group
	active         selector.Active
	activeSince    *time.Time
	decisionsTotal int

	applyPending  bool
	pendingActive selector.Active
}

// intent is the group's current target Selection: the pending
// Decision if one is mid-apply, otherwise the converged active.
func (g *groupState) intent() selector.Active {
	if g.applyPending {
		return g.pendingActive
	}
	return g.active
}

// daemon bundles the runtime state and subsystem handles. Wired
// once in run(), then driven by eventLoop's dispatch.
type daemon struct {
	cfg      *config.Config
	metrics  *metrics.Registry
	stateW   *state.Writer
	hookR    *state.Runner
	logger   *slog.Logger
	wans     map[string]*wanState
	groups   map[string]*groupState
	gateways *GatewayCache

	// The syscall-touching seams of the apply path — newDaemon wires
	// each to its production function; tests substitute fakes (the
	// sandbox grants no CAP_NET_ADMIN). ifindexOf and writeRoute drive
	// applyRoutes; interfaceAddrs and flushConntrack drive the
	// post-switch conntrack flush.
	ifindexOf      func(name string) (int, error)
	writeRoute     func(ctx context.Context, r apply.DefaultRoute) error
	interfaceAddrs func(name string) ([]net.IP, error)
	flushConntrack func(ctx context.Context, family probe.Family, ip net.IP) (uint, error)
}

// newDaemon constructs the runtime state from `cfg` — the per-WAN
// and per-group slices plus a fresh hysteresis state machine per
// (WAN, family). It performs no I/O and starts no goroutines.
func newDaemon(cfg *config.Config, mreg *metrics.Registry, logger *slog.Logger) *daemon {
	d := &daemon{
		cfg:     cfg,
		metrics: mreg,
		stateW:  &state.Writer{Path: cfg.Global.StatePath},
		hookR: &state.Runner{
			Dir:      cfg.Global.HooksDir,
			MaxHooks: maxHooksPerEvent,
			Timeout:  time.Duration(cfg.Global.HookTimeoutMs) * time.Millisecond,
		},
		logger:         logger,
		wans:           make(map[string]*wanState, len(cfg.Wans)),
		groups:         make(map[string]*groupState, len(cfg.Groups)),
		gateways:       NewGatewayCache(),
		ifindexOf:      interfaceIndex,
		writeRoute:     apply.WriteDefault,
		interfaceAddrs: interfaceAddrs,
		flushConntrack: apply.FlushBySource,
	}
	for name, wan := range cfg.Wans {
		ws := &wanState{
			name:      name,
			cfg:       wan,
			carrier:   rtnl.CarrierUnknown,
			operstate: rtnl.OperstateUnknown,
			families:  make(map[probe.Family]*familyState, 2),
		}
		fams := familiesFromTargets(wan.Probe.Targets)
		hyst := wan.Probe.Hysteresis
		if fams.v4 {
			ws.families[probe.FamilyV4] = &familyState{
				family: probe.FamilyV4,
				hyst:   selector.NewHysteresisState(hyst.ConsecutiveUp, hyst.ConsecutiveDown),
			}
		}
		if fams.v6 {
			ws.families[probe.FamilyV6] = &familyState{
				family: probe.FamilyV6,
				hyst:   selector.NewHysteresisState(hyst.ConsecutiveUp, hyst.ConsecutiveDown),
			}
		}
		d.wans[name] = ws
	}
	for name, g := range cfg.Groups {
		d.groups[name] = &groupState{cfg: g}
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
			if err := apply.EnsureRule(ctx, apply.FwmarkRule{
				Family: fam,
				Mark:   g.Mark,
				Table:  g.Table,
			}); err != nil {
				return err
			}
		}
	}
	d.logger.Info("fwmark rules installed", "groups", len(d.cfg.Groups))

	// Publish an initial state.json so consumers (state-readers,
	// wanwatch_state_publications_total, integration checks) see
	// the daemon's view from the very first scrape — even before
	// any probe sample lands. PLAN §8 cold-start.
	d.writeStateSnapshot()
	return nil
}

// handleProbeResult folds a per-cycle result into the daemon's
// runtime state. If the (WAN, family) Healthy verdict changes, it
// recomputes every group containing the WAN.
func (d *daemon) handleProbeResult(ctx context.Context, r probe.ProbeResult) {
	ws, ok := d.wans[r.Wan]
	if !ok {
		return
	}
	fs, ok := ws.families[r.Family]
	if !ok {
		return
	}
	fs.stats = r.Stats

	probeCfg := ws.cfg.Probe
	raw := evaluateThresholds(fs.healthy, r.Stats, probeCfg.Thresholds)
	// Capture effective Health before any family-state mutation —
	// fs.cooked and fs.healthy below both feed ws.healthy().
	prevHealthy := ws.healthy()

	// First Window for a (WAN, family) seeds the hysteresis from the
	// measured Health (PLAN §8 cold-start handoff); every Window
	// after ramps through Observe's consecutive-cycle logic.
	prevCooked := fs.cooked
	fs.cooked = true
	var stable bool
	if prevCooked {
		stable = fs.hyst.Observe(raw)
	} else {
		stable = fs.hyst.Seed(raw)
	}

	d.recordProbeMetrics(r, stable)

	// A probe result means r.Wan is reachable — retry any of its
	// Decisions whose apply hasn't landed yet.
	d.retryPendingApply(ctx, r.Wan)

	if prevCooked && stable == fs.healthy {
		return
	}
	fs.healthy = stable
	nowHealthy := ws.healthy()
	d.metrics.WanHealthy.WithLabelValues(ws.name).Set(boolToFloat(nowHealthy))

	if nowHealthy != prevHealthy {
		d.recomputeAffectedGroups(ctx, r.Wan, reasonHealth)
	}
	// A per-family verdict can transition without moving the
	// aggregate (e.g. v4 drops while v6 holds under
	// familyHealthPolicy=any). That is not a Decision, so state.json
	// is deliberately not republished — it is a Decision snapshot
	// (PLAN §5.5), and the live per-family view is the Prometheus
	// endpoint, updated above via recordProbeMetrics.
}

// handleLinkEvent updates per-WAN carrier/operstate. Carrier-down
// fast-tracks the WAN to unhealthy (PLAN §8 cold-start invariant)
// — the selector sees the carrier change immediately, without
// waiting for the probe to time out.
func (d *daemon) handleLinkEvent(ctx context.Context, e rtnl.LinkEvent) {
	for _, ws := range d.wans {
		if ws.cfg.Interface != e.Name {
			continue
		}
		prevCarrier := ws.carrier
		prevHealthy := ws.healthy()
		ws.carrier = e.Carrier
		ws.operstate = e.Operstate

		if prevCarrier != ws.carrier {
			d.metrics.WanCarrierChanges.WithLabelValues(ws.name).Inc()
		}
		d.metrics.WanCarrier.WithLabelValues(ws.name).Set(boolToFloat(ws.carrier == rtnl.CarrierUp))
		d.metrics.WanOperstate.WithLabelValues(ws.name).Set(float64(int(e.Operstate)))

		if ws.healthy() != prevHealthy {
			d.recomputeAffectedGroups(ctx, ws.name, reasonCarrier)
		}
		return
	}
}

// recomputeAffectedGroups runs selector.Select for every group
// containing `wan` and applies any resulting change.
func (d *daemon) recomputeAffectedGroups(ctx context.Context, wan string, reason decisionReason) {
	for _, g := range d.groups {
		if !groupContainsWAN(g.cfg, wan) {
			continue
		}
		d.recomputeGroup(ctx, g, reason)
	}
}

// recomputeGroup is the per-group Decision path: run the selector,
// detect a change against the group's current intent, record the
// Decision (decisionsTotal, the GroupDecisions metric, the log
// line), and hand it to commitDecision — which defers the visible
// effects until the routes converge.
func (d *daemon) recomputeGroup(ctx context.Context, g *groupState, reason decisionReason) {
	healths := buildMemberHealth(g.cfg, d.wans)
	sel, err := selector.Select(g.cfg, healths)
	if err != nil {
		d.logger.Error("selector", "group", g.cfg.Name, "err", err)
		return
	}
	if sel.Active == g.intent() {
		return
	}

	d.logger.Info("decision",
		"group", g.cfg.Name,
		"reason", reason,
		"old", g.active.Wan,
		"new", sel.Active.Wan,
	)
	g.applyPending = true
	g.pendingActive = sel.Active
	g.decisionsTotal++
	d.metrics.GroupDecisions.WithLabelValues(g.cfg.Name, string(reason)).Inc()

	d.commitDecision(ctx, g)
}

// commitDecision applies the routes for the group's pending Decision.
// Once they land it publishes the Decision's visible effects —
// promote pendingActive to `active`, refresh the gauge, write
// state.json, fire the hook. A hard apply failure returns with the
// Decision still pending, so state.json and hooks never report a
// switch the kernel hasn't made; retryPendingApply or
// handleRouteEvent re-drives it.
func (d *daemon) commitDecision(ctx context.Context, g *groupState) {
	if !g.applyPending {
		return
	}
	next := g.pendingActive
	if next.Has {
		if err := d.applyRoutes(ctx, g, next.Wan); err != nil {
			d.logger.Warn("decision apply incomplete; will retry",
				"group", g.cfg.Name, "wan", next.Wan, "err", err)
			return
		}
	}

	old := g.active
	g.active = next
	g.applyPending = false
	g.pendingActive = selector.Active{}
	if next.Has {
		now := time.Now().UTC()
		g.activeSince = &now
	}
	d.updateGroupActiveGauge(g)
	d.flushSwitchedConntrack(ctx, g, old, next)
	d.writeStateSnapshot()
	d.runHooks(ctx, g, old, next)
}

// flushSwitchedConntrack clears the conntrack entries pinned to the
// vacated WAN's source addresses, so flows that were SNATted out the
// old WAN re-establish via the new one instead of being black-holed
// until their conntrack entries time out.
//
// Switch-only: it runs when both old and next are present. On a
// `down` there is no healthy successor and the old default route is
// left in place, so a flush would only churn. Best-effort per
// PLAN §6.1 — a resolve or flush failure is logged and metered but
// never fails the Decision; the routes have already converged.
func (d *daemon) flushSwitchedConntrack(ctx context.Context, g *groupState, old, next selector.Active) {
	if !old.Has || !next.Has {
		return
	}
	ws, ok := d.wans[old.Wan]
	if !ok {
		return
	}
	addrs, err := d.interfaceAddrs(ws.cfg.Interface)
	if err != nil {
		d.logger.Warn("conntrack flush: resolve vacated WAN addresses",
			"group", g.cfg.Name, "wan", old.Wan, "iface", ws.cfg.Interface, "err", err)
		d.metrics.ApplyOpErrors.WithLabelValues(g.cfg.Name, "conntrack_flush").Inc()
		return
	}
	for _, ip := range addrs {
		family := probe.FamilyV4
		if ip.To4() == nil {
			family = probe.FamilyV6
		}
		n, err := d.flushConntrack(ctx, family, ip)
		if err != nil {
			d.logger.Warn("conntrack flush",
				"group", g.cfg.Name, "wan", old.Wan, "family", family, "ip", ip, "err", err)
			d.metrics.ApplyOpErrors.WithLabelValues(g.cfg.Name, "conntrack_flush").Inc()
			continue
		}
		d.logger.Info("conntrack flushed",
			"group", g.cfg.Name, "wan", old.Wan, "family", family, "ip", ip, "entries", n)
	}
}

// retryPendingApply re-attempts commitDecision for any group whose
// pending Decision targets `wan`. Called from handleProbeResult:
// probe results arrive every cycle for an active WAN, so a transient
// apply failure converges within one probe interval without a
// dedicated retry timer.
func (d *daemon) retryPendingApply(ctx context.Context, wan string) {
	for _, g := range d.groups {
		if g.applyPending && g.pendingActive.Has && g.pendingActive.Wan == wan {
			d.commitDecision(ctx, g)
		}
	}
}

// applyRoutes writes the default route per family of the active
// WAN. With no `families` argument it writes every family the WAN
// probes (commitDecision's full pass); handleRouteEvent passes a
// single family — the one whose gateway changed — to skip the
// netlink work for families that didn't. Families the WAN doesn't
// probe are skipped either way.
//
// PointToPoint WANs get scope-link routes (no gateway needed);
// non-PtP WANs use the gateway the GatewayCache learned from the
// kernel's main routing table.
//
// It returns an error if any family *hard*-fails — the ifindex
// lookup, or a netlink write — so commitDecision can hold the
// Decision pending and retry. A family with no gateway cached yet
// is *not* a failure: that write is intentionally deferred (PLAN
// §6), and handleRouteEvent reapplies it once the gateway is
// discovered.
func (d *daemon) applyRoutes(ctx context.Context, g *groupState, activeWan string, families ...probe.Family) error {
	ws, ok := d.wans[activeWan]
	if !ok {
		return fmt.Errorf("apply routes: unknown wan %q", activeWan)
	}
	ifindex, err := d.ifindexOf(ws.cfg.Interface)
	if err != nil {
		d.logger.Error("ifindex lookup", "iface", ws.cfg.Interface, "err", err)
		d.metrics.ApplyOpErrors.WithLabelValues(g.cfg.Name, "ifindex_lookup").Inc()
		return fmt.Errorf("apply routes: ifindex %q: %w", ws.cfg.Interface, err)
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
			Table:   g.cfg.Table,
			IfIndex: ifindex,
		}
		switch {
		case ws.cfg.PointToPoint:
			route.PointToPoint = true
		default:
			gw, ok := d.gateways.Get(ws.cfg.Interface, rtnl.RouteFamily(fam))
			if !ok || gw == nil {
				d.logger.Info("no gateway in cache; skipping route write (will reapply on discovery)",
					"group", g.cfg.Name, "wan", activeWan, "family", famLabel,
					"iface", ws.cfg.Interface)
				return false
			}
			route.Gateway = gw
		}
		started := time.Now()
		err := d.writeRoute(ctx, route)
		d.metrics.ApplyRouteDuration.WithLabelValues(g.cfg.Name, famLabel).Observe(time.Since(started).Seconds())
		if err != nil {
			d.logger.Error("route write", "group", g.cfg.Name, "family", famLabel, "err", err)
			d.metrics.ApplyRouteErrors.WithLabelValues(g.cfg.Name, famLabel).Inc()
			return true
		}
		return false
	}

	var failed int
	if len(families) == 0 {
		for fam := range ws.families {
			if writeOne(fam) {
				failed++
			}
		}
	} else {
		// Explicit family set (the RouteEvent reapply path) — skip
		// families the WAN doesn't probe; the daemon has no route to
		// maintain for an unprobed family.
		for _, fam := range families {
			if _, probesIt := ws.families[fam]; !probesIt {
				continue
			}
			if writeOne(fam) {
				failed++
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("apply routes: %d route write(s) failed for wan %q", failed, activeWan)
	}
	return nil
}

// handleRouteEvent absorbs an rtnetlink default-route observation
// into the gateway cache and reapplies any group whose active WAN
// runs on the affected interface. RTM_NEWROUTE updates the cache
// (and if the gateway differs from the prior entry, kicks a
// reapply); RTM_DELROUTE clears the entry.
//
// The reapply rewrites only the family whose gateway changed —
// RouteReplace is idempotent so a full rewrite would be harmless,
// but per-family halves the netlink syscall count under flap.
func (d *daemon) handleRouteEvent(ctx context.Context, e rtnl.RouteEvent) {
	prev, hadPrev := d.gateways.Get(e.Iface, e.Family)
	switch e.Op {
	case rtnl.RouteEventAdd:
		d.gateways.Set(e.Iface, e.Family, e.Gateway)
	case rtnl.RouteEventDel:
		d.gateways.Clear(e.Iface, e.Family)
	}

	changed := !hadPrev || e.Op == rtnl.RouteEventDel || !prev.Equal(e.Gateway)
	if !changed {
		return
	}

	for _, g := range d.groups {
		want := g.intent()
		if !want.Has {
			continue
		}
		ws, ok := d.wans[want.Wan]
		if !ok || ws.cfg.Interface != e.Iface {
			continue
		}
		if g.applyPending {
			// A freshly discovered gateway may complete a Decision
			// whose apply was waiting on it.
			d.commitDecision(ctx, g)
			continue
		}
		// Already converged; rewrite just the family whose gateway
		// changed. applyRoutes logs its own failures and the health
		// pipeline handles a vanished interface, so a failure here
		// needs no further action.
		_ = d.applyRoutes(ctx, g, want.Wan, probe.Family(e.Family))
	}
}

// writeStateSnapshot serializes the runtime state into the form
// state.Writer expects, then writes atomically. Increments
// `state_publications_total` on success.
func (d *daemon) writeStateSnapshot() {
	snap := state.State{
		Wans:   make(map[string]state.Wan, len(d.wans)),
		Groups: make(map[string]state.Group, len(d.groups)),
	}
	for _, ws := range d.wans {
		fams := make(map[string]state.FamilyHealth, len(ws.families))
		for fam, fs := range ws.families {
			fams[fam.String()] = state.FamilyHealth{
				Healthy:       fs.healthy,
				RTTSeconds:    float64(fs.stats.RTTMicros) / 1e6,
				JitterSeconds: float64(fs.stats.JitterMicros) / 1e6,
				LossRatio:     fs.stats.LossRatio,
				Targets:       targetsFor(ws.cfg, fam),
			}
		}
		snap.Wans[ws.name] = state.Wan{
			Interface: ws.cfg.Interface,
			Carrier:   ws.carrier.String(),
			Operstate: ws.operstate.String(),
			Healthy:   ws.healthy(),
			Gateways: state.Gateways{
				V4: d.gateways.String(ws.cfg.Interface, rtnl.RouteFamilyV4),
				V6: d.gateways.String(ws.cfg.Interface, rtnl.RouteFamilyV6),
			},
			Families: fams,
		}
	}
	for _, g := range d.groups {
		var active *string
		if g.active.Has {
			a := g.active.Wan
			active = &a
		}
		snap.Groups[g.cfg.Name] = state.Group{
			Active:         active,
			ActiveSince:    g.activeSince,
			DecisionsTotal: g.decisionsTotal,
			Strategy:       g.cfg.Strategy,
		}
	}
	if err := d.stateW.Write(snap); err != nil {
		d.logger.Error("state write", "err", err)
		return
	}
	d.metrics.StatePublications.Inc()
}

// runHooks dispatches the event matching the old→next active
// transition (see hookEventFor in decision.go) into the configured
// hook directory. The hooks run under `parent` — the daemon
// context — so a shutdown signal cancels any in-flight hook rather
// than blocking the daemon behind it.
func (d *daemon) runHooks(parent context.Context, g *groupState, old, next selector.Active) {
	event := hookEventFor(old, next)
	if event == "" {
		return
	}

	oldIface := ifaceFor(d.wans, old)
	nextIface := ifaceFor(d.wans, next)
	hookCtx := state.HookContext{
		Event:    event,
		Group:    g.cfg.Name,
		WanOld:   old.Wan,
		WanNew:   next.Wan,
		IfaceOld: oldIface,
		IfaceNew: nextIface,
		// Gateway env vars come from the discovery cache. They're
		// blank when (a) the iface has no cached default route yet,
		// or (b) the route is scope-link (point-to-point) so there
		// is no gateway to surface.
		GatewayV4Old: d.gateways.String(oldIface, rtnl.RouteFamilyV4),
		GatewayV4New: d.gateways.String(nextIface, rtnl.RouteFamilyV4),
		GatewayV6Old: d.gateways.String(oldIface, rtnl.RouteFamilyV6),
		GatewayV6New: d.gateways.String(nextIface, rtnl.RouteFamilyV6),
		Families:     probedFamiliesFor(d.wans, next),
		Table:        g.cfg.Table,
		Mark:         g.cfg.Mark,
	}
	results := d.hookR.Run(parent, hookCtx)
	for _, r := range results {
		if r.Skipped {
			d.logger.Warn("hook skipped: per-event limit reached",
				"event", string(event), "hook", r.Path, "limit", maxHooksPerEvent)
			continue
		}
		result := "ok"
		switch {
		case r.TimedOut:
			result = "timeout"
		case r.ExitCode != 0:
			result = "nonzero"
		}
		d.metrics.HookInvocations.WithLabelValues(string(event), result).Inc()
		if result != "ok" {
			d.logger.Warn("hook failed",
				"event", string(event), "hook", r.Path, "result", result,
				"exitCode", r.ExitCode, "err", r.Err, "output", r.Output)
		}
	}
}

// maxHooksPerEvent caps how many hooks one event's `.d` directory
// may run, bounding the event loop's worst-case stall at
// maxHooksPerEvent × DefaultHookTimeout. Hooks past the cap are
// logged and skipped, not silently starved of their timeout.
const maxHooksPerEvent = 8

func (d *daemon) recordProbeMetrics(r probe.ProbeResult, stableHealthy bool) {
	famLabel := r.Family.String()
	d.metrics.ProbeJitter.WithLabelValues(r.Wan, famLabel).Set(float64(r.Stats.JitterMicros) / 1e6)
	d.metrics.ProbeLoss.WithLabelValues(r.Wan, famLabel).Set(r.Stats.LossRatio)
	for _, t := range r.Stats.PerTarget {
		d.metrics.ProbeRTT.WithLabelValues(r.Wan, t.Target, famLabel).Set(float64(t.RTTMicros) / 1e6)
	}
	d.metrics.WanFamilyHealthy.WithLabelValues(r.Wan, famLabel).Set(boolToFloat(stableHealthy))
}

func (d *daemon) updateGroupActiveGauge(g *groupState) {
	for _, m := range g.cfg.Members {
		v := 0.0
		if g.active.Has && g.active.Wan == m.Wan {
			v = 1
		}
		d.metrics.GroupActive.WithLabelValues(g.cfg.Name, m.Wan).Set(v)
	}
}
