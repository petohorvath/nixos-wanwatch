package main

import (
	"context"
	"log/slog"
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
// plus the per-family probe verdicts.
type wanState struct {
	name      string
	cfg       config.Wan
	carrier   rtnl.Carrier
	operstate rtnl.Operstate
	families  map[probe.Family]*familyState
	healthy   bool
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

// groupState is the per-group runtime slice.
type groupState struct {
	cfg            selector.Group
	active         selector.Active
	activeSince    *time.Time
	decisionsTotal int
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
}

// newDaemon constructs the runtime state from `cfg`. Hysteresis
// state machines start fresh — no probe samples observed yet — so
// every WAN begins as not-healthy until the configured number of
// consecutive cycles cross the up-threshold.
func newDaemon(cfg *config.Config, mreg *metrics.Registry, logger *slog.Logger) *daemon {
	d := &daemon{
		cfg:      cfg,
		metrics:  mreg,
		stateW:   &state.Writer{Path: cfg.Global.StatePath},
		hookR:    &state.Runner{Dir: cfg.Global.HooksDir},
		logger:   logger,
		wans:     make(map[string]*wanState, len(cfg.Wans)),
		groups:   make(map[string]*groupState, len(cfg.Groups)),
		gateways: NewGatewayCache(),
	}
	for name, wan := range cfg.Wans {
		ws := &wanState{
			name:      name,
			cfg:       wan,
			carrier:   rtnl.CarrierUnknown,
			operstate: rtnl.OperstateUnknown,
			families:  make(map[probe.Family]*familyState, 2),
			// PLAN §8 cold-start: no probe samples yet means
			// "health unknown but carrier known". Treat the WAN
			// as health-positive so a carrier-up rtnl event can
			// fire an initial Decision without waiting on the
			// probe loop.
			healthy: true,
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
	stable := fs.hyst.Observe(raw)

	d.recordProbeMetrics(r, stable)

	prevCooked := fs.cooked
	fs.cooked = true
	if prevCooked && stable == fs.healthy {
		return
	}
	fs.healthy = stable
	prevAggregate := ws.healthy
	ws.healthy = combineFamilies(ws.families, probeCfg.FamilyHealthPolicy)
	d.metrics.WanHealthy.WithLabelValues(ws.name).Set(boolToFloat(ws.healthy))

	if ws.healthy != prevAggregate {
		d.recomputeAffectedGroups(ctx, r.Wan, reasonHealth)
		return
	}
	// Per-family verdict transitioned but the aggregate did not
	// (e.g. v4 went healthy while v6 stayed down under
	// familyHealthPolicy=all). recomputeAffectedGroups will not
	// fire, but state.json still carries the stale per-family
	// slot — republish so external readers see the change.
	d.writeStateSnapshot()
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
		prevUp := ws.carrierUp()
		ws.carrier = e.Carrier
		ws.operstate = e.Operstate

		if prevCarrier != ws.carrier {
			d.metrics.WanCarrierChanges.WithLabelValues(ws.name).Inc()
		}
		d.metrics.WanCarrier.WithLabelValues(ws.name).Set(boolToFloat(ws.carrier == rtnl.CarrierUp))
		d.metrics.WanOperstate.WithLabelValues(ws.name).Set(float64(int(e.Operstate)))

		if prevUp != ws.carrierUp() {
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
// detect a change, and apply it through the apply + state + hook
// layers.
func (d *daemon) recomputeGroup(ctx context.Context, g *groupState, reason decisionReason) {
	healths := buildMemberHealth(g.cfg, d.wans)
	sel, err := selector.Select(g.cfg, healths)
	if err != nil {
		d.logger.Error("selector", "group", g.cfg.Name, "err", err)
		return
	}
	if sel.Active == g.active {
		return
	}
	old := g.active
	g.active = sel.Active
	now := time.Now().UTC()
	if sel.Active.Has {
		g.activeSince = &now
	}
	g.decisionsTotal++

	d.metrics.GroupDecisions.WithLabelValues(g.cfg.Name, string(reason)).Inc()
	d.updateGroupActiveGauge(g)

	d.logger.Info("decision",
		"group", g.cfg.Name,
		"reason", reason,
		"old", old.Wan,
		"new", sel.Active.Wan,
	)

	if sel.Active.Has {
		d.applyRoutes(ctx, g, sel.Active.Wan)
	}
	d.writeStateSnapshot()
	d.runHooks(g, old, sel.Active)
}

// applyRoutes writes the default route per probed family of the
// new active WAN. PointToPoint WANs get scope-link routes (no
// gateway needed). Non-PtP WANs use the gateway the GatewayCache
// learned from the kernel's main routing table; if the cache has
// no entry yet (kernel hasn't installed a default on that link),
// the family is logged + skipped — a subsequent RouteEvent will
// trigger a reapply.
func (d *daemon) applyRoutes(ctx context.Context, g *groupState, activeWan string) {
	ws, ok := d.wans[activeWan]
	if !ok {
		return
	}
	ifindex, err := interfaceIndex(ws.cfg.Interface)
	if err != nil {
		d.logger.Error("ifindex lookup", "iface", ws.cfg.Interface, "err", err)
		d.metrics.ApplyOpErrors.WithLabelValues(g.cfg.Name, "rule_install").Inc()
		return
	}
	for fam := range ws.families {
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
				continue
			}
			route.Gateway = gw
		}
		started := time.Now()
		err := apply.WriteDefault(ctx, route)
		d.metrics.ApplyRouteDuration.WithLabelValues(g.cfg.Name, famLabel).Observe(time.Since(started).Seconds())
		if err != nil {
			d.logger.Error("route write", "group", g.cfg.Name, "family", famLabel, "err", err)
			d.metrics.ApplyRouteErrors.WithLabelValues(g.cfg.Name, famLabel).Inc()
		}
	}
}

// handleRouteEvent absorbs an rtnetlink default-route observation
// into the gateway cache and reapplies any group whose active WAN
// runs on the affected interface. RTM_NEWROUTE updates the cache
// (and if the gateway differs from the prior entry, kicks a
// reapply); RTM_DELROUTE clears the entry.
//
// The reapply path is intentionally non-discriminating: it
// rewrites every family of the active WAN, not just the one that
// changed. RouteReplace is idempotent so re-writing a known-good
// route costs one extra netlink syscall — cheaper than tracking
// per-family dirty state.
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
		if !g.active.Has {
			continue
		}
		ws, ok := d.wans[g.active.Wan]
		if !ok || ws.cfg.Interface != e.Iface {
			continue
		}
		d.applyRoutes(ctx, g, g.active.Wan)
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
	gws := d.gateways.Snapshot()
	for _, ws := range d.wans {
		fams := make(map[string]state.FamilyHealth, len(ws.families))
		for fam, fs := range ws.families {
			fams[fam.String()] = state.FamilyHealth{
				Healthy:       fs.healthy,
				RTTSeconds:    float64(fs.stats.RTTMicros) / 1e6,
				JitterSeconds: float64(fs.stats.JitterMicros) / 1e6,
				LossRatio:     fs.stats.LossRatio,
				Targets:       ws.cfg.Probe.Targets,
			}
		}
		snap.Wans[ws.name] = state.Wan{
			Interface: ws.cfg.Interface,
			Carrier:   ws.carrier.String(),
			Operstate: ws.operstate.String(),
			Healthy:   ws.healthy,
			Gateways: state.Gateways{
				V4: gws.String(ws.cfg.Interface, rtnl.RouteFamilyV4),
				V6: gws.String(ws.cfg.Interface, rtnl.RouteFamilyV6),
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

// runHooks dispatches the event matching the old→new active
// transition (see hookEventFor in decision.go) into the configured
// hook directory.
func (d *daemon) runHooks(g *groupState, old, new_ selector.Active) {
	event := hookEventFor(old, new_)
	if event == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), state.DefaultHookTimeout*time.Duration(maxHooksPerEvent))
	defer cancel()

	oldIface := ifaceFor(d.wans, old)
	newIface := ifaceFor(d.wans, new_)
	gws := d.gateways.Snapshot()
	hookCtx := state.HookContext{
		Event:    event,
		Group:    g.cfg.Name,
		WanOld:   old.Wan,
		WanNew:   new_.Wan,
		IfaceOld: oldIface,
		IfaceNew: newIface,
		// Gateway env vars come from the discovery cache. They're
		// blank when (a) the iface has no cached default route yet,
		// or (b) the route is scope-link (point-to-point) so there
		// is no gateway to surface.
		GatewayV4Old: gws.String(oldIface, rtnl.RouteFamilyV4),
		GatewayV4New: gws.String(newIface, rtnl.RouteFamilyV4),
		GatewayV6Old: gws.String(oldIface, rtnl.RouteFamilyV6),
		GatewayV6New: gws.String(newIface, rtnl.RouteFamilyV6),
		Families:     probedFamiliesFor(d.wans, new_),
		Table:        g.cfg.Table,
		Mark:         g.cfg.Mark,
	}
	results := d.hookR.Run(ctx, hookCtx)
	for _, r := range results {
		result := "ok"
		switch {
		case r.TimedOut:
			result = "timeout"
		case r.ExitCode != 0:
			result = "nonzero"
		}
		d.metrics.HookInvocations.WithLabelValues(string(event), result).Inc()
	}
}

// maxHooksPerEvent is a safety bound for the aggregate hook
// timeout: the daemon won't wait longer than this multiple of the
// per-hook timeout for one event's entire .d directory to finish.
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
