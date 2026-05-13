package main

import (
	"encoding/json"
	"net"
	"os"
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
