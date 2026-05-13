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
		next selector.Active
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
			got := string(hookEventFor(tc.old, tc.next))
			if got != tc.want {
				t.Errorf("hookEventFor(%+v,%+v) = %q, want %q", tc.old, tc.next, got, tc.want)
			}
		})
	}
}

func TestGroupContainsWAN(t *testing.T) {
	t.Parallel()
	g := selector.Group{
		Name: "home",
		Members: []selector.Member{
			{Wan: "primary"},
			{Wan: "backup"},
		},
	}
	cases := []struct {
		name string
		wan  string
		want bool
	}{
		{"first member", "primary", true},
		{"second member", "backup", true},
		{"non-member", "ghost", false},
		{"empty string", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := groupContainsWAN(g, tc.wan); got != tc.want {
				t.Errorf("groupContainsWAN(_, %q) = %v, want %v", tc.wan, got, tc.want)
			}
		})
	}
}

func TestGroupContainsWANEmptyGroup(t *testing.T) {
	t.Parallel()
	if groupContainsWAN(selector.Group{Name: "empty"}, "anything") {
		t.Error("groupContainsWAN on empty group returned true")
	}
}

// TestCombineFamiliesEmptyReturnsFalse: a WAN with zero probed
// families isn't a useful runtime state, but the function is the
// last line of defence — degenerate input must yield "unhealthy",
// never panic or return true by default.
func TestCombineFamiliesEmptyReturnsFalse(t *testing.T) {
	t.Parallel()
	cases := []string{"all", "any", "weird-policy"}
	for _, policy := range cases {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()
			if combineFamilies(map[probe.Family]*familyState{}, policy) {
				t.Errorf("combineFamilies(empty, %q) = true, want false", policy)
			}
		})
	}
}

// TestCombineFamiliesNilEntry: a nil *familyState in the map must
// be skipped (defensive guard), not crash.
func TestCombineFamiliesNilEntry(t *testing.T) {
	t.Parallel()
	fams := map[probe.Family]*familyState{
		probe.FamilyV4: nil,
		probe.FamilyV6: {family: probe.FamilyV6, healthy: true, cooked: true},
	}
	if !combineFamilies(fams, "any") {
		t.Error("combineFamilies({nil,v6-healthy}, any) = false, want true (nil skipped, v6 wins)")
	}
}

// TestCombineFamiliesPolicyMatrix is the exhaustive policy ×
// (per-family health × cooked) table. Encodes the v1 contract
// explicitly so a refactor that, say, accidentally inverts the
// cold-start cooked-false-counts-as-healthy rule turns this red
// in a way the per-package coverage % never could.
//
// Cold-start cooked=false counts as healthy (PLAN §8: "health
// unknown but carrier known → trust carrier"). Both families
// cooked=false with carrier-up is a healthy WAN under either
// policy.
func TestCombineFamiliesPolicyMatrix(t *testing.T) {
	t.Parallel()
	mkFam := func(cooked, healthy bool) *familyState {
		return &familyState{cooked: cooked, healthy: healthy}
	}
	cases := []struct {
		name   string
		v4     *familyState
		v6     *familyState
		policy string
		want   bool
	}{
		// Cold-start defaults: cooked=false counts as healthy.
		{"cold v4, cold v6, all", mkFam(false, false), mkFam(false, false), "all", true},
		{"cold v4, cold v6, any", mkFam(false, false), mkFam(false, false), "any", true},

		// One cooked-healthy, one cold.
		{"cooked-healthy v4, cold v6, all", mkFam(true, true), mkFam(false, false), "all", true},
		{"cooked-healthy v4, cold v6, any", mkFam(true, true), mkFam(false, false), "any", true},

		// One cooked-unhealthy: "all" → false; "any" → still true via cold v6.
		{"cooked-unhealthy v4, cold v6, all", mkFam(true, false), mkFam(false, false), "all", false},
		{"cooked-unhealthy v4, cold v6, any", mkFam(true, false), mkFam(false, false), "any", true},

		// Both cooked.
		{"v4 up + v6 up, all", mkFam(true, true), mkFam(true, true), "all", true},
		{"v4 up + v6 up, any", mkFam(true, true), mkFam(true, true), "any", true},
		{"v4 up + v6 down, all", mkFam(true, true), mkFam(true, false), "all", false},
		{"v4 up + v6 down, any", mkFam(true, true), mkFam(true, false), "any", true},
		{"v4 down + v6 down, all", mkFam(true, false), mkFam(true, false), "all", false},
		{"v4 down + v6 down, any", mkFam(true, false), mkFam(true, false), "any", false},

		// Unknown policy defaults to "all" (the conservative pick).
		{"v4 up + v6 down, unknown policy", mkFam(true, true), mkFam(true, false), "", false},
		{"v4 up + v6 down, garbage policy", mkFam(true, true), mkFam(true, false), "weird", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fams := map[probe.Family]*familyState{
				probe.FamilyV4: tc.v4,
				probe.FamilyV6: tc.v6,
			}
			if got := combineFamilies(fams, tc.policy); got != tc.want {
				t.Errorf("combineFamilies(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestBuildMemberHealthGatesByCarrier documents the load-bearing
// `healthy = ok && w.carrierUp() && w.healthy` predicate: probes
// reporting healthy do NOT count when carrier is down. This is
// what makes carrier-down events drive failover instantly without
// waiting for probe timeouts.
func TestBuildMemberHealthGatesByCarrier(t *testing.T) {
	t.Parallel()
	wans := map[string]*wanState{
		"primary": {
			carrier:   rtnl.CarrierDown,
			operstate: rtnl.OperstateDown,
			healthy:   true, // probes say healthy
		},
		"backup": {
			carrier:   rtnl.CarrierUp,
			operstate: rtnl.OperstateUp,
			healthy:   true,
		},
	}
	g := selector.Group{
		Members: []selector.Member{{Wan: "primary"}, {Wan: "backup"}},
	}
	got := buildMemberHealth(g, wans)
	want := map[string]bool{"primary": false, "backup": true}
	for _, m := range got {
		if want[m.Member.Wan] != m.Healthy {
			t.Errorf("member %q: healthy = %v, want %v (carrier+probe predicate broken)",
				m.Member.Wan, m.Healthy, want[m.Member.Wan])
		}
	}
}

// TestBuildMemberHealthHandlesMissingWan: a Member references a
// Wan absent from the runtime map. Members like this aren't
// theoretical — the daemon-config validator should reject them
// upstream, but the daemon doesn't trust its input. Missing WAN
// → unhealthy, no panic, no map insertion.
func TestBuildMemberHealthHandlesMissingWan(t *testing.T) {
	t.Parallel()
	g := selector.Group{
		Members: []selector.Member{{Wan: "ghost"}, {Wan: "primary"}},
	}
	wans := map[string]*wanState{
		"primary": {carrier: rtnl.CarrierUp, operstate: rtnl.OperstateUp, healthy: true},
	}
	got := buildMemberHealth(g, wans)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (ghost + primary)", len(got))
	}
	for _, m := range got {
		if m.Member.Wan == "ghost" && m.Healthy {
			t.Error("ghost member reported healthy despite missing wanState")
		}
	}
	if _, leaked := wans["ghost"]; leaked {
		t.Error("buildMemberHealth wrote a ghost entry into the wans map")
	}
}
