package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// readGauge extracts a single Gauge's current value via the
// Prometheus DTO interface. Used by tests that assert on the
// gauges the daemon updates directly — going through scrape
// would test the wire format too, but that's already covered
// in `internal/metrics`.
func readGauge(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("Gauge.Write: %v", err)
	}
	return m.GetGauge().GetValue()
}

// readCounter is the Counter twin of readGauge — same idea, same
// DTO entry point.
func readCounter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("Counter.Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

// testDaemon builds a daemon wired against `cfg` without invoking
// bootstrap() — netlink rule install isn't available in unit tests.
func testDaemon(t *testing.T, cfg *config.Config) *daemon {
	t.Helper()
	cfg.Global.StatePath = filepath.Join(t.TempDir(), "state.json")
	cfg.Global.HooksDir = t.TempDir()
	d := newDaemon(cfg, metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Default the apply seams to succeeding fakes: the sandbox has no
	// CAP_NET_ADMIN, so the real netlink path can't run. Tests that
	// exercise an apply *failure* override the relevant seam.
	d.ifindexOf = func(string) (int, error) { return 1, nil }
	d.writeRoute = func(context.Context, apply.DefaultRoute) error { return nil }
	d.interfaceAddrs = func(string) ([]net.IP, error) { return nil, nil }
	d.flushConntrack = func(context.Context, probe.Family, net.IP) (uint, error) { return 0, nil }
	return d
}

// failingIfindex is a d.ifindexOf stub that always fails — the
// ifindex-lookup hard failure applyRoutes must surface and
// commitDecision must hold pending.
func failingIfindex(string) (int, error) {
	return 0, errors.New("apply test: no such interface")
}

// countWrites points d.writeRoute at a stub that records every route
// write and returns the running count, so a test can assert
// applyRoutes ran without depending on netlink.
func countWrites(d *daemon) *int {
	var n int
	d.writeRoute = func(context.Context, apply.DefaultRoute) error {
		n++
		return nil
	}
	return &n
}

func testCfg() *config.Config {
	return &config.Config{
		Wans: map[string]config.Wan{
			"primary": {
				Name:      "primary",
				Interface: "eth0",
				Probe: config.Probe{
					Targets: config.Targets{
						V4: []string{"1.1.1.1"},
						V6: []string{"2606:4700:4700::1111"},
					},
				},
			},
			"backup": {
				Name:      "backup",
				Interface: "wwan0",
				// v4-only WAN — only a v4 probe target.
				Probe: config.Probe{
					Targets: config.Targets{
						V4: []string{"8.8.8.8"},
					},
				},
			},
		},
	}
}

func TestBoolToFloat(t *testing.T) {
	t.Parallel()
	if got := boolToFloat(true); got != 1 {
		t.Errorf("boolToFloat(true) = %v, want 1", got)
	}
	if got := boolToFloat(false); got != 0 {
		t.Errorf("boolToFloat(false) = %v, want 0", got)
	}
}

func TestInterfaceIndexLoopback(t *testing.T) {
	t.Parallel()
	// `lo` is the one interface that's always present on every Linux
	// host, regardless of CI runner / netns / sandbox. ifindex 1 is
	// the convention but not guaranteed; we just assert > 0.
	idx, err := interfaceIndex("lo")
	if err != nil {
		t.Fatalf("interfaceIndex(lo) = %v, want nil", err)
	}
	if idx <= 0 {
		t.Errorf("interfaceIndex(lo) = %d, want > 0", idx)
	}
}

func TestInterfaceIndexUnknown(t *testing.T) {
	t.Parallel()
	// A name no kernel could ever assign (>15 chars rejects at
	// netlink ABI; we go further to dodge any test-VM quirk).
	if _, err := interfaceIndex("wanwatch-test-no-such-iface"); err == nil {
		t.Error("interfaceIndex(missing) = nil error, want non-nil")
	}
}

func TestInterfaceAddrsLoopback(t *testing.T) {
	t.Parallel()
	// `lo` always exists, and all its addresses are loopback — so the
	// global-unicast filter leaves nothing. Exercises the resolve +
	// filter path end to end on a real interface.
	addrs, err := interfaceAddrs("lo")
	if err != nil {
		t.Fatalf("interfaceAddrs(lo) = %v, want nil", err)
	}
	if len(addrs) != 0 {
		t.Errorf("interfaceAddrs(lo) = %v, want empty (loopback filtered out)", addrs)
	}
}

func TestInterfaceAddrsUnknown(t *testing.T) {
	t.Parallel()
	if _, err := interfaceAddrs("wanwatch-test-no-such-iface"); err == nil {
		t.Error("interfaceAddrs(missing) = nil error, want non-nil")
	}
}

func TestFilterGlobalUnicast(t *testing.T) {
	t.Parallel()
	mkNet := func(s string) *net.IPNet { return &net.IPNet{IP: net.ParseIP(s)} }
	addrs := []net.Addr{
		mkNet("192.0.2.1"),                           // global v4 — kept
		mkNet("2001:db8::1"),                         // global v6 — kept
		mkNet("127.0.0.1"),                           // loopback — dropped
		mkNet("fe80::1"),                             // link-local — dropped
		&net.IPAddr{IP: net.ParseIP("198.51.100.1")}, // not *net.IPNet — dropped
	}
	got := filterGlobalUnicast(addrs)
	if len(got) != 2 {
		t.Fatalf("filterGlobalUnicast = %v, want 2 (the globals)", got)
	}
	if !got[0].Equal(net.ParseIP("192.0.2.1")) || !got[1].Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("filterGlobalUnicast = %v, want [192.0.2.1 2001:db8::1]", got)
	}
}

func TestIfaceFor(t *testing.T) {
	t.Parallel()
	wans := map[string]*wanState{
		"primary": {cfg: config.Wan{Interface: "eth0"}},
		"backup":  {cfg: config.Wan{Interface: "wwan0"}},
	}
	cases := []struct {
		name   string
		active selector.Active
		want   string
	}{
		{"absent → empty", selector.NoActive, ""},
		{"known WAN → its iface", selector.Active{Wan: "primary", Has: true}, "eth0"},
		{"unknown WAN → empty", selector.Active{Wan: "ghost", Has: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ifaceFor(wans, tc.active); got != tc.want {
				t.Errorf("ifaceFor(_, %+v) = %q, want %q", tc.active, got, tc.want)
			}
		})
	}
}

func TestProbedFamiliesFor(t *testing.T) {
	t.Parallel()
	wans := map[string]*wanState{
		"v4only": {
			cfg:      config.Wan{Interface: "wan4"},
			families: map[probe.Family]*familyState{probe.FamilyV4: {family: probe.FamilyV4}},
		},
		"dual": {
			cfg: config.Wan{Interface: "wan0"},
			families: map[probe.Family]*familyState{
				probe.FamilyV4: {family: probe.FamilyV4},
				probe.FamilyV6: {family: probe.FamilyV6},
			},
		},
	}

	if got := probedFamiliesFor(wans, selector.NoActive); got != nil {
		t.Errorf("absent Active = %v, want nil", got)
	}
	if got := probedFamiliesFor(wans, selector.Active{Wan: "ghost", Has: true}); got != nil {
		t.Errorf("unknown WAN = %v, want nil", got)
	}
	if got := probedFamiliesFor(wans, selector.Active{Wan: "v4only", Has: true}); len(got) != 1 || got[0] != "v4" {
		t.Errorf("v4-only WAN = %v, want [v4]", got)
	}
	// Dual-stack: map iteration order is non-deterministic, so just
	// check the multiset.
	got := probedFamiliesFor(wans, selector.Active{Wan: "dual", Has: true})
	if len(got) != 2 {
		t.Fatalf("dual WAN = %v, want 2 entries", got)
	}
	saw := map[string]bool{}
	for _, f := range got {
		saw[f] = true
	}
	if !saw["v4"] || !saw["v6"] {
		t.Errorf("dual WAN = %v, want both v4 and v6", got)
	}
}
