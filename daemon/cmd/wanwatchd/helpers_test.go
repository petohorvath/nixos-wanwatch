package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

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
