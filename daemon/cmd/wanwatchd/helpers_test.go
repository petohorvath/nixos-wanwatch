package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// readGauge extracts a single Gauge's current value via the
// Prometheus DTO interface. Used by tests that assert on the
// gauges the daemon updates directly — going through scrape
// would test the wire format too, but that's already covered
// in `internal/metrics`.
func readGauge(t testing.TB, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("Gauge.Write: %v", err)
	}
	return m.GetGauge().GetValue()
}

// readCounter is the Counter twin of readGauge — same idea, same
// DTO entry point.
func readCounter(t testing.TB, c prometheus.Counter) float64 {
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
	return testDaemonWithContext(t.Context(), t, cfg)
}

func testDaemonWithContext(ctx context.Context, t *testing.T, cfg *config.Config) *daemon {
	t.Helper()
	cfg.Global.StatePath = filepath.Join(t.TempDir(), "state.json")
	cfg.Global.HooksDir = t.TempDir()
	mreg := metrics.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The sandbox has no CAP_NET_ADMIN, so the real netlink path can't
	// run: the fake kernel succeeds unless a test scripts a failure.
	hooks := state.NewHookNotifier(ctx, cfg.Global.HooksDir,
		time.Duration(cfg.Global.HookTimeoutMs)*time.Millisecond, mreg, logger)
	d := newDaemon(cfg, newFakeKernel(&trace{}, cfg), hooks, mreg, logger)
	t.Cleanup(d.hooks.Close)
	return d
}

// readPublishedState reads the state.json d publishes.
func readPublishedState(t *testing.T, d *daemon) state.State {
	t.Helper()
	data, err := os.ReadFile(d.cfg.Global.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var snap state.State
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// fakeKernelOf returns the fake kernel testDaemon wired into d.
func fakeKernelOf(d *daemon) *fakeKernel {
	return d.kernel.(*fakeKernel)
}

// routeWrites counts the route writes d's fake kernel has attempted.
func routeWrites(d *daemon) int {
	n := 0
	for _, line := range fakeKernelOf(d).trace.all() {
		if strings.HasPrefix(line, "route ") {
			n++
		}
	}
	return n
}

// gatewayText renders a State Gateway for failure messages: the
// address, or "<nil>" when the Gateway is absent.
func gatewayText(gw *string) string {
	if gw == nil {
		return "<nil>"
	}
	return *gw
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
	wans := map[string]config.Wan{
		"primary": {Interface: "eth0"},
		"backup":  {Interface: "wwan0"},
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
	wans := map[string]config.Wan{
		"v4only": {Probe: config.Probe{Targets: config.Targets{V4: []string{"192.0.2.1"}}}},
		"dual": {Probe: config.Probe{Targets: config.Targets{
			V4: []string{"192.0.2.1"},
			V6: []string{"2001:db8::1"},
		}}},
	}
	cases := []struct {
		name   string
		active selector.Active
		want   []string
	}{
		{"absent → nil", selector.NoActive, nil},
		{"unknown WAN → nil", selector.Active{Wan: "ghost", Has: true}, nil},
		{"v4-only WAN", selector.Active{Wan: "v4only", Has: true}, []string{"v4"}},
		{"dual-stack WAN, v4 first", selector.Active{Wan: "dual", Has: true}, []string{"v4", "v6"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probedFamiliesFor(wans, tc.active); !slices.Equal(got, tc.want) {
				t.Errorf("probedFamiliesFor(_, %+v) = %v, want %v", tc.active, got, tc.want)
			}
		})
	}
}
