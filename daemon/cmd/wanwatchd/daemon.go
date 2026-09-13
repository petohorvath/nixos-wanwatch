package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/decision"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// familyState is the per-(WAN, family) slice of runtime state. One
// per Pinger goroutine; updated when a ProbeResult arrives.
//
// `cooked` flips to true on the first ProbeResult whose sliding
// window is filled — until then, PLAN §8 cold-start grants the
// family healthy-via-carrier (handled in combineFamilies). Without
// this an interface that boots before its first probe cycle would
// be unhealthy and the daemon would publish no Selection even when
// carrier is fine, and a partial-window verdict (e.g. the first
// sample landed Lost because the route hadn't converged) would
// seed the hysteresis unhealthy and flap the WAN once probes catch
// up.
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

// daemon bundles the runtime state and subsystem handles. Wired
// once in run(), then driven by eventLoop's dispatch.
type daemon struct {
	cfg      *config.Config
	metrics  *metrics.Registry
	stateW   *state.Writer
	hooks    *state.HookNotifier
	logger   *slog.Logger
	wans     map[string]*wanState
	groups   map[string]*decision.Group
	gateways *gatewayCache

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
func newDaemon(ctx context.Context, cfg *config.Config, mreg *metrics.Registry, logger *slog.Logger) *daemon {
	d := &daemon{
		cfg:     cfg,
		metrics: mreg,
		stateW:  &state.Writer{Path: cfg.Global.StatePath},
		hooks: state.NewHookNotifier(ctx, cfg.Global.HooksDir,
			time.Duration(cfg.Global.HookTimeoutMs)*time.Millisecond, mreg, logger),
		logger:         logger,
		wans:           make(map[string]*wanState, len(cfg.Wans)),
		groups:         make(map[string]*decision.Group, len(cfg.Groups)),
		gateways:       newGatewayCache(),
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
			if err := apply.EnsureRule(ctx, apply.FwmarkRule{
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
	// any probe sample lands. PLAN §8 cold-start.
	d.writeStateSnapshot(time.Time{})
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

	// Cold-start gate: defer hysteresis seed until the first probe
	// Window is *full*. Until then, the family stays `cooked=false`
	// and combineFamilies treats it as healthy via carrier alone
	// (PLAN §8). Without this gate, a Lost first Sample — common on
	// loaded CI runners, where the probe loop fires before the
	// route to the target has converged — seeds the hysteresis
	// unhealthy and produces a spurious down→up Decision pair once
	// probes catch up. The per-target windows agree on Filled via
	// FamilyStats.WindowFilled from probe.Aggregate.
	//
	// Metrics and the apply retry still fire on every cycle —
	// operators want live RTT/loss in Prometheus from the first
	// Sample, and a Decision pending its kernel apply needs a kick
	// regardless of cold-start state.
	if !fs.cooked && !r.Stats.WindowFilled {
		d.recordProbeMetrics(r, false)
		d.retryGroupDecisions(ctx, r.Wan)
		return
	}

	probeCfg := ws.cfg.Probe
	raw := evaluateThresholds(fs.healthy, r.Stats, probeCfg.Thresholds)
	// Capture effective Health before any family-state mutation —
	// fs.cooked and fs.healthy below both feed ws.healthy().
	prevHealthy := ws.healthy()

	// First *full* Window for a (WAN, family) seeds the hysteresis
	// from the measured Health (PLAN §8 cold-start handoff); every
	// Window after ramps through Observe's consecutive-cycle logic.
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
	d.retryGroupDecisions(ctx, r.Wan)

	if prevCooked && stable == fs.healthy {
		return
	}
	fs.healthy = stable
	nowHealthy := ws.healthy()
	d.metrics.WanHealthy.WithLabelValues(ws.name).Set(boolToFloat(nowHealthy))

	if nowHealthy != prevHealthy {
		d.recomputeAffectedGroups(ctx, r.Wan, reasonHealth)
	}
	// Republish state.json on any per-family verdict transition.
	// A family flip that *does* move the aggregate has already
	// been captured by publishDecision via recomputeAffectedGroups
	// above; one that doesn't (e.g. v4 drops while v6 holds under
	// familyHealthPolicy=any) wouldn't otherwise update state.json
	// at all, leaving wans[<name>].families[<f>].healthy stale
	// relative to the live Prometheus view. PLAN §5.5: state.json
	// mirrors per-family Health, not just Decisions.
	d.writeStateSnapshot(time.Time{})
}

// handleLinkEvent updates per-WAN carrier/operstate. Carrier-down
// fast-tracks the WAN to unhealthy (PLAN §8 cold-start invariant)
// — the selector sees the carrier change immediately, without
// waiting for the probe to time out.
func (d *daemon) handleLinkEvent(ctx context.Context, e rtnl.LinkEvent) {
	var healthChangedWANs []string
	stateChanged := false
	for _, ws := range d.wans {
		if ws.cfg.Interface != e.Name {
			continue
		}
		prevCarrier := ws.carrier
		prevOperstate := ws.operstate
		prevHealthy := ws.healthy()
		ws.carrier = e.Carrier
		ws.operstate = e.Operstate

		if prevCarrier != ws.carrier {
			d.metrics.WanCarrierChanges.WithLabelValues(ws.name).Inc()
		}
		d.metrics.WanCarrier.WithLabelValues(ws.name).Set(boolToFloat(ws.carrier == rtnl.CarrierUp))
		d.metrics.WanOperstate.WithLabelValues(ws.name).Set(float64(int(e.Operstate)))

		if ws.healthy() != prevHealthy {
			healthChangedWANs = append(healthChangedWANs, ws.name)
		}
		if prevCarrier != ws.carrier || prevOperstate != ws.operstate {
			stateChanged = true
		}
	}

	// Fold the event into every WAN sharing the interface before
	// recomputing any Group. Otherwise map iteration can expose a
	// transient Selection through a matching WAN whose link state has
	// not yet been updated.
	for _, wan := range healthChangedWANs {
		d.recomputeAffectedGroups(ctx, wan, reasonCarrier)
	}
	// Republish state.json on any carrier/operstate transition.
	// LinkSubscriber dedupes upstream so a fresh event always
	// represents a real change, but a change that doesn't move Health
	// (e.g. operstate down → dormant) would not otherwise update
	// state.json.
	if stateChanged {
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
		committed := d.groups[name].Recompute(ctx, buildMemberHealth(g, d.wans), string(reason))
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
// PLAN §6.1 — a resolve or flush failure is logged and metered but
// never fails the Decision; the routes have already converged.
func (d *daemon) flushSwitchedConntrack(ctx context.Context, g selector.Group, old, next selector.Active) {
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
			"group", g.Name, "wan", old.Wan, "iface", ws.cfg.Interface, "err", err)
		d.metrics.ApplyOpErrors.WithLabelValues(g.Name, "conntrack_flush").Inc()
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
// is *not* a failure: that write is intentionally deferred (PLAN
// §6), and handleRouteEvent reapplies it once the gateway is
// discovered.
func (d *daemon) applyRoutes(ctx context.Context, g selector.Group, activeWan string, families ...probe.Family) error {
	ws, ok := d.wans[activeWan]
	if !ok {
		return fmt.Errorf("apply routes: unknown wan %q", activeWan)
	}
	ifindex, err := d.ifindexOf(ws.cfg.Interface)
	if err != nil {
		d.logger.Error("ifindex lookup", "iface", ws.cfg.Interface, "err", err)
		d.metrics.ApplyOpErrors.WithLabelValues(g.Name, "ifindex_lookup").Inc()
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
			Table:   g.Table,
			IfIndex: ifindex,
		}
		switch {
		case ws.cfg.PointToPoint:
			route.PointToPoint = true
		default:
			gw, ok := d.gateways.get(ws.cfg.Interface, rtnl.RouteFamily(fam))
			if !ok || gw == nil {
				d.logger.Info("no gateway in cache; skipping route write (will reapply on discovery)",
					"group", g.Name, "wan", activeWan, "family", famLabel,
					"iface", ws.cfg.Interface)
				return false
			}
			route.Gateway = gw
		}
		started := time.Now()
		err := d.writeRoute(ctx, route)
		d.metrics.ApplyRouteDuration.WithLabelValues(g.Name, famLabel).Observe(time.Since(started).Seconds())
		if err != nil {
			d.logger.Error("route write", "group", g.Name, "family", famLabel, "err", err)
			d.metrics.ApplyRouteErrors.WithLabelValues(g.Name, famLabel).Inc()
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

	for wan, ws := range d.wans {
		if ws.cfg.Interface != e.Iface {
			continue
		}
		for name, g := range d.groups {
			committed := g.GatewayChanged(ctx, wan, probe.Family(e.Family))
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
		Wans:      make(map[string]state.Wan, len(d.wans)),
		Groups:    make(map[string]state.Group, len(d.groups)),
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
				V4: d.gateways.string(ws.cfg.Interface, rtnl.RouteFamilyV4),
				V6: d.gateways.string(ws.cfg.Interface, rtnl.RouteFamilyV6),
			},
			Families: fams,
		}
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

	oldIface := ifaceFor(d.wans, old)
	nextIface := ifaceFor(d.wans, next)
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
		Families:     probedFamiliesFor(d.wans, next),
		Table:        g.Table,
		Mark:         g.Mark,
		Timestamp:    now,
	}
	d.hooks.Notify(hookCtx)
}

func (d *daemon) recordProbeMetrics(r probe.ProbeResult, stableHealthy bool) {
	famLabel := r.Family.String()
	d.metrics.ProbeJitter.WithLabelValues(r.Wan, famLabel).Set(float64(r.Stats.JitterMicros) / 1e6)
	d.metrics.ProbeLoss.WithLabelValues(r.Wan, famLabel).Set(r.Stats.LossRatio)
	for _, t := range r.Stats.PerTarget {
		d.metrics.ProbeRTT.WithLabelValues(r.Wan, t.Target, famLabel).Set(float64(t.RTTMicros) / 1e6)
	}
	d.metrics.WanFamilyHealthy.WithLabelValues(r.Wan, famLabel).Set(boolToFloat(stableHealthy))
}
