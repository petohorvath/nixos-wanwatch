package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// TestWriteStateSnapshotHappyPath proves the round-trip: the
// daemon's in-memory state maps to a state.State and survives
// JSON serialization with the expected schema and per-WAN /
// per-Group fields.
func TestWriteStateSnapshotHappyPath(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	cfg.Groups = map[string]selector.Group{
		"home": {
			Name:     "home",
			Strategy: "primary-backup",
			Table:    100,
			Mark:     0x100,
		},
	}
	d := testDaemon(t, cfg)
	d.wans["primary"].carrier = rtnl.CarrierUp
	d.wans["primary"].operstate = rtnl.OperstateUp
	d.wans["primary"].families[probe.FamilyV4].healthy = true
	d.wans["primary"].families[probe.FamilyV4].stats = probe.FamilyStats{
		RTTMicros:    12_500,
		JitterMicros: 800,
		LossRatio:    0.05,
	}
	d.gateways.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))

	d.writeStateSnapshot()

	data, err := os.ReadFile(cfg.Global.StatePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got state.State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Schema != state.SchemaVersion {
		t.Errorf("Schema = %d, want %d", got.Schema, state.SchemaVersion)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero — Writer.Write should stamp it")
	}
	wan, ok := got.Wans["primary"]
	if !ok {
		t.Fatalf("Wans[primary] missing")
	}
	if !wan.Healthy {
		t.Error("Wans[primary].Healthy = false, want true")
	}
	if wan.Gateways.V4 != "192.0.2.1" {
		t.Errorf("Wans[primary].Gateways.V4 = %q, want 192.0.2.1", wan.Gateways.V4)
	}
	fh, ok := wan.Families["v4"]
	if !ok {
		t.Fatalf("Wans[primary].Families[v4] missing")
	}
	if fh.RTTSeconds != 0.0125 || fh.JitterSeconds != 0.0008 || fh.LossRatio != 0.05 {
		t.Errorf("FamilyHealth = %+v, want RTTSeconds=0.0125 JitterSeconds=0.0008 LossRatio=0.05", fh)
	}
	if _, ok := got.Groups["home"]; !ok {
		t.Fatalf("Groups[home] missing")
	}
}

// TestHandleProbeResultDrivesUnhealthy: a high-loss probe result
// for a healthy WAN should drive the family verdict to unhealthy
// once hysteresis settles, and the aggregate WAN.Healthy flips.
//
// Uses a 1-cycle hysteresis (consecutiveDown=1) so one bad sample
// is enough — the slow-default 3-of-3 would need a loop.
func TestHandleProbeResultDrivesUnhealthy(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	cfg.Wans["primary"] = config.Wan{
		Name:      "primary",
		Interface: "eth0",
		Probe: config.Probe{
			Targets: config.Targets{V4: []string{"1.1.1.1"}},
			Thresholds: config.Thresholds{
				LossPctUp: 10, LossPctDown: 20,
				RttMsUp: 100, RttMsDown: 200,
			},
			Hysteresis: config.Hysteresis{ConsecutiveUp: 1, ConsecutiveDown: 1},
		},
	}
	d := testDaemon(t, cfg)
	// markHealthy brings carrier up; with families still uncooked the
	// WAN starts healthy — the cold-start state we transition out of.
	markHealthy(d, "primary")
	if !d.wans["primary"].healthy() {
		t.Fatalf("precondition: primary not healthy at setup")
	}

	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats: probe.FamilyStats{
			LossRatio: 0.95, RTTMicros: 50_000,
			WindowFilled: true,
		},
	})

	if d.wans["primary"].families[probe.FamilyV4].healthy {
		t.Error("primary/v4 still healthy after high-loss probe")
	}
	if d.wans["primary"].healthy() {
		t.Error("primary aggregate still healthy after high-loss probe")
	}
}

// TestHandleProbeResultSeedsHysteresisNoColdStartFlap: a healthy
// WAN with consecutiveUp>1 must not flap during warm-up. The first
// ProbeResult seeds the hysteresis straight from the measured
// Health (PLAN §8) instead of ramping up from false — without the
// seed, a good first probe leaves the hysteresis verdict false
// until consecutiveUp probes in, briefly dropping a healthy WAN.
func TestHandleProbeResultSeedsHysteresisNoColdStartFlap(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	cfg.Wans["primary"] = config.Wan{
		Name:      "primary",
		Interface: "eth0",
		Probe: config.Probe{
			Targets: config.Targets{V4: []string{"1.1.1.1"}},
			Thresholds: config.Thresholds{
				LossPctUp: 10, LossPctDown: 20,
				RttMsUp: 100, RttMsDown: 200,
			},
			// consecutiveUp=3: pre-fix, a single good probe would not
			// flip the hysteresis healthy — it would ramp from false.
			Hysteresis: config.Hysteresis{ConsecutiveUp: 3, ConsecutiveDown: 3},
		},
	}
	d := testDaemon(t, cfg)
	markHealthy(d, "primary")

	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats: probe.FamilyStats{
			LossRatio: 0.0, RTTMicros: 10_000,
			WindowFilled: true,
		},
	})

	if !d.wans["primary"].families[probe.FamilyV4].healthy {
		t.Error("primary/v4 not healthy after a good first probe — hysteresis ramped instead of seeding")
	}
	if !d.wans["primary"].healthy() {
		t.Error("primary aggregate not healthy after a good first probe")
	}
}

// TestHandleProbeResultDefersSeedUntilWindowFilled: a partial-window
// first ProbeResult (the probe loop fires before the route has
// converged → Lost first sample) must not seed the hysteresis. The
// family stays uncooked, combineFamilies keeps the WAN healthy via
// carrier alone (PLAN §8), and no spurious health Decision fires.
// Once a full Window lands healthy, the seed fires and the family
// transitions in one shot — never down→up.
func TestHandleProbeResultDefersSeedUntilWindowFilled(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	cfg.Wans["primary"] = config.Wan{
		Name:      "primary",
		Interface: "eth0",
		Probe: config.Probe{
			Targets: config.Targets{V4: []string{"1.1.1.1"}},
			Thresholds: config.Thresholds{
				LossPctUp: 5, LossPctDown: 25,
				RttMsUp: 4000, RttMsDown: 5000,
			},
			// consecutiveUp/Down=2 mirrors the cold-start scenario;
			// a pre-fix ramp would dip the WAN for one cycle before
			// recovering, generating a down+up Decision pair.
			Hysteresis: config.Hysteresis{ConsecutiveUp: 2, ConsecutiveDown: 2},
		},
	}
	d := testDaemon(t, cfg)
	markHealthy(d, "primary")

	// Partial-window Lost first sample (route hasn't converged).
	// Without the gate, hysteresis would Seed unhealthy here.
	partialStats := probe.FamilyStats{
		LossRatio: 1.0,
		RTTMicros: 0,
		// WindowFilled deliberately omitted (zero == false).
	}
	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats:  partialStats,
	})
	fs := d.wans["primary"].families[probe.FamilyV4]
	if fs.cooked {
		t.Error("cooked = true after a partial-window Sample; want deferred")
	}
	if !d.wans["primary"].healthy() {
		t.Error("aggregate flipped unhealthy on a partial-window Lost sample; want carrier-only-healthy")
	}
	// Live stats absorbed even in the deferred branch — operators
	// rely on Prometheus showing real RTT/loss before hysteresis
	// has cooked, so fs.stats and the per-family probe metrics
	// must reflect the ProbeResult either way.
	if fs.stats.LossRatio != partialStats.LossRatio {
		t.Errorf("fs.stats.LossRatio = %v after partial probe, want %v (live stats not absorbed)",
			fs.stats.LossRatio, partialStats.LossRatio)
	}
	if v := readGauge(t, d.metrics.ProbeLoss.WithLabelValues("primary", "v4")); v != partialStats.LossRatio {
		t.Errorf("wanwatch_probe_loss_ratio = %v after partial probe, want %v",
			v, partialStats.LossRatio)
	}

	// First full Window now lands healthy. Seed adopts it.
	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats: probe.FamilyStats{
			LossRatio: 0.0, RTTMicros: 10_000,
			WindowFilled: true,
		},
	})
	if !d.wans["primary"].families[probe.FamilyV4].cooked {
		t.Error("cooked = false after a full-Window seed")
	}
	if !d.wans["primary"].families[probe.FamilyV4].healthy {
		t.Error("family not healthy after a healthy full-Window seed — Seed didn't fire on first filled Window")
	}
}

// TestColdStartCookHealthyDoesNotBumpDecisions: PLAN §8 cold-start
// requires hysteresis to seed from the first healthy probe Window
// rather than ramp up from false, so a WAN that's already healthy
// via carrier-only never flaps during warm-up. recomputeGroup
// fires the carrier-up Decision once; the subsequent cook with a
// healthy raw verdict must not produce a second Decision. The VM
// cold-start scenario asserts this end-to-end; this is the
// unit-level pin against a refactor accidentally breaking the
// invariant the VM test depends on.
func TestColdStartCookHealthyDoesNotBumpDecisions(t *testing.T) {
	t.Parallel()
	cfg := testCfgWithGroup()
	// testCfg's default Thresholds are all zero, which would make
	// any non-zero RTT trip lossPctUp/rttMsUp and seed unhealthy.
	// Set realistic ones so a healthy probe really reads as healthy.
	cfg.Wans["primary"] = config.Wan{
		Name:      "primary",
		Interface: "eth0",
		Probe: config.Probe{
			Targets:    config.Targets{V4: []string{"1.1.1.1"}},
			Thresholds: config.Thresholds{LossPctUp: 5, LossPctDown: 25, RttMsUp: 4000, RttMsDown: 5000},
			Hysteresis: config.Hysteresis{ConsecutiveUp: 2, ConsecutiveDown: 2},
		},
	}
	d := testDaemon(t, cfg)
	markHealthy(d, "primary", "backup")

	g := d.groups["home"]
	// Carrier-up Decision lands primary as active. decisionsTotal=1.
	d.recomputeGroup(t.Context(), g, reasonCarrier)
	if g.decisionsTotal != 1 {
		t.Fatalf("setup: decisionsTotal = %d, want 1 (cold-start carrier Decision)", g.decisionsTotal)
	}
	if !g.active.Has || g.active.Wan != "primary" {
		t.Fatalf("setup: active = %+v, want primary", g.active)
	}

	// First full Window for v4 lands healthy. Cook fires;
	// fs.healthy goes true; ws.healthy() was already true via
	// cold-start carrier-only, stays true now via cooked-healthy.
	// No aggregate flip → no recomputeAffectedGroups call → no
	// Decision counter bump.
	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats: probe.FamilyStats{
			LossRatio: 0, RTTMicros: 10_000,
			WindowFilled: true,
		},
	})

	fs := d.wans["primary"].families[probe.FamilyV4]
	if !fs.cooked {
		t.Fatalf("post-cook precondition: family.cooked = false, want true")
	}
	if !fs.healthy {
		t.Fatalf("post-cook precondition: family.healthy = false, want true (Seed adopted raw=true)")
	}
	if g.decisionsTotal != 1 {
		t.Errorf("decisionsTotal = %d, want 1 — cold-start cook on a healthy Window must not produce a health Decision",
			g.decisionsTotal)
	}
}

// TestColdStartManyPartialWindowsThenCook: a cold-start that takes
// more than one probe cycle to fill the Window (e.g. multi-target
// probes where targets fill at different rates, or simply a config
// with windowSize > 1 — the common case) must keep the daemon in
// carrier-only mode through *every* partial-window ProbeResult, and
// only cook on the cycle that finally lands WindowFilled=true. Pins
// that the deferred branch is idempotent across an arbitrary ramp,
// not a one-shot no-op.
func TestColdStartManyPartialWindowsThenCook(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	cfg.Wans["primary"] = config.Wan{
		Name:      "primary",
		Interface: "eth0",
		Probe: config.Probe{
			Targets: config.Targets{V4: []string{"1.1.1.1"}},
			Thresholds: config.Thresholds{
				LossPctUp: 5, LossPctDown: 25,
				RttMsUp: 4000, RttMsDown: 5000,
			},
			Hysteresis: config.Hysteresis{ConsecutiveUp: 2, ConsecutiveDown: 2},
		},
	}
	d := testDaemon(t, cfg)
	markHealthy(d, "primary")

	// Three partial-window probes. All must defer; the daemon's
	// cold-start carrier-only health must hold through every one.
	// The LossRatio is varied so a buggy gate that triggered on
	// "raw verdict bad" rather than "window not filled" would
	// surface here.
	partials := []float64{1.0, 0.5, 0.2}
	for i, loss := range partials {
		d.handleProbeResult(t.Context(), probe.ProbeResult{
			Wan:    "primary",
			Family: probe.FamilyV4,
			Stats: probe.FamilyStats{
				LossRatio: loss,
				RTTMicros: 10_000,
				// WindowFilled deliberately omitted (zero == false).
			},
		})
		fs := d.wans["primary"].families[probe.FamilyV4]
		if fs.cooked {
			t.Fatalf("partial probe %d (loss=%v): cooked=true, want still uncooked", i, loss)
		}
		if !d.wans["primary"].healthy() {
			t.Fatalf("partial probe %d (loss=%v): aggregate not healthy via carrier-only cold-start", i, loss)
		}
	}

	// Fourth probe completes the Window, healthy. Cook fires.
	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats: probe.FamilyStats{
			LossRatio: 0, RTTMicros: 10_000,
			WindowFilled: true,
		},
	})
	fs := d.wans["primary"].families[probe.FamilyV4]
	if !fs.cooked {
		t.Error("after the filled Window: cooked=false, want true (gate should release)")
	}
	if !fs.healthy {
		t.Error("after the filled Window with raw=true: family.healthy=false, want true")
	}
}

// TestHandleRouteEventPopulatesGatewayCache: an Add RouteEvent
// populates the cache so subsequent applyRoutes can find a gw.
func TestHandleRouteEventPopulatesGatewayCache(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())
	d.handleRouteEvent(t.Context(), rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "eth0",
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("198.51.100.1"),
	})
	gw, ok := d.gateways.Get("eth0", rtnl.RouteFamilyV4)
	if !ok || gw.String() != "198.51.100.1" {
		t.Errorf("eth0/v4 = (%v, %v), want (198.51.100.1, true)", gw, ok)
	}
}

// TestHandleRouteEventDelClearsCache: a Del clears the entry.
func TestHandleRouteEventDelClearsCache(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())
	d.gateways.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("198.51.100.1"))
	d.handleRouteEvent(t.Context(), rtnl.RouteEvent{
		Op:     rtnl.RouteEventDel,
		Iface:  "eth0",
		Family: rtnl.RouteFamilyV4,
	})
	if _, ok := d.gateways.Get("eth0", rtnl.RouteFamilyV4); ok {
		t.Error("cache still has eth0/v4 after Del event")
	}
}

// TestUpdateGroupActiveGauge: the per-member `wanwatch_group_active`
// gauge must read 1 for the active member and 0 for every other
// member of the group. Drives the function directly because the
// pipeline that normally calls it (recomputeGroup) needs a
// healthy WAN + clean apply path to fire.
func TestUpdateGroupActiveGauge(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, &config.Config{
		Wans: testCfg().Wans,
		Groups: map[string]selector.Group{
			"home": {
				Name:     "home",
				Strategy: "primary-backup",
				Table:    100,
				Mark:     0x100,
				Members: []selector.Member{
					{Wan: "primary", Priority: 1},
					{Wan: "backup", Priority: 2},
				},
			},
		},
	})
	g := d.groups["home"]
	g.active = selector.Active{Wan: "primary", Has: true}

	d.updateGroupActiveGauge(g)

	// Verify by scraping the registry — that's the consumer
	// contract; reading the gauge directly via Prometheus's
	// internal types would bypass it.
	pri := readGauge(t, d.metrics.GroupActive.WithLabelValues("home", "primary"))
	bak := readGauge(t, d.metrics.GroupActive.WithLabelValues("home", "backup"))
	if pri != 1 {
		t.Errorf("active member primary: gauge = %v, want 1", pri)
	}
	if bak != 0 {
		t.Errorf("inactive member backup: gauge = %v, want 0", bak)
	}
}

// TestUpdateGroupActiveGaugeAbsentClearsAll: when no member is
// active (Selection.Has == false), every per-member gauge should
// read 0.
func TestUpdateGroupActiveGaugeAbsentClearsAll(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, &config.Config{
		Wans: testCfg().Wans,
		Groups: map[string]selector.Group{
			"home": {
				Name:     "home",
				Strategy: "primary-backup",
				Members: []selector.Member{
					{Wan: "primary", Priority: 1},
					{Wan: "backup", Priority: 2},
				},
			},
		},
	})
	g := d.groups["home"]
	// Seed: pretend primary was active, then clear it.
	g.active = selector.Active{Wan: "primary", Has: true}
	d.updateGroupActiveGauge(g)
	g.active = selector.NoActive
	d.updateGroupActiveGauge(g)

	if v := readGauge(t, d.metrics.GroupActive.WithLabelValues("home", "primary")); v != 0 {
		t.Errorf("primary after clear: gauge = %v, want 0", v)
	}
}

// testCfgWithGroup builds the same two-WAN cfg as testCfg() and
// attaches a single primary-backup group containing both members.
// Hand-rolled here rather than mutating testCfg() so the existing
// subscriber-level tests keep their slim config shape.
func testCfgWithGroup() *config.Config {
	cfg := testCfg()
	cfg.Groups = map[string]selector.Group{
		"home": {
			Name:     "home",
			Strategy: "primary-backup",
			Table:    100,
			Mark:     0x100,
			Members: []selector.Member{
				{Wan: "primary", Priority: 1},
				{Wan: "backup", Priority: 2},
			},
		},
	}
	return cfg
}

// markHealthy brings each named WAN's carrier + operstate up.
// newDaemon leaves carrier at CarrierUnknown (cold start), which
// carrierUp() reads as false; with carrier up and families still
// uncooked, wanState.healthy() is true — the cold-start state.
func markHealthy(d *daemon, wans ...string) {
	for _, name := range wans {
		ws := d.wans[name]
		ws.carrier = rtnl.CarrierUp
		ws.operstate = rtnl.OperstateUp
	}
}

// markUnhealthy cooks every family of each named WAN with a failed
// probe verdict. Carrier is left as-is — the cooked-unhealthy
// families collapse combineFamilies, so wanState.healthy() is
// false even with carrier up. The probe-side mirror of markHealthy.
func markUnhealthy(d *daemon, wans ...string) {
	for _, name := range wans {
		for _, fs := range d.wans[name].families {
			fs.cooked = true
			fs.healthy = false
		}
	}
}

// TestRecomputeGroupColdToPrimary: every member healthy, cold start
// (no active yet) → selector picks the lowest-priority member.
// Asserts state.json publishes the new active and the per-group
// `decisions_total` + `active{wan=primary}` metrics fire.
func TestRecomputeGroupColdToPrimary(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)

	if !g.active.Has || g.active.Wan != "primary" {
		t.Errorf("g.active = %+v, want primary present", g.active)
	}
	if g.decisionsTotal != 1 {
		t.Errorf("decisionsTotal = %d, want 1", g.decisionsTotal)
	}
	if g.activeSince == nil {
		t.Error("activeSince = nil, want non-nil on up transition")
	}
	if v := readGauge(t, d.metrics.GroupActive.WithLabelValues("home", "primary")); v != 1 {
		t.Errorf("group_active{primary} = %v, want 1", v)
	}
	// state.json should have been written.
	if _, err := os.Stat(d.cfg.Global.StatePath); err != nil {
		t.Errorf("state file not written: %v", err)
	}
}

// TestRecomputeGroupNoChange: a second recomputeGroup with the
// same input must not bump decisionsTotal — the change-detection
// guard at the top of the function is what keeps the metric
// honest under flap-free traffic.
func TestRecomputeGroupNoChange(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)
	first := g.decisionsTotal
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if g.decisionsTotal != first {
		t.Errorf("decisionsTotal advanced on no-op recompute: %d → %d", first, g.decisionsTotal)
	}
}

// TestRecomputeGroupSwitch: primary unhealthy, backup healthy →
// active flips from primary to backup; the prior primary gauge
// drops to 0 and the new active gauge reads 1.
func TestRecomputeGroupSwitch(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")

	g := d.groups["home"]
	// First decision: primary wins.
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if g.active.Wan != "primary" {
		t.Fatalf("setup: want primary active, got %+v", g.active)
	}
	// Sicken primary via failed probes — carrier stays up.
	markUnhealthy(d, "primary")
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if !g.active.Has || g.active.Wan != "backup" {
		t.Errorf("after primary failure: g.active = %+v, want backup", g.active)
	}
	if g.decisionsTotal != 2 {
		t.Errorf("decisionsTotal = %d, want 2 (cold→primary, primary→backup)", g.decisionsTotal)
	}
	if v := readGauge(t, d.metrics.GroupActive.WithLabelValues("home", "primary")); v != 0 {
		t.Errorf("primary gauge after switch = %v, want 0", v)
	}
	if v := readGauge(t, d.metrics.GroupActive.WithLabelValues("home", "backup")); v != 1 {
		t.Errorf("backup gauge after switch = %v, want 1", v)
	}
}

func TestFlushSwitchedConntrackFlushesVacatedWAN(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())

	var gotIface string
	d.interfaceAddrs = func(iface string) ([]net.IP, error) {
		gotIface = iface
		return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("2001:db8::1")}, nil
	}
	var flushed []probe.Family
	d.flushConntrack = func(_ context.Context, family probe.Family, _ net.IP) (uint, error) {
		flushed = append(flushed, family)
		return 3, nil
	}

	g := d.groups["home"]
	old := selector.Active{Wan: "primary", Has: true}
	next := selector.Active{Wan: "backup", Has: true}
	d.flushSwitchedConntrack(t.Context(), g, old, next)

	if gotIface != "eth0" {
		t.Errorf("interfaceAddrs called with %q, want eth0 (primary's iface)", gotIface)
	}
	if len(flushed) != 2 {
		t.Fatalf("flushConntrack calls = %d, want 2 (one per family)", len(flushed))
	}
	if flushed[0] != probe.FamilyV4 || flushed[1] != probe.FamilyV6 {
		t.Errorf("flushed families = %v, want [v4 v6]", flushed)
	}
}

func TestFlushSwitchedConntrackSkipsNonSwitch(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	called := false
	d.interfaceAddrs = func(string) ([]net.IP, error) {
		called = true
		return nil, nil
	}

	g := d.groups["home"]
	primary := selector.Active{Wan: "primary", Has: true}
	// down: a WAN is vacated but has no healthy successor — the old
	// route stays, so a flush would only churn.
	d.flushSwitchedConntrack(t.Context(), g, primary, selector.NoActive)
	// up: nothing was vacated.
	d.flushSwitchedConntrack(t.Context(), g, selector.NoActive, primary)

	if called {
		t.Error("interfaceAddrs called for a non-switch transition")
	}
}

func TestFlushSwitchedConntrackMetersResolveFailure(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	d.interfaceAddrs = func(string) ([]net.IP, error) {
		return nil, errors.New("flush test: no such interface")
	}
	flushCalled := false
	d.flushConntrack = func(context.Context, probe.Family, net.IP) (uint, error) {
		flushCalled = true
		return 0, nil
	}

	g := d.groups["home"]
	old := selector.Active{Wan: "primary", Has: true}
	next := selector.Active{Wan: "backup", Has: true}
	d.flushSwitchedConntrack(t.Context(), g, old, next)

	if flushCalled {
		t.Error("flushConntrack called despite address-resolution failure")
	}
	if got := readCounter(t, d.metrics.ApplyOpErrors.WithLabelValues("home", "conntrack_flush")); got != 1 {
		t.Errorf("ApplyOpErrors{home,conntrack_flush} = %v, want 1", got)
	}
}

func TestFlushSwitchedConntrackMetersFlushFailure(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	d.interfaceAddrs = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("2001:db8::1")}, nil
	}
	calls := 0
	d.flushConntrack = func(_ context.Context, family probe.Family, _ net.IP) (uint, error) {
		calls++
		if family == probe.FamilyV4 {
			return 0, errors.New("flush test: ENOMEM")
		}
		return 5, nil
	}

	g := d.groups["home"]
	old := selector.Active{Wan: "primary", Has: true}
	next := selector.Active{Wan: "backup", Has: true}
	d.flushSwitchedConntrack(t.Context(), g, old, next)

	// A failure on one family must not abort the next.
	if calls != 2 {
		t.Errorf("flushConntrack calls = %d, want 2 (v4 failed, v6 still attempted)", calls)
	}
	if got := readCounter(t, d.metrics.ApplyOpErrors.WithLabelValues("home", "conntrack_flush")); got != 1 {
		t.Errorf("ApplyOpErrors{home,conntrack_flush} = %v, want 1 (the v4 failure)", got)
	}
}

// TestRecomputeGroupSwitchFlushesConntrack: a switch driven through
// recomputeGroup → commitDecision flushes the vacated WAN's conntrack
// entries; a cold-start "up" (nothing vacated) does not.
func TestRecomputeGroupSwitchFlushesConntrack(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")

	var flushedIfaces []string
	d.interfaceAddrs = func(iface string) ([]net.IP, error) {
		flushedIfaces = append(flushedIfaces, iface)
		return []net.IP{net.ParseIP("192.0.2.1")}, nil
	}
	d.flushConntrack = func(context.Context, probe.Family, net.IP) (uint, error) {
		return 1, nil
	}

	g := d.groups["home"]
	// Cold start → primary: an "up", nothing vacated.
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if len(flushedIfaces) != 0 {
		t.Errorf("conntrack resolve on cold-start up = %v, want none", flushedIfaces)
	}
	// primary sickens → switch to backup: primary's iface is flushed.
	markUnhealthy(d, "primary")
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if len(flushedIfaces) != 1 || flushedIfaces[0] != "eth0" {
		t.Errorf("conntrack resolve on switch = %v, want [eth0]", flushedIfaces)
	}
}

// TestRecomputeAffectedGroupsFansOut: a per-WAN health change
// drives recomputeGroup on every group containing that WAN, and
// only those groups. Builds two groups (home contains primary;
// guest contains backup only) and asserts the fan-out predicate
// fires correctly.
func TestRecomputeAffectedGroupsFansOut(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	cfg.Groups = map[string]selector.Group{
		"home": {
			Name: "home", Strategy: "primary-backup", Table: 100, Mark: 0x100,
			Members: []selector.Member{
				{Wan: "primary", Priority: 1},
				{Wan: "backup", Priority: 2},
			},
		},
		"guest": {
			Name: "guest", Strategy: "primary-backup", Table: 200, Mark: 0x200,
			Members: []selector.Member{
				{Wan: "backup", Priority: 1},
			},
		},
	}
	d := testDaemon(t, cfg)
	markHealthy(d, "primary", "backup")

	// `primary` only belongs to `home` — recomputing for primary
	// should leave guest untouched.
	d.recomputeAffectedGroups(t.Context(), "primary", reasonHealth)
	if !d.groups["home"].active.Has {
		t.Errorf("home should have an active after primary fanout; got %+v", d.groups["home"].active)
	}
	if d.groups["guest"].decisionsTotal != 0 {
		t.Errorf("guest.decisionsTotal = %d, want 0 (primary not a member)", d.groups["guest"].decisionsTotal)
	}

	// `backup` belongs to both — touching it must fire both.
	d.recomputeAffectedGroups(t.Context(), "backup", reasonCarrier)
	if d.groups["guest"].decisionsTotal != 1 {
		t.Errorf("guest.decisionsTotal = %d, want 1 after backup fanout", d.groups["guest"].decisionsTotal)
	}
}

// TestRecomputeGroupAllUnhealthy: every member unhealthy → no
// Selection. Active.Has flips to false; the prior gauge clears.
func TestRecomputeGroupAllUnhealthy(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if !g.active.Has {
		t.Fatal("setup: expected primary active after first decision")
	}
	// Both go unhealthy.
	markUnhealthy(d, "primary", "backup")
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if g.active.Has {
		t.Errorf("g.active = %+v, want absent when all members unhealthy", g.active)
	}
	if v := readGauge(t, d.metrics.GroupActive.WithLabelValues("home", "primary")); v != 0 {
		t.Errorf("primary gauge after all-down = %v, want 0", v)
	}
}

// TestRunHooksUpEvent: drop an executable hook into HooksDir/up.d
// and assert the daemon's runHooks invokes it on an absent→present
// transition, with the right env vars populated.
func TestRunHooksUpEvent(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	// Mirror the env-capture pattern from internal/state/hooks_test.go.
	outFile := filepath.Join(d.cfg.Global.HooksDir, "captured.txt")
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "up.d"), "env.sh",
		`echo "$WANWATCH_EVENT|$WANWATCH_GROUP|$WANWATCH_WAN_NEW|$WANWATCH_IFACE_NEW" > `+outFile)

	g := d.groups["home"]
	d.runHooks(t.Context(), g, selector.NoActive, selector.Active{Wan: "primary", Has: true})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("hook didn't run (no output file): %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "up|home|primary|eth0"
	if got != want {
		t.Errorf("hook env capture = %q, want %q", got, want)
	}
}

// TestRunHooksNoEventOnIdentical: same Active before/after → no
// event → runHooks bails before touching HooksDir. We assert by
// dropping a hook that would fail loudly if invoked, then proving
// it wasn't.
func TestRunHooksNoEventOnIdentical(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	sentinel := filepath.Join(d.cfg.Global.HooksDir, "ran.txt")
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "up.d"), "should-not-run.sh",
		`touch `+sentinel)

	active := selector.Active{Wan: "primary", Has: true}
	d.runHooks(t.Context(), d.groups["home"], active, active)

	if _, err := os.Stat(sentinel); err == nil {
		t.Error("hook fired on identical-active transition; want no event")
	}
}

// TestRunHooksMissingDirIsNotError: no hook directory present →
// runHooks must finish quietly. The state.Runner already
// returns nil for ENOENT; we're pinning that the daemon layer
// doesn't add noise around it.
func TestRunHooksMissingDirIsNotError(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	// No writeHook → HooksDir/up.d/ doesn't exist.
	d.runHooks(t.Context(), d.groups["home"], selector.NoActive,
		selector.Active{Wan: "primary", Has: true})
	// No assertion needed — the test fails by panicking if runHooks
	// gets the error contract wrong. Reaching here is the success
	// condition.
}

// TestHandleRouteEventReappliesOnActiveIface: a RouteEvent for the
// active member's iface populates the gateway cache and reapplies —
// applyRoutes now has a v4 gateway to write, so writeRoute fires.
func TestHandleRouteEventReappliesOnActiveIface(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")
	writes := countWrites(d)

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if g.active.Wan != "primary" {
		t.Fatalf("setup: want primary active, got %+v", g.active)
	}

	before := *writes
	d.handleRouteEvent(t.Context(), rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "eth0", // primary's iface — must trigger reapply
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("192.0.2.1"),
	})

	if *writes <= before {
		t.Errorf("writeRoute calls %d → %d, want an increase (active-WAN reapply not entered)", before, *writes)
	}
}

// TestHandleRouteEventSkipsInactiveIface: a RouteEvent on an iface
// not used by any active member populates the cache but triggers no
// route write.
func TestHandleRouteEventSkipsInactiveIface(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")
	writes := countWrites(d)

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)

	before := *writes
	d.handleRouteEvent(t.Context(), rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "lo", // not used by any WAN
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("127.0.0.1"),
	})

	if *writes != before {
		t.Errorf("writeRoute called on inactive-iface event: %d → %d", before, *writes)
	}
}

// writeHook is the same shape as the one in internal/state/hooks_test
// — duplicated here because Go's test-helper sharing across packages
// would need exporting it from the production tree.
func writeHook(t *testing.T, eventDir, name, script string) {
	t.Helper()
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(eventDir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestHandleProbeResultUnknownWanNoOp: a result for a WAN the
// daemon has no state for must silently drop — catches a config-
// reload race where the prober has spun up but the daemon's WAN
// map hasn't.
func TestHandleProbeResultUnknownWanNoOp(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())
	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "ghost",
		Family: probe.FamilyV4,
		Stats:  probe.FamilyStats{LossRatio: 0.5},
	})
	// No panic, no metric pollution. Verify d.wans is untouched
	// (no ghost entry sneaked in).
	if _, ok := d.wans["ghost"]; ok {
		t.Error("handleProbeResult created a wanState for an unknown WAN")
	}
}

// TestHandleProbeResultUnknownFamilyNoOp: result for a (known
// WAN, unsupported family) — the family map is built from the
// declared probe.targets, so a result for a family the config
// didn't ask for is a programmer mistake, not a runtime case.
// Drop silently.
func TestHandleProbeResultUnknownFamilyNoOp(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	// backup is v4-only (target list has only "8.8.8.8") — so its
	// families map will not contain FamilyV6.
	d := testDaemon(t, cfg)
	if _, ok := d.wans["backup"].families[probe.FamilyV6]; ok {
		t.Fatal("setup: backup unexpectedly probes v6")
	}
	// This should be a no-op.
	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "backup",
		Family: probe.FamilyV6,
		Stats:  probe.FamilyStats{LossRatio: 0.0},
	})
}

// TestHandleProbeResultRepublishesOnFamilyFlipWithoutAggregate:
// for a dual-stack WAN under familyHealthPolicy=any, a single
// family going unhealthy doesn't move the aggregate (the other
// family is still healthy via cold-start). No Decision fires, but
// the per-family `healthy` field in state.json *does* change — so
// per PLAN §5.5 state.json is republished anyway: it mirrors live
// per-family Health, not just Decisions. Prior to the family-flip
// republish this case left state.json stale relative to the
// Prometheus view, and made it impossible to use state.json to
// answer "is family X probe-healthy yet?" before a Decision lands.
func TestHandleProbeResultRepublishesOnFamilyFlipWithoutAggregate(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	// any-policy: aggregate stays true as long as ≥1 family is
	// healthy. Cold-start gives both `cooked=false` ⇒ both count
	// as healthy ⇒ aggregate=true. A first ProbeResult with bad
	// stats flips v4's cooked=true (raw=false ⇒ stable=false), but
	// v6 is still uncooked → aggregate remains true under "any".
	cfg.Wans["primary"] = config.Wan{
		Name:      "primary",
		Interface: "eth0",
		Probe: config.Probe{
			Targets: config.Targets{
				V4: []string{"1.1.1.1"},
				V6: []string{"2606:4700:4700::1111"},
			},
			Thresholds:         config.Thresholds{LossPctUp: 10, LossPctDown: 20, RttMsUp: 100, RttMsDown: 200},
			Hysteresis:         config.Hysteresis{ConsecutiveUp: 1, ConsecutiveDown: 1},
			FamilyHealthPolicy: "any",
		},
	}
	d := testDaemon(t, cfg)
	markHealthy(d, "primary")

	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats: probe.FamilyStats{
			LossRatio: 0.95, RTTMicros: 50_000,
			WindowFilled: true,
		},
	})

	if !d.wans["primary"].healthy() {
		t.Error("aggregate flipped under `any` despite v6 still uncooked")
	}
	// No Decision fired, but the per-family verdict transitioned —
	// state.json must be rewritten so consumers see the live v4
	// health. testDaemon skips bootstrap, so the file's mere
	// existence proves the family-flip republish happened.
	if _, err := os.Stat(d.cfg.Global.StatePath); err != nil {
		t.Errorf("state.json not written on family flip: %v", err)
	}
}

// TestEventLoopEndToEndFiresUpHook is a small but full-fat
// integration of the event loop: a real eventLoop goroutine, a
// real hook script, real channels. Sending a carrier-up event
// for the primary WAN must:
//
//  1. flow through eventLoop → handleLinkEvent
//  2. trigger recomputeAffectedGroups → recomputeGroup
//  3. produce a Selection with primary active (carrier up plus
//     uncooked families ⇒ healthy(), no probe sample needed)
//  4. invoke runHooks with EventUp
//  5. execute the hook script and write the expected env vars
//
// If any of those stops happening — e.g. a refactor accidentally
// short-circuits the link → Decision plumbing — the hook never
// produces its output file and this test times out.
func TestEventLoopEndToEndFiresUpHook(t *testing.T) {
	t.Parallel()
	cfg := testCfgWithGroup()
	d := testDaemon(t, cfg)

	outFile := filepath.Join(d.cfg.Global.HooksDir, "e2e.txt")
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "up.d"), "notify.sh",
		`echo "$WANWATCH_GROUP|$WANWATCH_WAN_NEW|$WANWATCH_EVENT" > `+outFile)

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(loopDone)
	}()

	// Send the carrier-up event for primary. With carrier up and
	// families still uncooked, healthy() is true, so primary becomes
	// the active member → up event fires → hook runs.
	linkEvents <- rtnl.LinkEvent{Name: "eth0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp}

	// Poll for the hook's output file. The hook timeout cap is
	// `DefaultHookTimeout * maxHooksPerEvent` (5s × 8 = 40s); we
	// bound at 5s here — that's enough for a single shell script
	// to fork+write on any sane runner.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(outFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook did not run within 5s; outFile %s missing", outFile)
		}
		time.Sleep(20 * time.Millisecond)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("ReadFile(outFile): %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "home|primary|up"
	if got != want {
		t.Errorf("hook payload = %q, want %q", got, want)
	}

	cancel()
	<-loopDone
}

// TestRecordProbeMetricsEmptyPerTarget: cold start emits a
// FamilyStats with no per-target entries — the PerTarget loop
// must be a no-op (not crash, not emit a stale gauge with empty
// label).
func TestRecordProbeMetricsEmptyPerTarget(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())
	d.recordProbeMetrics(probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats:  probe.FamilyStats{LossRatio: 0, RTTMicros: 0, PerTarget: nil},
	}, false)
	// We mostly want "no panic"; assert one observable side
	// effect that should still happen even on empty PerTarget:
	// the WanFamilyHealthy gauge.
	v := readGauge(t, d.metrics.WanFamilyHealthy.WithLabelValues("primary", "v4"))
	if v != 0 {
		t.Errorf("WanFamilyHealthy(primary,v4) = %v, want 0", v)
	}
}

// TestCommitDecisionDefersOnApplyFailure: a hard apply failure
// records the Decision internally (decisionsTotal, pendingActive)
// but defers the visible effects — `active` stays absent and
// state.json is not written, so neither reports a switch the kernel
// hasn't made.
func TestCommitDecisionDefersOnApplyFailure(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")
	d.ifindexOf = failingIfindex

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)

	if g.decisionsTotal != 1 {
		t.Errorf("decisionsTotal = %d, want 1 (Decision recorded even on apply failure)", g.decisionsTotal)
	}
	if !g.applyPending || g.pendingActive.Wan != "primary" {
		t.Errorf("applyPending=%v pendingActive=%+v, want pending on primary", g.applyPending, g.pendingActive)
	}
	if g.active.Has {
		t.Errorf("g.active = %+v, want absent — apply failed, switch not converged", g.active)
	}
	if _, err := os.Stat(d.cfg.Global.StatePath); err == nil {
		t.Error("state.json written despite a failed apply — switch not converged")
	}
}

// TestRetryPendingApplyConverges: once apply starts succeeding, the
// next probe result for the pending WAN converges the Decision —
// promoting `active`, writing state.json, and firing the deferred
// up hook, none of which happened while the apply was failing.
func TestRetryPendingApplyConverges(t *testing.T) {
	t.Parallel()
	cfg := testCfgWithGroup()
	// Give primary real thresholds so the probe result below keeps it
	// healthy — the result is only here to *trigger* the retry, not
	// to change the health verdict.
	cfg.Wans["primary"] = config.Wan{
		Name:      "primary",
		Interface: "eth0",
		Probe: config.Probe{
			Targets: config.Targets{
				V4: []string{"1.1.1.1"},
				V6: []string{"2606:4700:4700::1111"},
			},
			Thresholds: config.Thresholds{LossPctUp: 10, LossPctDown: 20, RttMsUp: 100, RttMsDown: 200},
			Hysteresis: config.Hysteresis{ConsecutiveUp: 1, ConsecutiveDown: 1},
		},
	}
	d := testDaemon(t, cfg)
	markHealthy(d, "primary", "backup")

	sentinel := filepath.Join(d.cfg.Global.HooksDir, "up-ran.txt")
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "up.d"), "notify.sh",
		"touch "+sentinel)

	failing := true
	d.ifindexOf = func(string) (int, error) {
		if failing {
			return 0, errors.New("no such interface")
		}
		return 1, nil
	}

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if !g.applyPending {
		t.Fatalf("setup: want applyPending after a failed apply, got %+v", g)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("up hook fired while the Decision was still pending")
	}

	// Apply now succeeds; a probe result for the pending WAN retries.
	failing = false
	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats:  probe.FamilyStats{LossRatio: 0, RTTMicros: 10_000},
	})

	if g.applyPending {
		t.Error("still applyPending after a successful retry")
	}
	if !g.active.Has || g.active.Wan != "primary" {
		t.Errorf("g.active = %+v, want primary after the retry converged", g.active)
	}
	if _, err := os.Stat(d.cfg.Global.StatePath); err != nil {
		t.Errorf("state.json not written after the retry converged: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("deferred up hook did not fire after the retry converged: %v", err)
	}
}

// TestSupersedingDecisionWhilePending: a second Decision made while
// the first is still un-converged replaces pendingActive, bumps
// decisionsTotal, and — since nothing ever converged — leaves
// `active` absent so state/hooks never reported the dropped switch.
func TestSupersedingDecisionWhilePending(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")
	d.ifindexOf = failingIfindex

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if !g.applyPending || g.pendingActive.Wan != "primary" {
		t.Fatalf("decision 1: want pending on primary, got pending=%v active=%+v",
			g.applyPending, g.pendingActive)
	}

	// primary sickens — the selector now picks backup, superseding
	// the still-pending primary Decision.
	markUnhealthy(d, "primary")
	d.recomputeGroup(t.Context(), g, reasonHealth)

	if !g.applyPending || g.pendingActive.Wan != "backup" {
		t.Errorf("decision 2: want pending on backup, got pending=%v active=%+v",
			g.applyPending, g.pendingActive)
	}
	if g.decisionsTotal != 2 {
		t.Errorf("decisionsTotal = %d, want 2", g.decisionsTotal)
	}
	if g.active.Has {
		t.Errorf("g.active = %+v, want absent — no Decision ever converged", g.active)
	}
}

// TestApplyRoutesErrorContract: applyRoutes returns nil for a soft
// gateway-skip (intentional deferral, not a failure) but an error
// for every hard failure — unknown WAN, ifindex lookup, netlink
// write — and a failed write bumps the per-(group,family) counter.
func TestApplyRoutesErrorContract(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	g := d.groups["home"]

	// No gateway cached for primary (non-PtP) → every family is a
	// soft skip, which is not a failure.
	if err := d.applyRoutes(t.Context(), g, "primary"); err != nil {
		t.Errorf("applyRoutes with no gateways = %v, want nil (soft skip is not a failure)", err)
	}

	// Unknown WAN is a hard failure.
	if err := d.applyRoutes(t.Context(), g, "ghost"); err == nil {
		t.Error("applyRoutes for unknown wan = nil, want error")
	}

	// An ifindex lookup failure is a hard failure.
	d.ifindexOf = failingIfindex
	if err := d.applyRoutes(t.Context(), g, "primary"); err == nil {
		t.Error("applyRoutes with a failing ifindex lookup = nil, want error")
	}

	// A netlink write error is a hard failure, and bumps the
	// per-(group,family) error counter so the failure is observable.
	d.ifindexOf = func(string) (int, error) { return 1, nil }
	d.gateways.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	d.writeRoute = func(context.Context, apply.DefaultRoute) error {
		return errors.New("netlink: operation not permitted")
	}
	if err := d.applyRoutes(t.Context(), g, "primary"); err == nil {
		t.Error("applyRoutes with a failing route write = nil, want error")
	}
	if n := readCounter(t, d.metrics.ApplyRouteErrors.WithLabelValues("home", "v4")); n != 1 {
		t.Errorf("apply_route_errors_total{home,v4} = %v, want 1", n)
	}
}

func TestApplyRoutesExplicitFamilyWritesOnlyThat(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	// Seed both families' gateways so the filtered call has something
	// to write — otherwise both families would soft-skip and the
	// assertion below couldn't distinguish "filtered" from "skipped".
	d.gateways.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	d.gateways.Set("eth0", rtnl.RouteFamilyV6, net.ParseIP("2001:db8::1"))
	var fams []probe.Family
	d.writeRoute = func(_ context.Context, r apply.DefaultRoute) error {
		fams = append(fams, r.Family)
		return nil
	}

	if err := d.applyRoutes(t.Context(), d.groups["home"], "primary", probe.FamilyV4); err != nil {
		t.Fatalf("applyRoutes(primary, v4) = %v, want nil", err)
	}
	if len(fams) != 1 || fams[0] != probe.FamilyV4 {
		t.Errorf("written families = %v, want [v4]", fams)
	}
}

func TestApplyRoutesExplicitFamilySkipsUnprobed(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	writes := countWrites(d)
	// backup is v4-only in testCfg (its only target is 8.8.8.8), so
	// passing FamilyV6 must be a no-op — the daemon has no route to
	// maintain for an unprobed family.
	if err := d.applyRoutes(t.Context(), d.groups["home"], "backup", probe.FamilyV6); err != nil {
		t.Errorf("applyRoutes(backup, v6) = %v, want nil (unprobed family is a no-op)", err)
	}
	if *writes != 0 {
		t.Errorf("writes = %d, want 0 (unprobed family must not call writeRoute)", *writes)
	}
}

// TestHandleRouteEventRewritesOnlyEventFamily: a route event for one
// family rewrites that family's default route and not the other.
// RouteReplace is idempotent so a full rewrite would be harmless,
// but per-family halves the netlink syscall count under flap.
func TestHandleRouteEventRewritesOnlyEventFamily(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")

	// Both families have a cached gateway up front so the cold-start
	// commit's per-family writes can all land.
	d.gateways.Set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	d.gateways.Set("eth0", rtnl.RouteFamilyV6, net.ParseIP("2001:db8::1"))

	var written []probe.Family
	d.writeRoute = func(_ context.Context, r apply.DefaultRoute) error {
		written = append(written, r.Family)
		return nil
	}

	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if got := len(written); got != 2 {
		t.Fatalf("cold-start writes = %d (families %v), want 2 (both)", got, written)
	}

	// A v4 RouteEvent must rewrite only v4.
	cold := len(written)
	d.handleRouteEvent(t.Context(), rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "eth0",
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("192.0.2.2"), // distinct → changed=true
	})
	after := written[cold:]
	if len(after) != 1 {
		t.Fatalf("RouteEvent writes = %d (families %v), want 1 (just v4)", len(after), after)
	}
	if after[0] != probe.FamilyV4 {
		t.Errorf("RouteEvent rewrote family %v, want FamilyV4", after[0])
	}
}
