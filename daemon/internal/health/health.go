// Package health owns each WAN's Health: threshold verdicts, Hysteresis,
// the cold-start gate, Family-policy aggregation, and carrier/operstate.
// Callers forward ProbeResults and LinkEvents and act on the returned
// Change; they never sequence Health's internal steps themselves.
package health

import (
	"cmp"
	"slices"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// Change reports what one event did to Health.
type Change struct {
	// Flipped lists the WANs whose Health changed, sorted by name.
	Flipped []string
	// Republish reports a transition daemon-state.md publishes: a
	// Family's Health or first full Window, or a carrier or operstate
	// change.
	Republish bool
}

// Wans owns the Health of every configured WAN. Its methods run
// synchronously on the caller's goroutine; it is not safe for
// concurrent use.
type Wans struct {
	byName  map[string]*wan
	metrics *metrics.Registry
}

type wan struct {
	name      string
	cfg       config.Wan
	carrier   rtnl.Carrier
	operstate rtnl.Operstate
	families  map[probe.Family]*family
}

// family is one (WAN, Family) slice of Health. It stays uncooked
// until its first full Window; until then it votes healthy, so
// carrier alone decides WAN Health (docs/specs/failover.md cold
// start). Without the gate a Lost first Sample, common when the probe
// loop fires before the route has converged, would seed Hysteresis
// unhealthy and flap the WAN down and up once probes catch up.
type family struct {
	targets []string
	stats   probe.FamilyStats
	hyst    *hysteresis
	healthy bool
	cooked  bool
}

// New builds cold Health for every WAN in cfgs: carrier and operstate
// unknown, no Family cooked. It sets the link-state gauges to unknown
// and performs no I/O.
func New(cfgs map[string]config.Wan, mreg *metrics.Registry) *Wans {
	w := &Wans{byName: make(map[string]*wan, len(cfgs)), metrics: mreg}
	for name, cfg := range cfgs {
		ws := &wan{name: name, cfg: cfg, families: make(map[probe.Family]*family, 2)}
		hyst := cfg.Probe.Hysteresis
		for _, fam := range cfg.Probe.Targets.Families() {
			ws.families[fam] = &family{
				targets: cfg.Probe.Targets.For(fam),
				hyst:    newHysteresis(hyst.ConsecutiveUp, hyst.ConsecutiveDown),
			}
		}
		w.byName[name] = ws
		w.recordLink(ws)
	}
	return w
}

// Probe folds one ProbeResult into its WAN. A result for an unknown
// WAN or an unprobed Family changes nothing.
//
// Until the Family's first full Window, results leave Health alone.
// The first full Window seeds Hysteresis from its threshold verdict,
// bypassing the consecutive-cycle ramp; every later result ramps.
func (w *Wans) Probe(r probe.ProbeResult) Change {
	ws, ok := w.byName[r.Wan]
	if !ok {
		return Change{}
	}
	fs, ok := ws.families[r.Family]
	if !ok {
		return Change{}
	}
	fs.stats = r.Stats
	var c Change
	if fs.cooked || r.Stats.WindowFilled {
		c = ws.fold(fs, r.Stats)
	}
	w.recordProbe(ws, fs, r)
	return c
}

// fold runs one full-Window verdict through the thresholds and
// Hysteresis of fs and reports the resulting Change.
func (ws *wan) fold(fs *family, stats probe.FamilyStats) Change {
	prev := ws.healthy()
	raw := evaluateThresholds(fs.healthy, stats, ws.cfg.Probe.Thresholds)
	var stable bool
	if fs.cooked {
		stable = fs.hyst.observe(raw)
	} else {
		stable = fs.hyst.seed(raw)
	}
	var c Change
	if !fs.cooked || stable != fs.healthy {
		c.Republish = true
	}
	fs.cooked = true
	fs.healthy = stable
	if ws.healthy() != prev {
		c.Flipped = []string{ws.name}
	}
	return c
}

// recordProbe sets the probe gauges from every result, so operators
// see live RTT and loss before the first full Window, and the Health
// gauges from the verdicts the result left behind.
func (w *Wans) recordProbe(ws *wan, fs *family, r probe.ProbeResult) {
	fam := r.Family.String()
	w.metrics.ProbeJitter.WithLabelValues(ws.name, fam).Set(float64(r.Stats.JitterMicros) / 1e6)
	w.metrics.ProbeLoss.WithLabelValues(ws.name, fam).Set(r.Stats.LossRatio)
	for _, t := range r.Stats.PerTarget {
		w.metrics.ProbeRTT.WithLabelValues(ws.name, t.Target, fam).Set(float64(t.RTTMicros) / 1e6)
	}
	w.metrics.WanFamilyHealthy.WithLabelValues(ws.name, fam).Set(boolToFloat(fs.healthy))
	w.metrics.WanHealthy.WithLabelValues(ws.name).Set(boolToFloat(ws.healthy()))
}

// Link folds a LinkEvent into every WAN on its interface before
// reporting any of them, so a caller recomputing Groups never sees a
// WAN whose link state lags a peer on the same interface.
func (w *Wans) Link(e rtnl.LinkEvent) Change {
	var c Change
	for _, ws := range w.byName {
		if ws.cfg.Interface != e.Name {
			continue
		}
		prev := ws.healthy()
		if e.Carrier != ws.carrier {
			w.metrics.WanCarrierChanges.WithLabelValues(ws.name).Inc()
		}
		if e.Carrier != ws.carrier || e.Operstate != ws.operstate {
			c.Republish = true
		}
		ws.carrier = e.Carrier
		ws.operstate = e.Operstate
		w.recordLink(ws)
		now := ws.healthy()
		w.metrics.WanHealthy.WithLabelValues(ws.name).Set(boolToFloat(now))
		if now != prev {
			c.Flipped = append(c.Flipped, ws.name)
		}
	}
	slices.Sort(c.Flipped)
	return c
}

// operstates is the wanwatch_wan_operstate state-set: every operstate
// the catalog names, each one series.
var operstates = []rtnl.Operstate{
	rtnl.OperstateUp,
	rtnl.OperstateDown,
	rtnl.OperstateDormant,
	rtnl.OperstateLowerLayerDown,
	rtnl.OperstateNotPresent,
	rtnl.OperstateTesting,
	rtnl.OperstateUnknown,
}

// knownOperstate returns o, or unknown for a kernel operstate outside
// the state-set, so the metrics and State report the same value.
func knownOperstate(o rtnl.Operstate) rtnl.Operstate {
	if !slices.Contains(operstates, o) {
		return rtnl.OperstateUnknown
	}
	return o
}

// recordLink sets the link-state gauges of ws: carrier as 1, 0, or -1
// for up, down, or unknown, and the operstate state-set with 1 on the
// current state and 0 on the others. A kernel operstate outside the
// state-set counts as unknown.
func (w *Wans) recordLink(ws *wan) {
	w.metrics.WanCarrier.WithLabelValues(ws.name).Set(carrierValue(ws.carrier))
	current := knownOperstate(ws.operstate)
	for _, o := range operstates {
		w.metrics.WanOperstate.WithLabelValues(ws.name, o.String()).Set(boolToFloat(o == current))
	}
}

// carrierValue encodes c for wanwatch_wan_carrier.
func carrierValue(c rtnl.Carrier) float64 {
	switch c {
	case rtnl.CarrierUp:
		return 1
	case rtnl.CarrierDown:
		return 0
	default:
		return -1
	}
}

// Healthy reports wan's Health; an unknown WAN is unhealthy.
func (w *Wans) Healthy(name string) bool {
	ws, ok := w.byName[name]
	return ok && ws.healthy()
}

// MemberHealth returns the selector input for g: each Member with its
// WAN's Health, sorted by WAN name so logs, Hooks, and State present
// Members the same way every cycle. A Member naming an unknown WAN is
// unhealthy.
func (w *Wans) MemberHealth(g selector.Group) []selector.MemberHealth {
	out := make([]selector.MemberHealth, 0, len(g.Members))
	for _, m := range g.Members {
		out = append(out, selector.MemberHealth{Member: m, Healthy: w.Healthy(m.Wan)})
	}
	slices.SortFunc(out, func(a, b selector.MemberHealth) int {
		return cmp.Compare(a.Member.Wan, b.Member.Wan)
	})
	return out
}

// Snapshot returns every WAN's State view, keyed by WAN name. Each
// Family carries its measured verdict and the latest Window
// statistics. Gateways are left empty: the caller owns the Gateway
// cache, which Health does not depend on.
func (w *Wans) Snapshot() map[string]state.Wan {
	out := make(map[string]state.Wan, len(w.byName))
	for name, ws := range w.byName {
		fams := make(map[string]state.FamilyHealth, len(ws.families))
		for fam, fs := range ws.families {
			fams[fam.String()] = state.FamilyHealth{
				Healthy:       fs.healthy,
				RTTSeconds:    float64(fs.stats.RTTMicros) / 1e6,
				JitterSeconds: float64(fs.stats.JitterMicros) / 1e6,
				LossRatio:     fs.stats.LossRatio,
				Targets:       fs.targets,
			}
		}
		out[name] = state.Wan{
			Interface: ws.cfg.Interface,
			Carrier:   ws.carrier.String(),
			Operstate: knownOperstate(ws.operstate).String(),
			Healthy:   ws.healthy(),
			Families:  fams,
		}
	}
	return out
}

// healthy is WAN Health: carrier up and the Family verdicts agreeing
// under the Family policy. Derived on demand from the two event
// streams, so neither can leave a stored copy stale.
func (ws *wan) healthy() bool {
	return ws.carrierUp() && ws.combineFamilies()
}

// familyPolicyAny is the familyHealthPolicy literal, per
// docs/specs/daemon-config.md, that relaxes the default "all".
const familyPolicyAny = "any"

// combineFamilies combines the Family verdicts under the WAN's
// familyHealthPolicy: "any" needs one healthy Family, and "all" (also
// the conservative fallback for an unknown policy) needs every one.
// An uncooked Family votes healthy. A WAN with no probed Family is
// unhealthy, so a WAN without a Probe is never silently up.
func (ws *wan) combineFamilies() bool {
	if len(ws.families) == 0 {
		return false
	}
	var healthy int
	for _, f := range ws.families {
		if !f.cooked || f.healthy {
			healthy++
		}
	}
	if ws.cfg.Probe.FamilyHealthPolicy == familyPolicyAny {
		return healthy > 0
	}
	return healthy == len(ws.families)
}

// evaluateThresholds is a two-threshold band-pass: at or above Down,
// unhealthy; at or below Up, healthy; in between, hold prev. The Nix
// option type guarantees Up < Down, so the band is never empty.
func evaluateThresholds(prev bool, stats probe.FamilyStats, t config.Thresholds) bool {
	lossPct := stats.LossRatio * 100
	rttMs := float64(stats.RTTMicros) / 1000
	if prev {
		return lossPct < float64(t.LossPctDown) && rttMs < float64(t.RttMsDown)
	}
	return lossPct <= float64(t.LossPctUp) && rttMs <= float64(t.RttMsUp)
}

// carrierUp reports whether the WAN's interface is operationally up
// at the link layer.
//
// Carrier and operstate are OR'd, not AND'd: either signal saying
// "up" is enough to count the link as ready. Dummy / loopback /
// some tunnel drivers leave operstate at "unknown" forever (RFC
// 2863 explicitly allows this for virtual interfaces) yet drive
// carrier via IFF_LOWER_UP; conversely some hardware drivers
// drive operstate before carrier propagates. The explicit
// "down-ish" operstates veto, so an admin-down link isn't selected
// just because the cable happens to be live.
func (ws *wan) carrierUp() bool {
	if ws.carrier == rtnl.CarrierDown {
		return false
	}
	switch ws.operstate {
	case rtnl.OperstateDown, rtnl.OperstateLowerLayerDown, rtnl.OperstateNotPresent:
		return false
	}
	return ws.carrier == rtnl.CarrierUp || ws.operstate == rtnl.OperstateUp
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
