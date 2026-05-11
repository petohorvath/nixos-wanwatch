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
	healthyV4 := &familyState{healthy: true}
	healthyV6 := &familyState{healthy: true}
	unhealthyV6 := &familyState{healthy: false}

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
		probe.FamilyV4: {healthy: true},
		probe.FamilyV6: {healthy: false},
	}, "any") {
		t.Error("one healthy under 'any' should be true")
	}
	if combineFamilies(map[probe.Family]*familyState{
		probe.FamilyV4: {healthy: false},
		probe.FamilyV6: {healthy: false},
	}, "any") {
		t.Error("none healthy under 'any' should be false")
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

func TestHookEventForMatrix(t *testing.T) {
	t.Parallel()
	primary := "primary"
	backup := "backup"

	cases := []struct {
		name string
		old  *string
		new_ *string
		want string
	}{
		{"down→up", nil, &primary, "up"},
		{"up→down", &primary, nil, "down"},
		{"primary→backup", &primary, &backup, "switch"},
		{"no change same", &primary, &primary, ""},
		{"no change both nil", nil, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(hookEventFor(tc.old, tc.new_))
			if got != tc.want {
				t.Errorf("hookEventFor(%v,%v) = %q, want %q", tc.old, tc.new_, got, tc.want)
			}
		})
	}
}

func TestEqualStringPtr(t *testing.T) {
	t.Parallel()
	a := "x"
	b := "x"
	c := "y"
	cases := []struct {
		name string
		p, q *string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"left nil", nil, &a, false},
		{"right nil", &a, nil, false},
		{"same content", &a, &b, true},
		{"different", &a, &c, false},
	}
	for _, tc := range cases {
		if got := equalStringPtr(tc.p, tc.q); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
