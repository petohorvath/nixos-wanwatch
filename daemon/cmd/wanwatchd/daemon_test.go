package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	d.wans["primary"].healthy = true
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
			Targets: []string{"1.1.1.1"},
			Thresholds: config.Thresholds{
				LossPctUp: 10, LossPctDown: 20,
				RttMsUp: 100, RttMsDown: 200,
			},
			Hysteresis: config.Hysteresis{ConsecutiveUp: 1, ConsecutiveDown: 1},
		},
	}
	d := testDaemon(t, cfg)
	// Cold-start defaults `healthy = true`; assert we're starting there.
	if !d.wans["primary"].healthy {
		t.Fatalf("precondition: primary.healthy = false, want true at cold-start")
	}

	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats:  probe.FamilyStats{LossRatio: 0.95, RTTMicros: 50_000},
	})

	if d.wans["primary"].families[probe.FamilyV4].healthy {
		t.Error("primary/v4 still healthy after high-loss probe")
	}
	if d.wans["primary"].healthy {
		t.Error("primary aggregate still healthy after high-loss probe")
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

// markHealthy sets carrier + operstate + healthy on every named
// WAN so buildMemberHealth votes them in. Tests that exercise
// recomputeGroup need this — newDaemon's cold-start leaves carrier
// at CarrierUnknown, which collapses to false in carrierUp().
func markHealthy(d *daemon, wans ...string) {
	for _, name := range wans {
		ws := d.wans[name]
		ws.carrier = rtnl.CarrierUp
		ws.operstate = rtnl.OperstateUp
		ws.healthy = true
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
	// Sicken primary. Carrier still up, but probes failed.
	d.wans["primary"].healthy = false
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
	d.wans["primary"].healthy = false
	d.wans["backup"].healthy = false
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
	d.runHooks(g, selector.NoActive, selector.Active{Wan: "primary", Has: true})

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
	d.runHooks(d.groups["home"], active, active)

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
	d.runHooks(d.groups["home"], selector.NoActive,
		selector.Active{Wan: "primary", Has: true})
	// No assertion needed — the test fails by panicking if runHooks
	// gets the error contract wrong. Reaching here is the success
	// condition.
}

// applyAttempts sums every counter inside applyRoutes that could
// fire on any path — runners differ in whether `eth0` exists and
// whether the daemon has CAP_NET_ADMIN, so a single-counter
// assertion would be too brittle. As long as one of these
// advanced, applyRoutes was called.
func applyAttempts(t *testing.T, d *daemon, group string) float64 {
	t.Helper()
	op := readCounter(t, d.metrics.ApplyOpErrors.WithLabelValues(group, "rule_install"))
	v4 := readCounter(t, d.metrics.ApplyRouteErrors.WithLabelValues(group, "v4"))
	v6 := readCounter(t, d.metrics.ApplyRouteErrors.WithLabelValues(group, "v6"))
	return op + v4 + v6
}

// TestHandleRouteEventReappliesOnActiveIface: when a RouteEvent
// arrives for the iface of the active member, applyRoutes must
// fire. We can't observe the netlink call directly without a test
// seam, so we proxy on the apply-error counters — at least one
// will advance because every applyRoutes path under a `nix develop`
// sandbox lacks CAP_NET_ADMIN.
func TestHandleRouteEventReappliesOnActiveIface(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")
	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)
	if g.active.Wan != "primary" {
		t.Fatalf("setup: want primary active, got %+v", g.active)
	}

	before := applyAttempts(t, d, "home")

	d.handleRouteEvent(t.Context(), rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "eth0", // primary's iface — must trigger reapply
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("192.0.2.1"),
	})

	if after := applyAttempts(t, d, "home"); after <= before {
		t.Errorf("no apply counter advanced: %v → %v (active-WAN reapply branch not entered)", before, after)
	}
}

// TestHandleRouteEventSkipsInactiveIface: a RouteEvent on an iface
// not used by any active member should populate the cache but
// not trigger any apply attempt.
func TestHandleRouteEventSkipsInactiveIface(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	markHealthy(d, "primary", "backup")
	g := d.groups["home"]
	d.recomputeGroup(t.Context(), g, reasonHealth)

	before := applyAttempts(t, d, "home")

	d.handleRouteEvent(t.Context(), rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "lo", // not used by any WAN
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("127.0.0.1"),
	})

	if after := applyAttempts(t, d, "home"); after != before {
		t.Errorf("apply counters advanced on inactive-iface event: %v → %v", before, after)
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
// family going healthy doesn't change the aggregate (it was
// already healthy via cold-start). The path still republishes
// state.json so external scrape readers see the per-family slot
// flip — that branch was the previously-uncovered tail of
// handleProbeResult.
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
			Targets:            []string{"1.1.1.1", "2606:4700:4700::1111"},
			Thresholds:         config.Thresholds{LossPctUp: 10, LossPctDown: 20, RttMsUp: 100, RttMsDown: 200},
			Hysteresis:         config.Hysteresis{ConsecutiveUp: 1, ConsecutiveDown: 1},
			FamilyHealthPolicy: "any",
		},
	}
	d := testDaemon(t, cfg)

	stateBefore, _ := os.Stat(d.cfg.Global.StatePath)

	d.handleProbeResult(t.Context(), probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats:  probe.FamilyStats{LossRatio: 0.95, RTTMicros: 50_000},
	})

	if !d.wans["primary"].healthy {
		t.Error("aggregate flipped under `any` despite v6 still uncooked")
	}
	// Republish should have happened: state.json modtime advances,
	// OR (cold path) state.json now exists.
	stateAfter, err := os.Stat(d.cfg.Global.StatePath)
	if err != nil {
		t.Fatalf("state.json missing after republish branch: %v", err)
	}
	if stateBefore != nil && !stateAfter.ModTime().After(stateBefore.ModTime()) {
		// Modtime may equal if the test runs within the FS's mtime
		// granularity. Be lenient — the file existing post-call is
		// the load-bearing assertion.
		t.Logf("state.json modtime didn't advance (within FS granularity?)")
	}
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
