package main

import (
	"context"
	"errors"
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
type familyState struct {
	family  probe.Family
	stats   probe.FamilyStats
	hyst    *selector.HysteresisState
	healthy bool
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
func (w *wanState) carrierUp() bool {
	return w.carrier == rtnl.CarrierUp && w.operstate == rtnl.OperstateUp
}

// groupState is the per-group runtime slice.
type groupState struct {
	cfg            selector.Group
	active         *string
	activeSince    *time.Time
	decisionsTotal int
}

// daemon bundles the runtime state and subsystem handles. Wired
// once in run(), then driven by eventLoop's dispatch.
type daemon struct {
	cfg        *config.Config
	metrics    *metrics.Registry
	stateW     *state.Writer
	hookR      *state.Runner
	logger     *slog.Logger
	wans       map[string]*wanState
	groups     map[string]*groupState
	started    time.Time
	startupRun bool
}

// newDaemon constructs the runtime state from `cfg`. Hysteresis
// state machines start fresh — no probe samples observed yet — so
// every WAN begins as not-healthy until the configured number of
// consecutive cycles cross the up-threshold.
func newDaemon(cfg *config.Config, mreg *metrics.Registry, logger *slog.Logger) *daemon {
	d := &daemon{
		cfg:     cfg,
		metrics: mreg,
		stateW:  &state.Writer{Path: cfg.Global.StatePath},
		hookR:   &state.Runner{Dir: cfg.Global.HooksDir},
		logger:  logger,
		wans:    make(map[string]*wanState, len(cfg.Wans)),
		groups:  make(map[string]*groupState, len(cfg.Groups)),
		started: time.Now().UTC(),
	}
	for name, wan := range cfg.Wans {
		ws := &wanState{
			name:      name,
			cfg:       wan,
			carrier:   rtnl.CarrierUnknown,
			operstate: rtnl.OperstateUnknown,
			families:  make(map[probe.Family]*familyState, 2),
		}
		if wan.Gateways.V4 != nil {
			ws.families[probe.FamilyV4] = &familyState{
				family: probe.FamilyV4,
				hyst:   selector.NewHysteresisState(),
			}
		}
		if wan.Gateways.V6 != nil {
			ws.families[probe.FamilyV6] = &familyState{
				family: probe.FamilyV6,
				hyst:   selector.NewHysteresisState(),
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
func (d *daemon) bootstrap() error {
	for _, g := range d.cfg.Groups {
		for _, fam := range []probe.Family{probe.FamilyV4, probe.FamilyV6} {
			if err := apply.EnsureRule(apply.FwmarkRule{
				Family: toApplyFamily(fam),
				Mark:   g.Mark,
				Table:  g.Table,
			}); err != nil {
				return err
			}
		}
	}
	d.logger.Info("fwmark rules installed", "groups", len(d.cfg.Groups))
	return nil
}

// handleProbeResult folds a per-cycle result into the daemon's
// runtime state. If the (WAN, family) Healthy verdict changes, it
// recomputes every group containing the WAN.
func (d *daemon) handleProbeResult(r probe.ProbeResult) {
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
	stable := fs.hyst.Observe(raw,
		probeCfg.Hysteresis.ConsecutiveUp,
		probeCfg.Hysteresis.ConsecutiveDown,
	)

	d.recordProbeMetrics(r, stable)

	if stable == fs.healthy {
		return
	}
	fs.healthy = stable
	prevAggregate := ws.healthy
	ws.healthy = combineFamilies(ws.families, probeCfg.FamilyHealthPolicy)
	d.metrics.WanHealthy.WithLabelValues(ws.name).Set(boolToFloat(ws.healthy))

	if ws.healthy != prevAggregate {
		d.recomputeAffectedGroups(r.Wan, reasonHealth)
	}
}

// handleLinkEvent updates per-WAN carrier/operstate. Carrier-down
// fast-tracks the WAN to unhealthy (PLAN §8 cold-start invariant)
// — the selector sees the carrier change immediately, without
// waiting for the probe to time out.
func (d *daemon) handleLinkEvent(e rtnl.LinkEvent) {
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
			d.recomputeAffectedGroups(ws.name, reasonCarrier)
		}
		return
	}
}

// recomputeAffectedGroups runs selector.Apply for every group
// containing `wan` and applies any resulting change.
func (d *daemon) recomputeAffectedGroups(wan string, reason decisionReason) {
	for _, g := range d.groups {
		if !groupContainsWAN(g.cfg, wan) {
			continue
		}
		d.recomputeGroup(g, reason)
	}
}

// recomputeGroup is the per-group Decision path: run the selector,
// detect a change, and apply it through the apply + state + hook
// layers.
func (d *daemon) recomputeGroup(g *groupState, reason decisionReason) {
	healths := buildMemberHealth(g.cfg, d.wans)
	sel, err := selector.Apply(g.cfg, healths)
	if err != nil {
		d.logger.Error("selector", "group", g.cfg.Name, "err", err)
		return
	}
	if equalStringPtr(sel.Active, g.active) {
		return
	}
	old := g.active
	g.active = sel.Active
	now := time.Now().UTC()
	if sel.Active != nil {
		g.activeSince = &now
	}
	g.decisionsTotal++

	d.metrics.GroupDecisions.WithLabelValues(g.cfg.Name, string(reason)).Inc()
	d.updateGroupActiveGauge(g)

	d.logger.Info("decision",
		"group", g.cfg.Name,
		"reason", reason,
		"old", strPtr(old),
		"new", strPtr(sel.Active),
	)

	if sel.Active != nil {
		d.applyRoutes(g, *sel.Active)
	}
	d.writeStateSnapshot()
	d.runHooks(g, old, sel.Active, reason)
}

// applyRoutes writes the default route in each family the new
// active member has a gateway in. Families it doesn't have a
// gateway in are left untouched per PLAN §5.5 (retain vs clear is
// a v0.2 knob).
func (d *daemon) applyRoutes(g *groupState, activeWan string) {
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
	for _, fam := range []probe.Family{probe.FamilyV4, probe.FamilyV6} {
		gw := gatewayFor(ws.cfg, fam)
		if gw == nil {
			continue
		}
		started := time.Now()
		err := apply.WriteDefault(apply.DefaultRoute{
			Family:  toApplyFamily(fam),
			Table:   g.cfg.Table,
			Gateway: gw,
			IfIndex: ifindex,
		})
		dur := time.Since(started).Seconds()
		famLabel := fmtFamily(fam)
		d.metrics.ApplyRouteDuration.WithLabelValues(g.cfg.Name, famLabel).Observe(dur)
		if err != nil {
			d.logger.Error("route write", "group", g.cfg.Name, "family", famLabel, "err", err)
			d.metrics.ApplyRouteErrors.WithLabelValues(g.cfg.Name, famLabel).Inc()
		}
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
		fams := make(map[string]state.Family, len(ws.families))
		for fam, fs := range ws.families {
			fams[fmtFamily(fam)] = state.Family{
				Healthy:  fs.healthy,
				RTTMs:    float64(fs.stats.RTTMicros) / 1000,
				JitterMs: float64(fs.stats.JitterMicros) / 1000,
				LossPct:  fs.stats.LossRatio * 100,
				Targets:  ws.cfg.Probe.Targets,
			}
		}
		snap.Wans[ws.name] = state.Wan{
			Interface: ws.cfg.Interface,
			Carrier:   ws.carrier.String(),
			Operstate: ws.operstate.String(),
			Healthy:   ws.healthy,
			Families:  fams,
		}
	}
	for _, g := range d.groups {
		snap.Groups[g.cfg.Name] = state.Group{
			Active:         g.active,
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

// runHooks dispatches the appropriate event hook directory. The
// up/down/switch matrix follows PLAN §5.5:
//   - old == nil, new != nil → up
//   - old != nil, new == nil → down
//   - old != new (both non-nil) → switch
func (d *daemon) runHooks(g *groupState, old, new_ *string, reason decisionReason) {
	event := hookEventFor(old, new_)
	if event == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), state.DefaultHookTimeout*time.Duration(maxHooksPerEvent))
	defer cancel()

	hookCtx := state.HookContext{
		Event:    event,
		Group:    g.cfg.Name,
		WanOld:   strPtr(old),
		WanNew:   strPtr(new_),
		IfaceOld: ifaceFor(d.wans, old),
		IfaceNew: ifaceFor(d.wans, new_),
		GwV4Old:  gwStr(d.wans, old, probe.FamilyV4),
		GwV4New:  gwStr(d.wans, new_, probe.FamilyV4),
		GwV6Old:  gwStr(d.wans, old, probe.FamilyV6),
		GwV6New:  gwStr(d.wans, new_, probe.FamilyV6),
		Families: probedFamiliesFor(d.wans, new_),
		Table:    g.cfg.Table,
		Mark:     g.cfg.Mark,
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
	_ = reason // reserved for the "reason" hook env var once v0.2 adds it
}

// maxHooksPerEvent is a safety bound for the aggregate hook
// timeout: the daemon won't wait longer than this multiple of the
// per-hook timeout for one event's entire .d directory to finish.
const maxHooksPerEvent = 8

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func toApplyFamily(f probe.Family) apply.Family {
	if f == probe.FamilyV6 {
		return apply.FamilyV6
	}
	return apply.FamilyV4
}

func gatewayFor(w config.Wan, fam probe.Family) net.IP {
	switch fam {
	case probe.FamilyV4:
		if w.Gateways.V4 != nil {
			return net.ParseIP(*w.Gateways.V4)
		}
	case probe.FamilyV6:
		if w.Gateways.V6 != nil {
			return net.ParseIP(*w.Gateways.V6)
		}
	}
	return nil
}

func interfaceIndex(name string) (int, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, errors.New("InterfaceByName " + name + ": " + err.Error())
	}
	return iface.Index, nil
}

func hookEventFor(old, new_ *string) state.Event {
	switch {
	case old == nil && new_ != nil:
		return state.EventUp
	case old != nil && new_ == nil:
		return state.EventDown
	case old != nil && new_ != nil && *old != *new_:
		return state.EventSwitch
	}
	return ""
}

func ifaceFor(wans map[string]*wanState, name *string) string {
	if name == nil {
		return ""
	}
	if w, ok := wans[*name]; ok {
		return w.cfg.Interface
	}
	return ""
}

func gwStr(wans map[string]*wanState, name *string, fam probe.Family) string {
	if name == nil {
		return ""
	}
	w, ok := wans[*name]
	if !ok {
		return ""
	}
	switch fam {
	case probe.FamilyV4:
		if w.cfg.Gateways.V4 != nil {
			return *w.cfg.Gateways.V4
		}
	case probe.FamilyV6:
		if w.cfg.Gateways.V6 != nil {
			return *w.cfg.Gateways.V6
		}
	}
	return ""
}

func probedFamiliesFor(wans map[string]*wanState, name *string) []string {
	if name == nil {
		return nil
	}
	w, ok := wans[*name]
	if !ok {
		return nil
	}
	out := make([]string, 0, 2)
	for fam := range w.families {
		out = append(out, fmtFamily(fam))
	}
	return out
}

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (d *daemon) recordProbeMetrics(r probe.ProbeResult, stableHealthy bool) {
	famLabel := fmtFamily(r.Family)
	d.metrics.ProbeJitter.WithLabelValues(r.Wan, famLabel).Set(float64(r.Stats.JitterMicros) / 1000)
	d.metrics.ProbeLoss.WithLabelValues(r.Wan, famLabel).Set(r.Stats.LossRatio)
	for _, t := range r.Stats.PerTarget {
		d.metrics.ProbeRTT.WithLabelValues(r.Wan, t.Target, famLabel).Set(float64(t.RTTMicros) / 1000)
	}
	d.metrics.WanFamilyHealthy.WithLabelValues(r.Wan, famLabel).Set(boolToFloat(stableHealthy))
}

func (d *daemon) updateGroupActiveGauge(g *groupState) {
	for _, m := range g.cfg.Members {
		v := 0.0
		if g.active != nil && *g.active == m.Wan {
			v = 1
		}
		d.metrics.GroupActive.WithLabelValues(g.cfg.Name, m.Wan).Set(v)
	}
}
