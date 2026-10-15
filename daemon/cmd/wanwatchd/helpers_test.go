package main

import (
	"net"
	"slices"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// readGauge extracts a single Gauge's current value via the
// Prometheus DTO interface. Used by tests that assert on the
// gauges the daemon updates directly — going through scrape
// would test the wire format too, but that's already covered
// in `internal/metrics`.
func readGauge(tb testing.TB, g prometheus.Gauge) float64 {
	tb.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		tb.Fatalf("Gauge.Write: %v", err)
	}
	return m.GetGauge().GetValue()
}

// readCounter is the Counter twin of readGauge — same idea, same
// DTO entry point.
func readCounter(tb testing.TB, c prometheus.Counter) float64 {
	tb.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		tb.Fatalf("Counter.Write: %v", err)
	}
	return m.GetCounter().GetValue()
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
