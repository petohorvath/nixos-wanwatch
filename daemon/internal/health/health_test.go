package health

import (
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// event is one replayed input: exactly one of probe or link is set.
type event struct {
	probe *probe.ProbeResult
	link  *rtnl.LinkEvent
}

// step replays one event and states what must hold afterwards.
type step struct {
	event
	// change is the Change the event must report.
	change Change
	// healthy is the WAN Health that must hold after the event, keyed
	// by WAN name. WANs not listed are not checked.
	healthy map[string]bool
}

func link(iface string, carrier rtnl.Carrier, operstate rtnl.Operstate) event {
	return event{link: &rtnl.LinkEvent{Name: iface, Carrier: carrier, Operstate: operstate}}
}

func up(iface string) event   { return link(iface, rtnl.CarrierUp, rtnl.OperstateUp) }
func down(iface string) event { return link(iface, rtnl.CarrierDown, rtnl.OperstateDown) }

func ptr[T any](v T) *T { return &v }

// window is a probe result whose Window is full.
func window(wan string, family probe.Family, lossRatio float64, rttMs uint64) event {
	return event{probe: &probe.ProbeResult{Wan: wan, Family: family, Stats: probe.FamilyStats{
		LossRatio: ptr(lossRatio), RTTMicros: ptr(rttMs * 1000), WindowFilled: true,
	}}}
}

// partial is a probe result whose Window has not filled yet.
func partial(wan string, family probe.Family, lossRatio float64, rttMs uint64) event {
	e := window(wan, family, lossRatio, rttMs)
	e.probe.Stats.WindowFilled = false
	return e
}

// lost is a full Window in which every Sample was lost, so it has no
// RTT or jitter.
func lost(wan string, family probe.Family) event {
	return event{probe: &probe.ProbeResult{Wan: wan, Family: family, Stats: probe.FamilyStats{
		LossRatio: ptr(1.0), WindowFilled: true,
	}}}
}

// lostPartial is lost before the Window has filled.
func lostPartial(wan string, family probe.Family) event {
	e := lost(wan, family)
	e.probe.Stats.WindowFilled = false
	return e
}

func good(wan string, family probe.Family) event { return window(wan, family, 0, 10) }
func bad(wan string, family probe.Family) event  { return lost(wan, family) }

var (
	noChange  = Change{}
	republish = Change{Republish: true}
)

func flipped(wans ...string) Change { return Change{Flipped: wans, Republish: true} }

// wanCfg builds a WAN on iface probing the given Families, with
// thresholds loss 10..20 % and RTT 100..200 ms.
func wanCfg(name, iface string, up, down int, families ...probe.Family) config.Wan {
	w := config.Wan{
		Name:      name,
		Interface: iface,
		Probe: config.Probe{
			Thresholds: config.Thresholds{LossPctUp: 10, LossPctDown: 20, RttMsUp: 100, RttMsDown: 200},
			Hysteresis: config.Hysteresis{ConsecutiveUp: up, ConsecutiveDown: down},
		},
	}
	for _, f := range families {
		if f == probe.FamilyV4 {
			w.Probe.Targets.V4 = []string{"192.0.2.1"}
		} else {
			w.Probe.Targets.V6 = []string{"2001:db8::1"}
		}
	}
	return w
}

func wans(cfgs ...config.Wan) map[string]config.Wan {
	out := make(map[string]config.Wan, len(cfgs))
	for _, c := range cfgs {
		out[c.Name] = c
	}
	return out
}

// replay feeds steps into w in order and checks each step's Change
// and the WAN Health that follows it.
func replay(t *testing.T, w *Wans, steps []step) {
	t.Helper()
	for i, s := range steps {
		var got Change
		if s.probe != nil {
			got = w.Probe(*s.probe)
		} else {
			got = w.Link(*s.link)
		}
		if !slices.Equal(got.Flipped, s.change.Flipped) || got.Republish != s.change.Republish {
			t.Fatalf("step %d: Change = %+v, want %+v", i, got, s.change)
		}
		for wan, want := range s.healthy {
			if h := w.Healthy(wan); h != want {
				t.Fatalf("step %d: Healthy(%q) = %v, want %v", i, wan, h, want)
			}
		}
	}
}

func readGauge(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("Gauge.Write: %v", err)
	}
	return m.GetGauge().GetValue()
}

func TestLinkUpMakesColdWanHealthy(t *testing.T) {
	t.Parallel()
	mreg := metrics.New()
	w := New(wans(wanCfg("primary", "eth0", 1, 1, probe.FamilyV4)), mreg)
	replay(t, w, []step{
		{event: up("eth0"), change: flipped("primary"), healthy: map[string]bool{"primary": true}},
	})
	if v := readGauge(t, mreg.WanHealthy.WithLabelValues("primary")); v != 1 {
		t.Errorf("wanwatch_wan_healthy{primary} = %v, want 1", v)
	}
}

// TestLinkStateMatrix pins the carrierUp rule: either signal saying
// up suffices, an explicit carrier-down or down-ish operstate vetoes,
// and two unknowns are not up.
func TestLinkStateMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		carrier   rtnl.Carrier
		operstate rtnl.Operstate
		want      bool
	}{
		{"both up", rtnl.CarrierUp, rtnl.OperstateUp, true},
		{"carrier up, operstate unknown (dummy, tunnel)", rtnl.CarrierUp, rtnl.OperstateUnknown, true},
		{"carrier unknown, operstate up", rtnl.CarrierUnknown, rtnl.OperstateUp, true},
		{"carrier up, operstate dormant", rtnl.CarrierUp, rtnl.OperstateDormant, true},
		{"carrier down vetoes operstate up", rtnl.CarrierDown, rtnl.OperstateUp, false},
		{"operstate down vetoes carrier up", rtnl.CarrierUp, rtnl.OperstateDown, false},
		{"operstate lowerlayerdown vetoes", rtnl.CarrierUp, rtnl.OperstateLowerLayerDown, false},
		{"operstate notpresent vetoes", rtnl.CarrierUp, rtnl.OperstateNotPresent, false},
		{"both unknown", rtnl.CarrierUnknown, rtnl.OperstateUnknown, false},
		{"carrier unknown, operstate dormant", rtnl.CarrierUnknown, rtnl.OperstateDormant, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mreg := metrics.New()
			w := New(wans(wanCfg("primary", "eth0", 1, 1, probe.FamilyV4)), mreg)
			want := noChange
			if tc.want {
				want = flipped("primary")
			} else if tc.carrier != rtnl.CarrierUnknown || tc.operstate != rtnl.OperstateUnknown {
				want = republish
			}
			replay(t, w, []step{{
				event:   link("eth0", tc.carrier, tc.operstate),
				change:  want,
				healthy: map[string]bool{"primary": tc.want},
			}})
			wantCarrier := map[rtnl.Carrier]float64{rtnl.CarrierUp: 1, rtnl.CarrierDown: 0, rtnl.CarrierUnknown: -1}[tc.carrier]
			if v := readGauge(t, mreg.WanCarrier.WithLabelValues("primary")); v != wantCarrier {
				t.Errorf("wanwatch_wan_carrier = %v, want %v", v, wantCarrier)
			}
			if v := readGauge(t, mreg.WanOperstate.WithLabelValues("primary", tc.operstate.String())); v != 1 {
				t.Errorf("wanwatch_wan_operstate{state=%s} = %v, want 1", tc.operstate, v)
			}
			if v := readGauge(t, mreg.WanHealthy.WithLabelValues("primary")); v != boolToFloat(tc.want) {
				t.Errorf("wanwatch_wan_healthy = %v, want %v", v, tc.want)
			}
		})
	}
}

// TestLinkOperstateOutOfRange pins that a kernel operstate outside the
// wanwatch_wan_operstate state-set reports as unknown.
func TestLinkOperstateOutOfRange(t *testing.T) {
	t.Parallel()
	mreg := metrics.New()
	w := New(wans(wanCfg("primary", "eth0", 1, 1, probe.FamilyV4)), mreg)
	replay(t, w, []step{
		{event: up("eth0"), change: flipped("primary"), healthy: map[string]bool{"primary": true}},
		{event: link("eth0", rtnl.CarrierUp, rtnl.Operstate(42)), change: republish, healthy: map[string]bool{"primary": true}},
	})
	for st, want := range map[string]float64{"up": 0, "unknown": 1} {
		if v := readGauge(t, mreg.WanOperstate.WithLabelValues("primary", st)); v != want {
			t.Errorf("wanwatch_wan_operstate{state=%s} = %v, want %v", st, v, want)
		}
	}
}

// TestLinkFoldsIntoEveryWanOnInterface pins the shared-interface rule:
// one LinkEvent updates every WAN on its interface before the caller
// sees any Change, reports them in name order, and leaves WANs on
// other interfaces alone.
func TestLinkFoldsIntoEveryWanOnInterface(t *testing.T) {
	t.Parallel()
	mreg := metrics.New()
	w := New(wans(
		wanCfg("zeta", "eth0", 1, 1, probe.FamilyV4),
		wanCfg("alpha", "eth0", 1, 1, probe.FamilyV4),
		wanCfg("mid", "eth0", 1, 1, probe.FamilyV6),
		wanCfg("other", "eth1", 1, 1, probe.FamilyV4),
	), mreg)
	all := func(h bool) map[string]bool {
		return map[string]bool{"alpha": h, "mid": h, "zeta": h, "other": false}
	}
	// Repeat so both Go map iteration orders are exercised.
	for range 16 {
		replay(t, w, []step{
			{event: up("eth0"), change: flipped("alpha", "mid", "zeta"), healthy: all(true)},
			{event: up("eth0"), change: noChange, healthy: all(true)},
			{event: down("eth0"), change: flipped("alpha", "mid", "zeta"), healthy: all(false)},
		})
	}
	for _, name := range []string{"alpha", "mid", "zeta"} {
		var m dto.Metric
		if err := mreg.WanCarrierChanges.WithLabelValues(name).Write(&m); err != nil {
			t.Fatal(err)
		}
		if got := m.GetCounter().GetValue(); got != 32 {
			t.Errorf("wanwatch_wan_carrier_changes_total{%s} = %v, want 32", name, got)
		}
	}
}

// TestColdStart pins docs/specs/failover.md's cold-start invariant:
// partial Windows leave Health alone, the first full Window seeds
// Hysteresis from its threshold verdict without a ramp, and later
// Windows ramp normally.
func TestColdStart(t *testing.T) {
	t.Parallel()
	v4 := probe.FamilyV4
	healthy := map[string]bool{"primary": true}
	unhealthy := map[string]bool{"primary": false}
	cases := []struct {
		name  string
		steps []step
	}{
		{"partial Windows keep carrier-only Health, then a good Window seeds healthy", []step{
			{event: up("eth0"), change: flipped("primary"), healthy: healthy},
			{event: lostPartial("primary", v4), change: noChange, healthy: healthy},
			{event: partial("primary", v4, 0.5, 10), change: noChange, healthy: healthy},
			{event: good("primary", v4), change: republish, healthy: healthy},
			{event: bad("primary", v4), change: noChange, healthy: healthy},
			{event: bad("primary", v4), change: flipped("primary"), healthy: unhealthy},
		}},
		{"a bad first full Window flips down without the ramp", []step{
			{event: up("eth0"), change: flipped("primary"), healthy: healthy},
			{event: bad("primary", v4), change: flipped("primary"), healthy: unhealthy},
			{event: good("primary", v4), change: noChange, healthy: unhealthy},
			{event: good("primary", v4), change: flipped("primary"), healthy: healthy},
		}},
		{"a partial Window after cooking still counts", []step{
			{event: up("eth0"), change: flipped("primary"), healthy: healthy},
			{event: good("primary", v4), change: republish, healthy: healthy},
			{event: lostPartial("primary", v4), change: noChange, healthy: healthy},
			{event: lostPartial("primary", v4), change: flipped("primary"), healthy: unhealthy},
		}},
		{"cooking before carrier is up publishes without flipping", []step{
			{event: good("primary", v4), change: republish, healthy: unhealthy},
			{event: up("eth0"), change: flipped("primary"), healthy: healthy},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := New(wans(wanCfg("primary", "eth0", 2, 2, v4)), metrics.New())
			replay(t, w, tc.steps)
		})
	}
}

// TestThresholdBand pins the two-threshold band-pass (loss 10..20 %,
// RTT 100..200 ms here): Down bounds trip a healthy Family, Up bounds
// must both clear for an unhealthy one, and in-band values hold. The
// first full Window starts from unhealthy, so it must clear Up.
func TestThresholdBand(t *testing.T) {
	t.Parallel()
	v4 := probe.FamilyV4
	h := func(b bool) map[string]bool { return map[string]bool{"primary": b} }
	w := New(wans(wanCfg("primary", "eth0", 1, 1, v4)), metrics.New())
	replay(t, w, []step{
		{event: up("eth0"), change: flipped("primary"), healthy: h(true)},
		{event: window("primary", v4, 0.15, 10), change: flipped("primary"), healthy: h(false)},
		{event: window("primary", v4, 0.10, 100), change: flipped("primary"), healthy: h(true)},
		{event: window("primary", v4, 0.15, 10), change: noChange, healthy: h(true)},
		{event: window("primary", v4, 0, 199), change: noChange, healthy: h(true)},
		{event: window("primary", v4, 0.20, 10), change: flipped("primary"), healthy: h(false)},
		{event: window("primary", v4, 0.15, 10), change: noChange, healthy: h(false)},
		{event: window("primary", v4, 0.10, 101), change: noChange, healthy: h(false)},
		{event: window("primary", v4, 0.11, 100), change: noChange, healthy: h(false)},
		{event: window("primary", v4, 0.10, 100), change: flipped("primary"), healthy: h(true)},
		{event: window("primary", v4, 0, 200), change: flipped("primary"), healthy: h(false)},
	})
}

// TestFamilyPolicy pins how Family Health combines into WAN Health
// under probe.familyHealthPolicy. An uncooked Family votes healthy, an
// unknown policy falls back to "all", and a WAN without a probed
// Family is never healthy.
func TestFamilyPolicy(t *testing.T) {
	t.Parallel()
	v4, v6 := probe.FamilyV4, probe.FamilyV6
	h := func(b bool) map[string]bool { return map[string]bool{"primary": b} }
	cases := []struct {
		name     string
		policy   string
		families []probe.Family
		steps    []step
	}{
		{"all: one unhealthy Family is enough", "all", []probe.Family{v4, v6}, []step{
			{event: up("eth0"), change: flipped("primary"), healthy: h(true)},
			{event: bad("primary", v4), change: flipped("primary"), healthy: h(false)},
			{event: good("primary", v6), change: republish, healthy: h(false)},
			{event: good("primary", v4), change: flipped("primary"), healthy: h(true)},
		}},
		{"any: one healthy Family is enough, including an uncooked one", "any", []probe.Family{v4, v6}, []step{
			{event: up("eth0"), change: flipped("primary"), healthy: h(true)},
			{event: bad("primary", v4), change: republish, healthy: h(true)},
			{event: bad("primary", v6), change: flipped("primary"), healthy: h(false)},
			{event: good("primary", v6), change: flipped("primary"), healthy: h(true)},
		}},
		{"unknown policy falls back to all", "weird", []probe.Family{v4, v6}, []step{
			{event: up("eth0"), change: flipped("primary"), healthy: h(true)},
			{event: bad("primary", v4), change: flipped("primary"), healthy: h(false)},
		}},
		{"no probed Family is never healthy", "any", nil, []step{
			{event: up("eth0"), change: republish, healthy: h(false)},
			{event: good("primary", v4), change: noChange, healthy: h(false)},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := wanCfg("primary", "eth0", 1, 1, tc.families...)
			cfg.Probe.FamilyHealthPolicy = tc.policy
			replay(t, New(wans(cfg), metrics.New()), tc.steps)
		})
	}
}

// TestGauges pins the gauges Wans owns. Probe gauges follow every
// result, including partial Windows, and report NaN where a statistic
// has no data, from startup on. wanwatch_wan_family_healthy is the
// measured verdict, so it stays 0 until the first full Window.
// wanwatch_wan_healthy follows WAN Health after every event, including
// a link event after probes have cooked (the stale-gauge regression).
func TestGauges(t *testing.T) {
	t.Parallel()
	v4 := probe.FamilyV4
	mreg := metrics.New()
	w := New(wans(wanCfg("primary", "eth0", 1, 1, v4)), mreg)
	familyHealthy := func() float64 { return readGauge(t, mreg.WanFamilyHealthy.WithLabelValues("primary", "v4")) }
	wanHealthy := func() float64 { return readGauge(t, mreg.WanHealthy.WithLabelValues("primary")) }
	assertProbeGauges := func(step string, rtt, jitter, loss, targetRTT float64) {
		t.Helper()
		for _, g := range []struct {
			name      string
			got, want float64
		}{
			{"wanwatch_probe_rtt_seconds", readGauge(t, mreg.ProbeRTT.WithLabelValues("primary", "v4")), rtt},
			{"wanwatch_probe_jitter_seconds", readGauge(t, mreg.ProbeJitter.WithLabelValues("primary", "v4")), jitter},
			{"wanwatch_probe_loss_ratio", readGauge(t, mreg.ProbeLoss.WithLabelValues("primary", "v4")), loss},
			{"wanwatch_probe_target_rtt_seconds", readGauge(t, mreg.ProbeTargetRTT.WithLabelValues("primary", "v4", "192.0.2.1")), targetRTT},
		} {
			if g.got != g.want && !(math.IsNaN(g.got) && math.IsNaN(g.want)) {
				t.Errorf("%s: %s = %v, want %v", step, g.name, g.got, g.want)
			}
		}
	}
	nan := math.NaN()

	assertProbeGauges("startup", nan, nan, nan, nan)

	w.Link(*up("eth0").link)
	w.Probe(probe.ProbeResult{Wan: "primary", Family: v4, Stats: probe.FamilyStats{
		RTTMicros: ptr[uint64](12_000), JitterMicros: ptr[uint64](800), LossRatio: ptr(0.5),
		PerTarget: []probe.TargetStats{{Target: "192.0.2.1", RTTMicros: ptr[uint64](12_000)}},
	}})
	assertProbeGauges("partial Window", 0.012, 0.0008, 0.5, 0.012)
	if v := familyHealthy(); v != 0 {
		t.Errorf("partial Window: wanwatch_wan_family_healthy = %v, want 0", v)
	}
	if v := wanHealthy(); v != 1 {
		t.Errorf("partial Window: wanwatch_wan_healthy = %v, want 1 (carrier-only Health)", v)
	}

	w.Probe(*good("primary", v4).probe)
	if v := familyHealthy(); v != 1 {
		t.Errorf("good Window: wanwatch_wan_family_healthy = %v, want 1", v)
	}
	w.Link(*down("eth0").link)
	if v := wanHealthy(); v != 0 {
		t.Errorf("carrier down: wanwatch_wan_healthy = %v, want 0", v)
	}
	if v := familyHealthy(); v != 1 {
		t.Errorf("carrier down: wanwatch_wan_family_healthy = %v, want 1 (carrier is not a Family verdict)", v)
	}
	w.Link(*up("eth0").link)
	w.Probe(probe.ProbeResult{Wan: "primary", Family: v4, Stats: probe.FamilyStats{
		LossRatio: ptr(1.0), WindowFilled: true,
		PerTarget: []probe.TargetStats{{Target: "192.0.2.1", LossRatio: ptr(1.0)}},
	}})
	assertProbeGauges("fully lost Window", nan, nan, 1, nan)
	if v := familyHealthy(); v != 0 {
		t.Errorf("bad Window: wanwatch_wan_family_healthy = %v, want 0", v)
	}
	if v := wanHealthy(); v != 0 {
		t.Errorf("bad Window: wanwatch_wan_healthy = %v, want 0", v)
	}
}

// TestSnapshot pins the state.Wan view: link state, WAN Health, and per
// Family the measured verdict, the latest Window statistics (even from
// a partial Window), and the Targets. A statistic without data, such as
// the RTT of a fully lost Window or every statistic before the first
// result, is nil. Gateways are left to the caller.
func TestSnapshot(t *testing.T) {
	t.Parallel()
	v4, v6 := probe.FamilyV4, probe.FamilyV6
	cfg := wanCfg("primary", "eth0", 1, 1, v4, v6)
	cfg.Probe.FamilyHealthPolicy = "any"
	w := New(wans(cfg, wanCfg("backup", "eth1", 1, 1, v4)), metrics.New())
	w.Link(*up("eth0").link)
	w.Probe(*bad("primary", v4).probe)
	w.Probe(probe.ProbeResult{Wan: "primary", Family: v6, Stats: probe.FamilyStats{
		RTTMicros: ptr[uint64](12_500), JitterMicros: ptr[uint64](800), LossRatio: ptr(0.05),
	}})

	want := map[string]state.Wan{
		"primary": {
			Interface: "eth0", Carrier: "up", Operstate: "up", Healthy: true,
			Families: map[string]state.FamilyHealth{
				"v4": {Healthy: false, LossRatio: ptr(1.0), Targets: []string{"192.0.2.1"}},
				"v6": {Healthy: false, RTTSeconds: ptr(0.0125), JitterSeconds: ptr(0.0008), LossRatio: ptr(0.05), Targets: []string{"2001:db8::1"}},
			},
		},
		"backup": {
			Interface: "eth1", Carrier: "unknown", Operstate: "unknown", Healthy: false,
			Families: map[string]state.FamilyHealth{
				"v4": {Targets: []string{"192.0.2.1"}},
			},
		},
	}
	if got := w.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("Snapshot() =\n%+v\nwant\n%+v", got, want)
	}
}

// TestMemberHealth pins the selector's input: one entry per Member,
// sorted by WAN name, carrying that WAN's Health. A Member naming an
// unknown WAN is unhealthy rather than a panic.
func TestMemberHealth(t *testing.T) {
	t.Parallel()
	w := New(wans(
		wanCfg("primary", "eth0", 1, 1, probe.FamilyV4),
		wanCfg("backup", "eth1", 1, 1, probe.FamilyV4),
	), metrics.New())
	w.Link(*up("eth1").link)
	g := selector.Group{Name: "home", Members: map[string]selector.Member{
		"primary": {Wan: "primary", Priority: 1},
		"ghost":   {Wan: "ghost", Priority: 2},
		"backup":  {Wan: "backup", Priority: 3},
	}}
	want := []selector.MemberHealth{
		{Member: selector.Member{Wan: "backup", Priority: 3}, Healthy: true},
		{Member: selector.Member{Wan: "ghost", Priority: 2}, Healthy: false},
		{Member: selector.Member{Wan: "primary", Priority: 1}, Healthy: false},
	}
	if got := w.MemberHealth(g); !reflect.DeepEqual(got, want) {
		t.Errorf("MemberHealth() = %+v, want %+v", got, want)
	}
}

// TestUnknownInputsChangeNothing: results for an unknown WAN or an
// unprobed Family, and link events on an unwatched interface, are
// dropped without a Change or a new WAN.
func TestUnknownInputsChangeNothing(t *testing.T) {
	t.Parallel()
	w := New(wans(wanCfg("primary", "eth0", 1, 1, probe.FamilyV4)), metrics.New())
	replay(t, w, []step{
		{event: up("eth0"), change: flipped("primary")},
		{event: bad("ghost", probe.FamilyV4), change: noChange, healthy: map[string]bool{"ghost": false}},
		{event: bad("primary", probe.FamilyV6), change: noChange, healthy: map[string]bool{"primary": true}},
		{event: down("eth9"), change: noChange, healthy: map[string]bool{"primary": true}},
	})
	if _, ok := w.Snapshot()["ghost"]; ok {
		t.Error("Snapshot() gained a WAN from an unknown result")
	}
}
