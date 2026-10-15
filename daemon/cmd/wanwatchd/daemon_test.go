package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if gw := gatewayText(wan.Gateways.V4); gw != "192.0.2.1" {
		t.Errorf("Wans[primary].Gateways.V4 = %s, want 192.0.2.1", gw)
	}
	if gw := gatewayText(wan.Gateways.V6); gw != "<nil>" {
		t.Errorf("Wans[primary].Gateways.V6 = %s, want <nil> (no v6 default route)", gw)
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
