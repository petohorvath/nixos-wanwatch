package main

import (
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

var defaultThresholds = config.Thresholds{
	LossPctUp:   5,
	LossPctDown: 20,
	RttMsUp:     150,
	RttMsDown:   300,
}

func TestEvaluateThresholdsStaysHealthyInsideBand(t *testing.T) {
	t.Parallel()
	stats := probe.FamilyStats{LossRatio: 0.10, RTTMicros: 200_000}
	if !evaluateThresholds(true, stats, defaultThresholds) {
		t.Error("inside-band stats should keep currently-healthy verdict")
	}
}

func TestEvaluateThresholdsFlipsDownOnLoss(t *testing.T) {
	t.Parallel()
	stats := probe.FamilyStats{LossRatio: 0.30, RTTMicros: 100_000}
	if evaluateThresholds(true, stats, defaultThresholds) {
		t.Error("loss above LossPctDown should flip to unhealthy")
	}
}

func TestEvaluateThresholdsFlipsDownOnRTT(t *testing.T) {
	t.Parallel()
	stats := probe.FamilyStats{LossRatio: 0.0, RTTMicros: 500_000}
	if evaluateThresholds(true, stats, defaultThresholds) {
		t.Error("RTT above RttMsDown should flip to unhealthy")
	}
}

func TestEvaluateThresholdsStaysUnhealthyInsideBand(t *testing.T) {
	t.Parallel()
	stats := probe.FamilyStats{LossRatio: 0.10, RTTMicros: 200_000}
	if evaluateThresholds(false, stats, defaultThresholds) {
		t.Error("inside-band stats should not flip currently-unhealthy to healthy")
	}
}

func TestEvaluateThresholdsFlipsUpRequiresBothMetrics(t *testing.T) {
	t.Parallel()
	// Low loss but high RTT — not yet healthy.
	stats := probe.FamilyStats{LossRatio: 0.0, RTTMicros: 200_000}
	if evaluateThresholds(false, stats, defaultThresholds) {
		t.Error("low loss alone shouldn't flip unhealthy → healthy when RTT > RttMsUp")
	}
	// Both clear — flips up.
	stats = probe.FamilyStats{LossRatio: 0.02, RTTMicros: 100_000}
	if !evaluateThresholds(false, stats, defaultThresholds) {
		t.Error("both metrics below Up thresholds should flip → healthy")
	}
}

func TestCombineFamiliesAll(t *testing.T) {
	t.Parallel()
	healthyV4 := &familyState{cooked: true, healthy: true}
	healthyV6 := &familyState{cooked: true, healthy: true}
	unhealthyV6 := &familyState{cooked: true, healthy: false}

	if !combineFamilies(map[probe.Family]*familyState{
		probe.FamilyV4: healthyV4,
		probe.FamilyV6: healthyV6,
	}, "all") {
		t.Error("all healthy under 'all' should be true")
	}
	if combineFamilies(map[probe.Family]*familyState{
		probe.FamilyV4: healthyV4,
		probe.FamilyV6: unhealthyV6,
	}, "all") {
		t.Error("one unhealthy under 'all' should be false")
	}
}

func TestCombineFamiliesAny(t *testing.T) {
	t.Parallel()
	if !combineFamilies(map[probe.Family]*familyState{
		probe.FamilyV4: {cooked: true, healthy: true},
		probe.FamilyV6: {cooked: true, healthy: false},
	}, "any") {
		t.Error("one healthy under 'any' should be true")
	}
	if combineFamilies(map[probe.Family]*familyState{
		probe.FamilyV4: {cooked: true, healthy: false},
		probe.FamilyV6: {cooked: true, healthy: false},
	}, "any") {
		t.Error("none healthy under 'any' should be false")
	}
}

func TestCombineFamiliesUncookedIsHealthyVote(t *testing.T) {
	t.Parallel()
	// PLAN §8 cold-start: before the first probe sample lands,
	// the family contributes a healthy vote so a carrier-up rtnl
	// event can fire an initial Decision.
	if !combineFamilies(map[probe.Family]*familyState{
		probe.FamilyV4: {cooked: false},
	}, "all") {
		t.Error("uncooked family under 'all' should vote healthy")
	}
	// After the first sample lands, the cooked verdict takes over.
	if combineFamilies(map[probe.Family]*familyState{
		probe.FamilyV4: {cooked: true, healthy: false},
	}, "all") {
		t.Error("cooked-unhealthy family should override cold-start")
	}
}

func TestCombineFamiliesNoProbedReturnsFalse(t *testing.T) {
	t.Parallel()
	// No families with a Pinger ⇒ no signal ⇒ unhealthy. Otherwise
	// a WAN with no probe configured would silently be considered
	// up forever.
	if combineFamilies(map[probe.Family]*familyState{}, "all") {
		t.Error("no probed families should evaluate as unhealthy")
	}
}

func TestBuildMemberHealthMissingWANIsUnhealthy(t *testing.T) {
	t.Parallel()
	g := selector.Group{
		Name: "home",
		Members: []selector.Member{
			{Wan: "primary", Priority: 1},
		},
	}
	// `wans` is empty — selector must see the member as unhealthy
	// rather than crashing.
	h := buildMemberHealth(g, map[string]*wanState{})
	if len(h) != 1 || h[0].Healthy {
		t.Errorf("missing wan should produce !healthy; got %+v", h)
	}
}

func TestBuildMemberHealthHonorsCarrierAndProbeVerdict(t *testing.T) {
	t.Parallel()
	g := selector.Group{
		Name: "home",
		Members: []selector.Member{
			{Wan: "primary", Priority: 1},
		},
	}
	wans := map[string]*wanState{
		"primary": {
			name:      "primary",
			carrier:   rtnl.CarrierUp,
			operstate: rtnl.OperstateUp,
			healthy:   true,
		},
	}
	h := buildMemberHealth(g, wans)
	if len(h) != 1 || !h[0].Healthy {
		t.Errorf("carrier+probe both up should produce healthy; got %+v", h)
	}

	// Drop carrier — probe says healthy but carrier is down.
	wans["primary"].carrier = rtnl.CarrierDown
	h = buildMemberHealth(g, wans)
	if h[0].Healthy {
		t.Error("carrier-down must override probe healthy")
	}
}

func TestCarrierUpAcceptsCarrierUpWithUnknownOperstate(t *testing.T) {
	t.Parallel()
	// Dummy / loopback / some tunnel drivers (and the kernel 6.18+
	// dummy in particular) keep operstate at "unknown" — RFC 2863
	// explicitly permits it for virtual links. Carrier=Up alone
	// must be enough to count the link as ready, otherwise dummy-
	// based VM tests never see a Decision fire.
	w := &wanState{carrier: rtnl.CarrierUp, operstate: rtnl.OperstateUnknown}
	if !w.carrierUp() {
		t.Error("carrier=up + operstate=unknown should be ready")
	}
}

func TestCarrierUpAcceptsOperstateUpWithUnknownCarrier(t *testing.T) {
	t.Parallel()
	// Symmetric: some hardware drivers drive operstate before
	// IFF_LOWER_UP propagates. Operstate=Up alone must also be
	// enough — taking the earliest positive signal.
	w := &wanState{carrier: rtnl.CarrierUnknown, operstate: rtnl.OperstateUp}
	if !w.carrierUp() {
		t.Error("carrier=unknown + operstate=up should be ready")
	}
}

func TestCarrierUpRejectsCarrierDown(t *testing.T) {
	t.Parallel()
	// Carrier-down is authoritative: even if operstate claims Up,
	// the physical link is gone and the WAN must not be selected.
	w := &wanState{carrier: rtnl.CarrierDown, operstate: rtnl.OperstateUp}
	if w.carrierUp() {
		t.Error("carrier=down must override operstate=up")
	}
}

func TestCarrierUpRejectsOperstateDown(t *testing.T) {
	t.Parallel()
	// Admin-down (operstate=Down) must override carrier=Up — the
	// user has explicitly disabled the interface even though the
	// cable is live.
	w := &wanState{carrier: rtnl.CarrierUp, operstate: rtnl.OperstateDown}
	if w.carrierUp() {
		t.Error("operstate=down must override carrier=up")
	}
}

func TestCarrierUpRejectsBothUnknown(t *testing.T) {
	t.Parallel()
	// No positive signal seen → not ready. This is the initial
	// state before any rtnl event has been processed.
	w := &wanState{carrier: rtnl.CarrierUnknown, operstate: rtnl.OperstateUnknown}
	if w.carrierUp() {
		t.Error("both unknown should not be ready")
	}
}

func TestHookEventForMatrix(t *testing.T) {
	t.Parallel()
	primary := selector.Active{Wan: "primary", Has: true}
	backup := selector.Active{Wan: "backup", Has: true}

	cases := []struct {
		name string
		old  selector.Active
		new_ selector.Active
		want string
	}{
		{"down→up", selector.NoActive, primary, "up"},
		{"up→down", primary, selector.NoActive, "down"},
		{"primary→backup", primary, backup, "switch"},
		{"no change same", primary, primary, ""},
		{"no change both absent", selector.NoActive, selector.NoActive, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(hookEventFor(tc.old, tc.new_))
			if got != tc.want {
				t.Errorf("hookEventFor(%+v,%+v) = %q, want %q", tc.old, tc.new_, got, tc.want)
			}
		})
	}
}
