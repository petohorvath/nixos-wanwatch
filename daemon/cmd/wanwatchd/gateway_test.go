package main

import (
	"net"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

func TestGatewayCacheGetEmpty(t *testing.T) {
	t.Parallel()
	c := newGatewayCache()
	gw, ok := c.get("eth0", rtnl.RouteFamilyV4)
	if ok {
		t.Errorf("Get on empty cache: ok=%v gw=%v, want ok=false", ok, gw)
	}
}

func TestGatewayCacheSetGet(t *testing.T) {
	t.Parallel()
	c := newGatewayCache()
	want := net.ParseIP("192.0.2.1")
	c.set("eth0", rtnl.RouteFamilyV4, want)

	got, ok := c.get("eth0", rtnl.RouteFamilyV4)
	if !ok {
		t.Fatal("Get after Set: ok=false")
	}
	if !got.Equal(want) {
		t.Errorf("Get = %v, want %v", got, want)
	}
}

func TestGatewayCacheSetRecordsScopeLink(t *testing.T) {
	t.Parallel()
	// Scope-link routes carry a nil gateway — the cache must
	// distinguish "no entry" from "entry with nil gw".
	c := newGatewayCache()
	c.set("wg0", rtnl.RouteFamilyV4, nil)

	gw, ok := c.get("wg0", rtnl.RouteFamilyV4)
	if !ok {
		t.Fatal("Get on scope-link entry: ok=false; want true with nil gw")
	}
	if gw != nil {
		t.Errorf("scope-link gw = %v, want nil", gw)
	}
}

func TestGatewayCacheKeysByFamily(t *testing.T) {
	t.Parallel()
	c := newGatewayCache()
	c.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	c.set("eth0", rtnl.RouteFamilyV6, net.ParseIP("2001:db8::1"))

	v4, _ := c.get("eth0", rtnl.RouteFamilyV4)
	v6, _ := c.get("eth0", rtnl.RouteFamilyV6)
	if v4.String() != "192.0.2.1" {
		t.Errorf("eth0/v4 = %v, want 192.0.2.1", v4)
	}
	if v6.String() != "2001:db8::1" {
		t.Errorf("eth0/v6 = %v, want 2001:db8::1", v6)
	}
}

func TestGatewayCacheClear(t *testing.T) {
	t.Parallel()
	c := newGatewayCache()
	c.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	c.clear("eth0", rtnl.RouteFamilyV4)

	if _, ok := c.get("eth0", rtnl.RouteFamilyV4); ok {
		t.Error("entry still present after Clear")
	}
}

// TestGatewayCacheFormats covers both encodings of an absent
// Gateway: nil for State, "" for Hook env vars.
func TestGatewayCacheFormats(t *testing.T) {
	t.Parallel()
	c := newGatewayCache()
	c.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
	c.set("wg0", rtnl.RouteFamilyV4, nil) // scope-link

	tests := []struct {
		name, iface, wantString, wantAddress string
	}{
		{name: "next-hop", iface: "eth0", wantString: "192.0.2.1", wantAddress: "192.0.2.1"},
		{name: "scope-link", iface: "wg0", wantString: "", wantAddress: "<nil>"},
		{name: "missing", iface: "missing", wantString: "", wantAddress: "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := c.string(tt.iface, rtnl.RouteFamilyV4); got != tt.wantString {
				t.Errorf("string(%s/v4) = %q, want %q", tt.iface, got, tt.wantString)
			}
			if got := gatewayText(c.address(tt.iface, rtnl.RouteFamilyV4)); got != tt.wantAddress {
				t.Errorf("address(%s/v4) = %s, want %s", tt.iface, got, tt.wantAddress)
			}
		})
	}
}

// TestProbeRouteFamilyValuesAgree pins the underlying contract
// that lets the daemon convert probe.Family → rtnl.RouteFamily by
// a plain cast: both types now use unix.AF_INET / AF_INET6 as
// their values.
func TestProbeRouteFamilyValuesAgree(t *testing.T) {
	t.Parallel()
	if int(probe.FamilyV4) != int(rtnl.RouteFamilyV4) {
		t.Errorf("probe.FamilyV4 (%d) != rtnl.RouteFamilyV4 (%d)",
			int(probe.FamilyV4), int(rtnl.RouteFamilyV4))
	}
	if int(probe.FamilyV6) != int(rtnl.RouteFamilyV6) {
		t.Errorf("probe.FamilyV6 (%d) != rtnl.RouteFamilyV6 (%d)",
			int(probe.FamilyV6), int(rtnl.RouteFamilyV6))
	}
}

// gatewayPair is one WAN's v4 and v6 Gateway in State, "" for null.
type gatewayPair struct{ v4, v6 string }

// assertGateways checks the Gateways State publishes for each WAN in
// want.
func assertGateways(tb testing.TB, h *harness, want map[string]gatewayPair) {
	tb.Helper()
	wans := h.State().Wans
	for wan, pair := range want {
		gws := wans[wan].Gateways
		got := gatewayPair{v4: gatewayString(gws.V4), v6: gatewayString(gws.V6)}
		if got != pair {
			tb.Errorf("State Gateways of %s = %+v, want %+v", wan, got, pair)
		}
	}
}

func gatewayString(gw *string) string {
	if gw == nil {
		return ""
	}
	return *gw
}

// gatewayText renders a State Gateway for failure messages: the
// address, or "<nil>" when the Gateway is absent.
func gatewayText(gw *string) string {
	if gw == nil {
		return "<nil>"
	}
	return *gw
}

// primaryActive teaches the daemon a v4 and v6 Gateway on eth0, then
// raises eth0's carrier so Group home selects primary.
var primaryActive = step{
	name: "primary active",
	do: func(h *harness) {
		h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.1"))
		h.Route(gateway("eth0", probe.FamilyV6, "2001:db8::1"))
		h.Link(carrier("eth0", true))
	},
	want: []string{
		"route home v4 eth0 via 192.0.2.1",
		"route home v6 eth0 via 2001:db8::1",
		"hook up home →primary",
	},
	active: "primary",
}

// routeStep delivers events in order, expecting trace want and the
// Selection active ("" for none) afterwards.
func routeStep(name, active string, want []string, events ...rtnl.RouteEvent) step {
	return step{
		name: name,
		do: func(h *harness) {
			for _, e := range events {
				h.Route(e)
			}
		},
		want:   want,
		active: active,
	}
}

// TestGatewayDiscovery pins how RouteEvents reach the Gateway cache,
// State, and the active WAN's group-table routes: only a changed
// Gateway of a probed Family on the selected WAN is rewritten, and
// no Gateway change alone moves the Selection or delivers a Hook.
func TestGatewayDiscovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		cfg      func(*config.Config) // nil keeps baseConfig
		steps    []step
		gateways map[string]gatewayPair // State after the last step
	}{
		{
			name:     "none learned",
			gateways: map[string]gatewayPair{"primary": {}, "backup": {}},
		},
		{
			name: "learned per Family",
			steps: []step{
				routeStep("v4 learned", "", nil, gateway("eth0", probe.FamilyV4, "192.0.2.1")),
				routeStep("v6 learned", "", nil, gateway("eth0", probe.FamilyV6, "2001:db8::1")),
			},
			gateways: map[string]gatewayPair{"primary": {"192.0.2.1", "2001:db8::1"}, "backup": {}},
		},
		{
			name: "replaced",
			steps: []step{routeStep(
				"v4 replaced", "", nil,
				gateway("eth0", probe.FamilyV4, "192.0.2.1"),
				gateway("eth0", probe.FamilyV4, "192.0.2.2"),
			)},
			gateways: map[string]gatewayPair{"primary": {v4: "192.0.2.2"}},
		},
		{
			name: "withdrawn",
			steps: []step{routeStep(
				"v4 withdrawn", "", nil,
				gateway("eth0", probe.FamilyV4, "192.0.2.1"),
				gatewayGone("eth0", probe.FamilyV4),
			)},
			gateways: map[string]gatewayPair{"primary": {}},
		},
		{
			name: "new Gateway on the active WAN refreshes only v4",
			steps: []step{
				primaryActive,
				routeStep("v4 Gateway changes", "primary",
					[]string{"route home v4 eth0 via 192.0.2.2"},
					gateway("eth0", probe.FamilyV4, "192.0.2.2")),
			},
			gateways: map[string]gatewayPair{"primary": {"192.0.2.2", "2001:db8::1"}},
		},
		{
			name: "new Gateway on the active WAN refreshes only v6",
			steps: []step{
				primaryActive,
				routeStep("v6 Gateway changes", "primary",
					[]string{"route home v6 eth0 via 2001:db8::2"},
					gateway("eth0", probe.FamilyV6, "2001:db8::2")),
			},
			gateways: map[string]gatewayPair{"primary": {"192.0.2.1", "2001:db8::2"}},
		},
		{
			name: "Selection without a Gateway commits, discovery writes each Family",
			steps: []step{
				{
					name:   "primary gains carrier",
					do:     func(h *harness) { h.Link(carrier("eth0", true)) },
					want:   []string{"hook up home →primary"},
					active: "primary",
				},
				routeStep("v6 discovered", "primary",
					[]string{"route home v6 eth0 via 2001:db8::1"},
					gateway("eth0", probe.FamilyV6, "2001:db8::1")),
				routeStep("v4 discovered", "primary",
					[]string{"route home v4 eth0 via 192.0.2.1"},
					gateway("eth0", probe.FamilyV4, "192.0.2.1")),
			},
			gateways: map[string]gatewayPair{"primary": {"192.0.2.1", "2001:db8::1"}},
		},
		{
			name: "Gateway removed from the active WAN leaves the stale route",
			steps: []step{
				primaryActive,
				routeStep("v4 withdrawn", "primary", nil, gatewayGone("eth0", probe.FamilyV4)),
			},
			gateways: map[string]gatewayPair{"primary": {v6: "2001:db8::1"}},
		},
		{
			name: "unrelated interface writes no route",
			steps: []step{
				primaryActive,
				routeStep("lo Gateway", "primary", nil, gateway("lo", probe.FamilyV4, "127.0.0.1")),
			},
			gateways: map[string]gatewayPair{"primary": {"192.0.2.1", "2001:db8::1"}, "backup": {}},
		},
		{
			name: "inactive WAN writes no route",
			steps: []step{
				primaryActive,
				{
					name:   "backup gains carrier",
					do:     func(h *harness) { h.Link(carrier("wwan0", true)) },
					active: "primary",
				},
				routeStep(
					"backup Gateways", "primary", nil,
					gateway("wwan0", probe.FamilyV4, "198.51.100.1"),
					gateway("wwan0", probe.FamilyV6, "2001:db8:1::1"),
					gateway("wwan0", probe.FamilyV4, "198.51.100.2"),
					gatewayGone("wwan0", probe.FamilyV6),
				),
			},
			gateways: map[string]gatewayPair{"primary": {"192.0.2.1", "2001:db8::1"}, "backup": {v4: "198.51.100.2"}},
		},
		{
			name: "point-to-point WAN routes scope link without a Gateway",
			cfg: func(cfg *config.Config) {
				wan := cfg.Wans["primary"]
				wan.PointToPoint = true
				cfg.Wans["primary"] = wan
			},
			steps: []step{
				{
					name: "primary gains carrier",
					do:   func(h *harness) { h.Link(carrier("eth0", true)) },
					want: []string{
						"route home v4 eth0 scope link",
						"route home v6 eth0 scope link",
						"hook up home →primary",
					},
					active: "primary",
				},
				routeStep("scope-link v4 default appears", "primary",
					[]string{"route home v4 eth0 scope link"},
					gateway("eth0", probe.FamilyV4, "")),
			},
			gateways: map[string]gatewayPair{"primary": {}},
		},
		{
			name: "unprobed Family is not reapplied",
			cfg: func(cfg *config.Config) {
				wan := cfg.Wans["primary"]
				wan.Probe.Targets.V6 = nil
				cfg.Wans["primary"] = wan
			},
			steps: []step{
				routeStep("v4 learned", "", nil, gateway("eth0", probe.FamilyV4, "192.0.2.1")),
				{
					name: "primary gains carrier",
					do:   func(h *harness) { h.Link(carrier("eth0", true)) },
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"hook up home →primary",
					},
					active: "primary",
				},
				routeStep("v6 Gateway appears", "primary", nil, gateway("eth0", probe.FamilyV6, "2001:db8::1")),
			},
			gateways: map[string]gatewayPair{"primary": {"192.0.2.1", "2001:db8::1"}},
		},
		{
			name: "duplicate Gateway event writes and publishes nothing",
			steps: []step{
				primaryActive,
				routeStep("v4 Gateway changes", "primary",
					[]string{"route home v4 eth0 via 192.0.2.2"},
					gateway("eth0", probe.FamilyV4, "192.0.2.2")),
				{
					name: "same v4 Gateway again",
					do: func(h *harness) {
						before := readCounter(h.tb, h.metrics.StatePublications)
						h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.2"))
						if after := readCounter(h.tb, h.metrics.StatePublications); after != before {
							h.tb.Errorf("duplicate Gateway event published State: %v → %v", before, after)
						}
					},
					active: "primary",
				},
			},
			gateways: map[string]gatewayPair{"primary": {"192.0.2.2", "2001:db8::1"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig()
			if tc.cfg != nil {
				tc.cfg(cfg)
			}
			h := startHarness(t, cfg)
			h.Trace() // the bootstrap rules belong to the startup tests
			runSteps(t, h, tc.steps)
			assertGateways(t, h, tc.gateways)
		})
	}
}
