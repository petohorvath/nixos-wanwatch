package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// testDaemon builds a daemon wired against `cfg` without invoking
// bootstrap() — netlink rule install isn't available in unit tests.
func testDaemon(t *testing.T, cfg *config.Config) *daemon {
	t.Helper()
	cfg.Global.StatePath = filepath.Join(t.TempDir(), "state.json")
	cfg.Global.HooksDir = t.TempDir()
	return newDaemon(cfg, metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func testCfg() *config.Config {
	return &config.Config{
		Wans: map[string]config.Wan{
			"primary": {
				Name:      "primary",
				Interface: "eth0",
				Probe: config.Probe{
					Targets: []string{"1.1.1.1", "2606:4700:4700::1111"},
				},
			},
			"backup": {
				Name:      "backup",
				Interface: "wwan0",
				// v4-only WAN — only a v4 probe target.
				Probe: config.Probe{
					Targets: []string{"8.8.8.8"},
				},
			},
		},
	}
}

func TestIdentKeysForFromProbeTargets(t *testing.T) {
	t.Parallel()
	keys := identKeysFor(testCfg())
	// Sorted by wan name: backup (v4) < primary (v4, v6) ⇒ 3 keys.
	want := []probe.IdentKey{
		{Wan: "backup", Family: probe.FamilyV4},
		{Wan: "primary", Family: probe.FamilyV4},
		{Wan: "primary", Family: probe.FamilyV6},
	}
	if len(keys) != len(want) {
		t.Fatalf("len = %d, want %d (keys=%+v)", len(keys), len(want), keys)
	}
	for i, w := range want {
		if keys[i] != w {
			t.Errorf("keys[%d] = %+v, want %+v", i, keys[i], w)
		}
	}
}

func TestIdentKeysForIsDeterministic(t *testing.T) {
	t.Parallel()
	// Map iteration is randomized but identKeysFor must produce a
	// stable order so the ident allocation is reproducible across
	// restarts (PLAN §8).
	a := identKeysFor(testCfg())
	b := identKeysFor(testCfg())
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("a[%d]=%+v b[%d]=%+v", i, a[i], i, b[i])
		}
	}
}

func TestEventLoopRoutesProbeResultToDaemon(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)
	probeResults <- probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats:  probe.FamilyStats{LossRatio: 0.5, RTTMicros: 12000},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := d.wans["primary"].families[probe.FamilyV4].stats.LossRatio; got != 0.5 {
		t.Errorf("primary v4 LossRatio = %v, want 0.5", got)
	}
}

func TestEventLoopRoutesLinkEventToDaemon(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)
	linkEvents <- rtnl.LinkEvent{
		Name:      "eth0",
		Carrier:   rtnl.CarrierUp,
		Operstate: rtnl.OperstateUp,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if d.wans["primary"].carrier != rtnl.CarrierUp {
		t.Errorf("primary carrier = %v, want up", d.wans["primary"].carrier)
	}
}

func TestEventLoopRoutesRouteEventToDaemon(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	probeResults := make(chan probe.ProbeResult, 1)
	linkEvents := make(chan rtnl.LinkEvent, 1)
	routeEvents := make(chan rtnl.RouteEvent, 1)
	routeEvents <- rtnl.RouteEvent{
		Op:      rtnl.RouteEventAdd,
		Iface:   "eth0",
		Family:  rtnl.RouteFamilyV4,
		Gateway: net.ParseIP("192.0.2.1"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	gw, ok := d.gateways.Get("eth0", rtnl.RouteFamilyV4)
	if !ok {
		t.Fatal("eth0/v4 gateway not in cache after RouteEvent")
	}
	if gw.String() != "192.0.2.1" {
		t.Errorf("cache eth0/v4 = %v, want 192.0.2.1", gw)
	}
}

func TestEventLoopReturnsOnCtxCancel(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	ctx, cancel := context.WithCancel(context.Background())
	probeResults := make(chan probe.ProbeResult)
	linkEvents := make(chan rtnl.LinkEvent)
	routeEvents := make(chan rtnl.RouteEvent)

	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not return within 1s of ctx cancel")
	}
}

func TestTargetsForFiltersByFamily(t *testing.T) {
	t.Parallel()
	wan := config.Wan{
		Probe: config.Probe{
			Targets: []string{"1.1.1.1", "2606:4700:4700::1111", "8.8.8.8", "not-an-ip"},
		},
	}

	v4 := targetsFor(wan, probe.FamilyV4)
	want4 := []string{"1.1.1.1", "8.8.8.8"}
	if !equalUnorderedStrings(v4, want4) {
		t.Errorf("targetsFor(v4) = %v, want %v", v4, want4)
	}

	v6 := targetsFor(wan, probe.FamilyV6)
	want6 := []string{"2606:4700:4700::1111"}
	if !equalUnorderedStrings(v6, want6) {
		t.Errorf("targetsFor(v6) = %v, want %v", v6, want6)
	}
}

func TestTargetsForEmpty(t *testing.T) {
	t.Parallel()
	wan := config.Wan{Probe: config.Probe{}}
	if got := targetsFor(wan, probe.FamilyV4); len(got) != 0 {
		t.Errorf("targetsFor on empty Targets = %v, want []", got)
	}
}

func TestTargetsForAllInvalid(t *testing.T) {
	t.Parallel()
	// Non-IP strings shouldn't crash and shouldn't be emitted.
	wan := config.Wan{Probe: config.Probe{Targets: []string{"not-ip", "also.not"}}}
	if got := targetsFor(wan, probe.FamilyV4); len(got) != 0 {
		t.Errorf("targetsFor on non-IP input = %v, want []", got)
	}
}

func TestWatchedInterfaces(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Wans: map[string]config.Wan{
			"primary": {Interface: "eth0"},
			"backup":  {Interface: "wwan0"},
		},
	}
	got := watchedInterfaces(cfg)
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (got %v)", len(got), got)
	}
	if _, ok := got["eth0"]; !ok {
		t.Error("eth0 missing from watched set")
	}
	if _, ok := got["wwan0"]; !ok {
		t.Error("wwan0 missing from watched set")
	}
}

func TestWatchedInterfacesCollapsesDuplicates(t *testing.T) {
	t.Parallel()
	// Two WANs on the same interface (not a useful config, but the
	// set-of-interfaces contract should collapse them to one entry).
	cfg := &config.Config{
		Wans: map[string]config.Wan{
			"alpha": {Interface: "eth0"},
			"beta":  {Interface: "eth0"},
		},
	}
	got := watchedInterfaces(cfg)
	if len(got) != 1 {
		t.Errorf("len = %d, want 1 (deduped to eth0); got %v", len(got), got)
	}
}

func TestWatchedInterfacesEmpty(t *testing.T) {
	t.Parallel()
	got := watchedInterfaces(&config.Config{})
	if len(got) != 0 {
		t.Errorf("empty cfg → %v, want empty map", got)
	}
}

// TestFamiliesFromTargetsSkipsNonIP: non-IP strings in the
// targets list are silently dropped — the config layer should
// have rejected them already, but the daemon doesn't trust its
// input and must not crash on garbage.
func TestFamiliesFromTargetsSkipsNonIP(t *testing.T) {
	t.Parallel()
	got := familiesFromTargets([]string{"not-an-ip", "1.1.1.1", "also.not"})
	if !got.v4 {
		t.Error("v4 = false; want true (1.1.1.1 should still count)")
	}
	if got.v6 {
		t.Error("v6 = true; want false (no v6 literal)")
	}
}

func TestFamiliesFromTargetsAllInvalid(t *testing.T) {
	t.Parallel()
	got := familiesFromTargets([]string{"", "abc", "256.256.256.256"})
	if got.v4 || got.v6 {
		t.Errorf("got = %+v, want both false (no valid IPs)", got)
	}
}

// equalUnorderedStrings returns true if a and b contain the same
// elements regardless of order. targetsFor preserves the input
// order today, but asserting on order would couple the test to
// an internal detail that's not part of the contract.
func equalUnorderedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
