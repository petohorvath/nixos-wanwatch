package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// TestWriteStateSnapshotHappyPath proves the round-trip: the WAN
// Health view, merged with the daemon's Gateway cache, maps to a
// state.State and survives JSON serialization with the expected
// schema and per-WAN / per-Group fields.
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
	linkUp(d, "primary")
	// A partial Window: live statistics, Health still carrier-only.
	d.health.Probe(probe.ProbeResult{Wan: "primary", Family: probe.FamilyV4, Stats: probe.FamilyStats{
		RTTMicros:    12_500,
		JitterMicros: 800,
		LossRatio:    0.05,
	}})
	d.gateways.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))

	d.writeStateSnapshot(time.Time{})

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
	gw, ok := d.gateways.get("eth0", rtnl.RouteFamilyV4)
	if !ok || gw.String() != "198.51.100.1" {
		t.Errorf("eth0/v4 = (%v, %v), want (198.51.100.1, true)", gw, ok)
	}
}

// TestHandleRouteEventDelClearsCache: a Del clears the entry.
func TestHandleRouteEventDelClearsCache(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())
	d.gateways.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("198.51.100.1"))
	d.handleRouteEvent(t.Context(), rtnl.RouteEvent{
		Op:     rtnl.RouteEventDel,
		Iface:  "eth0",
		Family: rtnl.RouteFamilyV4,
	})
	if _, ok := d.gateways.get("eth0", rtnl.RouteFamilyV4); ok {
		t.Error("cache still has eth0/v4 after Del event")
	}
}

// TestHandleLinkEventSharedInterfaceAvoidsIntermediateDecisions proves
// that a single physical link transition is folded into every matching
// WAN before any Group is recomputed. Recomputing inside the WAN map
// iteration can otherwise publish a transient Selection through the
// not-yet-updated peer; repeated up/down cycles exercise both possible
// Go map iteration orders.
func TestHandleLinkEventSharedInterfaceAvoidsIntermediateDecisions(t *testing.T) {
	t.Parallel()
	cfg := testCfgWithGroup()
	cfg.Wans["backup"] = config.Wan{
		Name:      "backup",
		Interface: "eth0",
		Probe:     cfg.Wans["backup"].Probe,
	}
	d := testDaemon(t, cfg)
	g := d.groups["home"]

	for cycle := range 32 {
		before := g.Snapshot().DecisionsTotal
		d.handleLinkEvent(t.Context(), rtnl.LinkEvent{
			Name:      "eth0",
			Carrier:   rtnl.CarrierUp,
			Operstate: rtnl.OperstateUp,
		})
		if delta := g.Snapshot().DecisionsTotal - before; delta != 1 {
			t.Fatalf("cycle %d carrier-up produced %d Decisions, want 1", cycle, delta)
		}
		if g.Snapshot().Active == nil || *g.Snapshot().Active != "primary" {
			t.Fatalf("cycle %d carrier-up active = %+v, want primary", cycle, g.Snapshot().Active)
		}

		before = g.Snapshot().DecisionsTotal
		d.handleLinkEvent(t.Context(), rtnl.LinkEvent{
			Name:      "eth0",
			Carrier:   rtnl.CarrierDown,
			Operstate: rtnl.OperstateDown,
		})
		if delta := g.Snapshot().DecisionsTotal - before; delta != 1 {
			t.Fatalf("cycle %d carrier-down produced %d Decisions, want 1", cycle, delta)
		}
		if g.Snapshot().Active != nil {
			t.Fatalf("cycle %d carrier-down active = %+v, want none", cycle, g.Snapshot().Active)
		}
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
			Members: map[string]selector.Member{
				"primary": {Wan: "primary", Priority: 1},
				"backup":  {Wan: "backup", Priority: 2},
			},
		},
	}
	return cfg
}

// linkUp reports carrier and operstate up on each named WAN's
// interface straight to WAN Health, leaving Group recompute to the
// test. With no full Window yet the WAN is healthy: the cold-start
// state.
func linkUp(d *daemon, wans ...string) {
	for _, name := range wans {
		d.health.Link(rtnl.LinkEvent{
			Name:      d.cfg.Wans[name].Interface,
			Carrier:   rtnl.CarrierUp,
			Operstate: rtnl.OperstateUp,
		})
	}
}

// probeLost reports a fully lost full Window for every Family of each
// named WAN straight to WAN Health, which seeds those Families
// unhealthy. Group recompute is left to the test.
func probeLost(d *daemon, wans ...string) {
	for _, name := range wans {
		for _, fam := range d.cfg.Wans[name].Probe.Targets.Families() {
			d.health.Probe(probe.ProbeResult{Wan: name, Family: fam, Stats: probe.FamilyStats{
				LossRatio: 1, WindowFilled: true,
			}})
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
	linkUp(d, "primary", "backup")

	g := d.groups["home"]
	d.recomputeAffectedGroups(t.Context(), "primary", reasonHealth)

	if g.Snapshot().Active == nil || *g.Snapshot().Active != "primary" {
		t.Errorf("g.Snapshot().Active = %+v, want primary present", g.Snapshot().Active)
	}
	if g.Snapshot().DecisionsTotal != 1 {
		t.Errorf("decisionsTotal = %d, want 1", g.Snapshot().DecisionsTotal)
	}
	if g.Snapshot().ActiveSince == nil {
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

	g := d.cfg.Groups["home"]
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

	g := d.cfg.Groups["home"]
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

	g := d.cfg.Groups["home"]
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

	g := d.cfg.Groups["home"]
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
// the Group Decision module flushes the vacated WAN's conntrack
// entries; a cold-start "up" (nothing vacated) does not.
func TestRecomputeGroupSwitchFlushesConntrack(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	linkUp(d, "primary", "backup")

	var flushedIfaces []string
	d.interfaceAddrs = func(iface string) ([]net.IP, error) {
		flushedIfaces = append(flushedIfaces, iface)
		return []net.IP{net.ParseIP("192.0.2.1")}, nil
	}
	d.flushConntrack = func(context.Context, probe.Family, net.IP) (uint, error) {
		return 1, nil
	}

	// Cold start → primary: an "up", nothing vacated.
	d.recomputeAffectedGroups(t.Context(), "primary", reasonHealth)
	if len(flushedIfaces) != 0 {
		t.Errorf("conntrack resolve on cold-start up = %v, want none", flushedIfaces)
	}
	// primary sickens → switch to backup: primary's iface is flushed.
	probeLost(d, "primary")
	d.recomputeAffectedGroups(t.Context(), "primary", reasonHealth)
	if len(flushedIfaces) != 1 || flushedIfaces[0] != "eth0" {
		t.Errorf("conntrack resolve on switch = %v, want [eth0]", flushedIfaces)
	}
}

// TestRecomputeAffectedGroupsFansOut: a per-WAN health change
// drives recomputeAffectedGroups on every group containing that WAN, and
// only those groups. Builds two groups (home contains primary;
// guest contains backup only) and asserts the fan-out predicate
// fires correctly.
func TestRecomputeAffectedGroupsFansOut(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	cfg.Groups = map[string]selector.Group{
		"home": {
			Name: "home", Strategy: "primary-backup", Table: 100, Mark: 0x100,
			Members: map[string]selector.Member{
				"primary": {Wan: "primary", Priority: 1},
				"backup":  {Wan: "backup", Priority: 2},
			},
		},
		"guest": {
			Name: "guest", Strategy: "primary-backup", Table: 200, Mark: 0x200,
			Members: map[string]selector.Member{
				"backup": {Wan: "backup", Priority: 1},
			},
		},
	}
	d := testDaemon(t, cfg)
	linkUp(d, "primary", "backup")

	// `primary` only belongs to `home` — recomputing for primary
	// should leave guest untouched.
	d.recomputeAffectedGroups(t.Context(), "primary", reasonHealth)
	if d.groups["home"].Snapshot().Active == nil {
		t.Errorf("home should have an active after primary fanout; got %+v", d.groups["home"].Snapshot().Active)
	}
	if d.groups["guest"].Snapshot().DecisionsTotal != 0 {
		t.Errorf("guest.decisionsTotal = %d, want 0 (primary not a member)", d.groups["guest"].Snapshot().DecisionsTotal)
	}

	// `backup` belongs to both — touching it must fire both.
	d.recomputeAffectedGroups(t.Context(), "backup", reasonCarrier)
	if d.groups["guest"].Snapshot().DecisionsTotal != 1 {
		t.Errorf("guest.decisionsTotal = %d, want 1 after backup fanout", d.groups["guest"].Snapshot().DecisionsTotal)
	}
}

func TestNotifyHooksCapturesDecisionData(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	started := filepath.Join(d.cfg.Global.HooksDir, "started")
	release := filepath.Join(d.cfg.Global.HooksDir, "release")
	outFile := filepath.Join(d.cfg.Global.HooksDir, "captured.txt")
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "up.d"), "gate.sh",
		`touch `+started+`; while [ ! -e `+release+` ]; do sleep 0.01; done`)
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "switch.d"), "env.sh", `env > `+outFile)

	g := d.cfg.Groups["home"]
	primary := selector.Active{Wan: "primary", Has: true}
	backup := selector.Active{Wan: "backup", Has: true}
	d.notifyHooks(g, selector.NoActive, backup, time.Time{})
	waitForHookPath(t, started, 3*time.Second)

	d.gateways.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	d.gateways.set("eth0", rtnl.RouteFamilyV6, net.ParseIP("2001:db8::1"))
	d.gateways.set("wwan0", rtnl.RouteFamilyV4, net.ParseIP("198.51.100.1"))
	now := time.Date(2026, 9, 13, 9, 0, 0, 123, time.UTC)
	want := map[string]string{
		state.EnvEvent: "switch", state.EnvGroup: "home",
		state.EnvWanOld: "backup", state.EnvWanNew: "primary",
		state.EnvIfaceOld: "wwan0", state.EnvIfaceNew: "eth0",
		state.EnvGatewayV4Old: "198.51.100.1", state.EnvGatewayV4New: "192.0.2.1",
		state.EnvGatewayV6Old: "", state.EnvGatewayV6New: "2001:db8::1",
		state.EnvTable: strconv.Itoa(g.Table), state.EnvMark: strconv.Itoa(g.Mark),
		state.EnvTimestamp: now.Format(time.RFC3339Nano),
	}
	d.notifyHooks(g, backup, primary, now)
	// Change all sources while delivery is held behind the first event.
	g.Name, g.Table, g.Mark = "changed", 999, 999
	d.cfg.Groups["home"] = g
	primaryCfg, backupCfg := d.cfg.Wans["primary"], d.cfg.Wans["backup"]
	primaryCfg.Interface, backupCfg.Interface = "changed0", "changed1"
	primaryCfg.Probe.Targets.V6 = nil
	d.cfg.Wans["primary"], d.cfg.Wans["backup"] = primaryCfg, backupCfg
	d.gateways.clear("eth0", rtnl.RouteFamilyV4)
	d.gateways.clear("eth0", rtnl.RouteFamilyV6)
	d.gateways.clear("wwan0", rtnl.RouteFamilyV4)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d.hooks.Close()

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	env := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		env[key] = value
	}
	for key, value := range want {
		if got, ok := env[key]; !ok || got != value {
			t.Errorf("%s = %q (present: %t), want %q", key, got, ok, value)
		}
	}
	families := strings.Split(env[state.EnvFamilies], ",")
	slices.Sort(families)
	if !slices.Equal(families, []string{"v4", "v6"}) {
		t.Errorf("captured families = %v, want [v4 v6]", families)
	}
}

// TestNotifyHooksNoEventOnIdentical: same Active before/after → no
// event → notifyHooks bails before touching HooksDir. We assert by
// dropping a hook that would fail loudly if invoked, then proving
// it wasn't.
func TestNotifyHooksNoEventOnIdentical(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	sentinel := filepath.Join(d.cfg.Global.HooksDir, "ran.txt")
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "up.d"), "should-not-run.sh",
		`touch `+sentinel)

	active := selector.Active{Wan: "primary", Has: true}
	d.notifyHooks(d.cfg.Groups["home"], active, active, time.Time{})
	d.hooks.Close()

	if _, err := os.Stat(sentinel); err == nil {
		t.Error("hook fired on identical-active transition; want no event")
	}
}

// TestHandleRouteEventReappliesOnActiveIface: a RouteEvent for the
// active member's iface populates the gateway cache and reapplies —
// applyRoutes now has a v4 gateway to write, so writeRoute fires.
func TestHandleRouteEventReappliesOnActiveIface(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	linkUp(d, "primary", "backup")
	writes := countWrites(d)

	g := d.groups["home"]
	d.recomputeAffectedGroups(t.Context(), "primary", reasonHealth)
	if *g.Snapshot().Active != "primary" {
		t.Fatalf("setup: want primary active, got %+v", g.Snapshot().Active)
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
	linkUp(d, "primary", "backup")
	writes := countWrites(d)

	d.recomputeAffectedGroups(t.Context(), "primary", reasonHealth)

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

func waitForHookPath(t *testing.T, path string, deadline time.Duration) {
	t.Helper()
	until := time.Now().Add(deadline)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("path %s was not created within %s", path, deadline)
		}
		time.Sleep(10 * time.Millisecond)
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

// TestHandleProbeResultForwardsHealthChange pins the probe-path
// wiring: a WAN Health flip recomputes its Groups, a Family transition
// that leaves WAN Health alone republishes state.json without a
// Decision (docs/specs/daemon-state.md), and a partial Window does
// neither. Health rules themselves are tested in internal/health.
func TestHandleProbeResultForwardsHealthChange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		policy        string
		windowFilled  bool
		wantDecisions int
		wantActive    string
		wantPublished bool
	}{
		{"Health flip switches the Group", "all", true, 1, "backup", true},
		{"Family flip under any republishes only", "any", true, 0, "primary", true},
		{"partial Window changes nothing", "all", false, 0, "primary", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testCfgWithGroup()
			primary := cfg.Wans["primary"]
			primary.Probe.FamilyHealthPolicy = tc.policy
			cfg.Wans["primary"] = primary
			d := testDaemon(t, cfg)
			for _, iface := range []string{"eth0", "wwan0"} {
				d.handleLinkEvent(t.Context(), rtnl.LinkEvent{Name: iface, Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp})
			}
			g := d.groups["home"]
			decisions := g.Snapshot().DecisionsTotal
			published := readCounter(t, d.metrics.StatePublications)

			d.handleProbeResult(t.Context(), probe.ProbeResult{
				Wan: "primary", Family: probe.FamilyV4,
				Stats: probe.FamilyStats{LossRatio: 1, WindowFilled: tc.windowFilled},
			})

			if delta := g.Snapshot().DecisionsTotal - decisions; delta != tc.wantDecisions {
				t.Errorf("Decisions = +%d, want +%d", delta, tc.wantDecisions)
			}
			if active := g.Snapshot().Active; active == nil || *active != tc.wantActive {
				t.Errorf("active = %v, want %s", active, tc.wantActive)
			}
			if got := readCounter(t, d.metrics.StatePublications) > published; got != tc.wantPublished {
				t.Errorf("state.json republished = %v, want %v", got, tc.wantPublished)
			}
		})
	}
}

// TestEventLoopEndToEndFiresUpHook is a small but full-fat
// integration of the event loop: a real eventLoop goroutine, a
// real hook script, real channels. Sending a carrier-up event
// for the primary WAN must:
//
//  1. flow through eventLoop → handleLinkEvent
//  2. trigger recomputeAffectedGroups → recomputeAffectedGroups
//  3. produce a Selection with primary active (carrier up plus
//     uncooked families ⇒ healthy(), no probe sample needed)
//  4. invoke notifyHooks with EventUp
//  5. execute the hook script and write the expected env vars
//
// If any of those stops happening — e.g. a refactor accidentally
// short-circuits the link → Decision plumbing — the hook never
// produces its output file and this test times out.
func TestEventLoopEndToEndFiresUpHook(t *testing.T) {
	t.Parallel()
	cfg := testCfgWithGroup()
	ctx, cancel := context.WithCancel(t.Context())
	d := testDaemonWithContext(ctx, t, cfg)

	outFile := filepath.Join(d.cfg.Global.HooksDir, "e2e.txt")
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "up.d"), "notify.sh",
		`echo "$WANWATCH_GROUP|$WANWATCH_WAN_NEW|$WANWATCH_EVENT" > `+outFile)

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)

	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents, nil)
		close(loopDone)
	}()

	// Send the carrier-up event for primary. With carrier up and
	// families still uncooked, healthy() is true, so primary becomes
	// the active member → up event fires → hook runs.
	linkEvents <- rtnl.LinkEvent{Name: "eth0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp}

	// Poll for the hook's output file. Five seconds leaves room for
	// the single script to start and write on a busy runner.
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

func TestSlowHookDoesNotBlockWatchdogAck(t *testing.T) {
	t.Parallel()
	cfg := testCfgWithGroup()
	cfg.Global.HookTimeoutMs = 3000
	ctx, cancel := context.WithCancel(t.Context())
	d := testDaemonWithContext(ctx, t, cfg)

	started := filepath.Join(d.cfg.Global.HooksDir, "started")
	finished := filepath.Join(d.cfg.Global.HooksDir, "finished")
	release := filepath.Join(d.cfg.Global.HooksDir, "release")
	writeHook(t, filepath.Join(d.cfg.Global.HooksDir, "up.d"), "slow.sh",
		`touch `+started+`; while [ ! -e `+release+` ]; do sleep 0.01; done; touch `+finished)

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)
	challenges := make(chan watchdogChallenge)
	loopDone := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents, challenges)
		close(loopDone)
	}()
	defer func() {
		_ = os.WriteFile(release, nil, 0o600)
		cancel()
		select {
		case <-loopDone:
		case <-time.After(2 * time.Second):
			t.Error("eventLoop did not stop after cancellation")
		}
	}()

	linkEvents <- rtnl.LinkEvent{Name: "eth0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp}
	waitForHookPath(t, started, 2*time.Second)

	ack := make(chan struct{})
	select {
	case challenges <- watchdogChallenge{ack: ack}:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not accept watchdog challenge while hook was running")
	}
	select {
	case <-ack:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not acknowledge watchdog challenge while hook was running")
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release hook: %v", err)
	}
	waitForHookPath(t, finished, 2*time.Second)
	d.hooks.Close()
}

// TestApplyRoutesErrorContract: applyRoutes returns nil for a soft
// gateway-skip (intentional deferral, not a failure) but an error
// for every hard failure — unknown WAN, ifindex lookup, netlink
// write — and a failed write bumps the per-(group,family) counter.
func TestApplyRoutesErrorContract(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	g := d.cfg.Groups["home"]

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
	d.gateways.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
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
	d.gateways.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	d.gateways.set("eth0", rtnl.RouteFamilyV6, net.ParseIP("2001:db8::1"))
	var fams []probe.Family
	d.writeRoute = func(_ context.Context, r apply.DefaultRoute) error {
		fams = append(fams, r.Family)
		return nil
	}

	if err := d.applyRoutes(t.Context(), d.cfg.Groups["home"], "primary", probe.FamilyV4); err != nil {
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
	if err := d.applyRoutes(t.Context(), d.cfg.Groups["home"], "backup", probe.FamilyV6); err != nil {
		t.Errorf("applyRoutes(backup, v6) = %v, want nil (unprobed family is a no-op)", err)
	}
	if *writes != 0 {
		t.Errorf("writes = %d, want 0 (unprobed family must not call writeRoute)", *writes)
	}
}
