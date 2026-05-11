package main

import (
	"context"
	"io"
	"log/slog"
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

func sptr(s string) *string { return &s }

func testCfg() *config.Config {
	return &config.Config{
		Wans: map[string]config.Wan{
			"primary": {
				Name:      "primary",
				Interface: "eth0",
				Gateways:  config.Gateways{V4: sptr("192.0.2.1"), V6: sptr("2001:db8::1")},
			},
			"backup": {
				Name:      "backup",
				Interface: "wwan0",
				// v4-only WAN — no v6 gateway, so no v6 prober.
				Gateways: config.Gateways{V4: sptr("100.64.0.1")},
			},
		},
	}
}

func TestIdentKeysForOnlyEmitsFamiliesWithGateway(t *testing.T) {
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
	probeResults <- probe.ProbeResult{
		Wan:    "primary",
		Family: probe.FamilyV4,
		Stats:  probe.FamilyStats{LossRatio: 0.5, RTTMicros: 12000},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents)
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
	linkEvents <- rtnl.LinkEvent{
		Name:      "eth0",
		Carrier:   rtnl.CarrierUp,
		Operstate: rtnl.OperstateUp,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if d.wans["primary"].carrier != rtnl.CarrierUp {
		t.Errorf("primary carrier = %v, want up", d.wans["primary"].carrier)
	}
}

func TestEventLoopReturnsOnCtxCancel(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfg())

	ctx, cancel := context.WithCancel(context.Background())
	probeResults := make(chan probe.ProbeResult)
	linkEvents := make(chan rtnl.LinkEvent)

	done := make(chan struct{})
	go func() {
		eventLoop(ctx, d, probeResults, linkEvents)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("eventLoop did not return within 1s of ctx cancel")
	}
}
