package main

import (
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// pendingBackup leaves Group home on primary with a Decision for
// backup pending: primary loses v4 and backup's v6 route fails.
var pendingBackup = []step{
	{name: "gateways cached", do: cachedGateways},
	{
		name: "both WANs gain carrier",
		do: func(h *harness) {
			h.Link(carrier("eth0", true))
			h.Link(carrier("wwan0", true))
		},
		want: []string{
			"route home v4 eth0 via 192.0.2.1",
			"route home v6 eth0 via 2001:db8::1",
			"hook up home →primary",
		},
		active: "primary",
	},
	{
		name: "primary loses v4, backup's v6 route fails",
		do: func(h *harness) {
			h.kernel.fail(opRoute, "wwan0", probe.FamilyV6)
			h.Probe(lossy("primary", probe.FamilyV4))
		},
		want: []string{
			"route home v4 wwan0 via 198.51.100.1",
			"route home v6 wwan0 via 2001:db8:1::1 ✗",
		},
		active: "primary",
	},
}

// TestPendingDecisionRetry pins the order in which a probe result
// reaches a pending Decision: the result's Health change recomputes
// the Group first, so a retry never moves the Selection onto a WAN the
// same result made unhealthy, and each result attempts Apply at most once.
func TestPendingDecisionRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		steps []step
		check func(testing.TB, *harness)
	}{
		{
			name: "a result that flips the pending WAN unhealthy recomputes away",
			steps: append(pendingBackup[:len(pendingBackup):len(pendingBackup)], step{
				name: "backup loses v4",
				do: func(h *harness) {
					h.kernel.heal(opRoute, "wwan0", probe.FamilyV6)
					h.Probe(lossy("backup", probe.FamilyV4))
				},
				want:   []string{"hook down home primary→"},
				active: "",
			}),
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				if got := len(h.hooks()); got != 2 {
					tb.Errorf("Hooks delivered = %d, want 2 (up, down)", got)
				}
				if got := h.State().Groups["home"].DecisionsTotal; got != 3 {
					tb.Errorf("decisionsTotal = %d, want 3", got)
				}
			},
		},
		{
			name: "a result that keeps the pending WAN healthy commits once",
			steps: append(pendingBackup[:len(pendingBackup):len(pendingBackup)], step{
				name: "backup's first full v4 Window",
				do: func(h *harness) {
					h.kernel.heal(opRoute, "wwan0", probe.FamilyV6)
					h.Probe(healthy("backup", probe.FamilyV4))
				},
				want: []string{
					"route home v4 wwan0 via 198.51.100.1",
					"route home v6 wwan0 via 2001:db8:1::1",
					"flush v4 192.0.2.10",
					"flush v6 2001:db8::10",
					"hook switch home primary→backup",
				},
				active: "backup",
				// The retry follows the result's State republication.
				decisionLast: true,
			}),
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				if got := h.State().Groups["home"].DecisionsTotal; got != 2 {
					tb.Errorf("decisionsTotal = %d, want 2", got)
				}
			},
		},
		{
			name: "a result that flips a WAN healthy attempts Apply once",
			steps: []step{
				{name: "gateways cached", do: cachedGateways},
				{
					name: "both WANs gain carrier",
					do: func(h *harness) {
						h.Link(carrier("eth0", true))
						h.Link(carrier("wwan0", true))
					},
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"route home v6 eth0 via 2001:db8::1",
						"hook up home →primary",
					},
					active: "primary",
				},
				{
					name: "primary loses v4",
					do:   func(h *harness) { h.Probe(lossy("primary", probe.FamilyV4)) },
					want: []string{
						"route home v4 wwan0 via 198.51.100.1",
						"route home v6 wwan0 via 2001:db8:1::1",
						"flush v4 192.0.2.10",
						"flush v6 2001:db8::10",
						"hook switch home primary→backup",
					},
					active: "backup",
				},
				{
					name: "primary recovers v4, its v6 route fails",
					do: func(h *harness) {
						h.kernel.fail(opRoute, "eth0", probe.FamilyV6)
						h.Probe(healthy("primary", probe.FamilyV4))
					},
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"route home v6 eth0 via 2001:db8::1 ✗",
					},
					active: "backup",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				if n := readCounter(tb, h.metrics.ApplyRouteErrors.WithLabelValues("home", "v6")); n != 1 {
					tb.Errorf("apply_route_errors_total{home,v6} = %v, want 1", n)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startHarness(t, baseConfig())
			h.Trace() // the bootstrap rules belong to the startup tests
			runSteps(t, h, tc.steps)
			tc.check(t, h)
		})
	}
}

// TestRouteRepair pins the retry of a failed Gateway refresh: the
// selected WAN's next probe result rewrites every Family, and the
// Repair is no Decision, so it delivers no Hook and keeps selectionSince.
func TestRouteRepair(t *testing.T) {
	t.Parallel()
	h := startHarness(t, baseConfig())
	h.Trace() // the bootstrap rules belong to the startup tests
	runSteps(t, h, []step{
		primaryActive,
		{
			name: "v4 refresh fails",
			do: func(h *harness) {
				h.kernel.fail(opRoute, "eth0", probe.FamilyV4)
				h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.2"))
			},
			want:   []string{"route home v4 eth0 via 192.0.2.2 ✗"},
			active: "primary",
		},
		{
			name: "failed repair",
			do:   func(h *harness) { h.Probe(healthy("primary", probe.FamilyV4)) },
			want: []string{
				"route home v4 eth0 via 192.0.2.2 ✗",
				"route home v6 eth0 via 2001:db8::1",
			},
			active: "primary",
		},
		{
			name: "next probe result repairs every Family",
			do: func(h *harness) {
				h.kernel.heal(opRoute, "eth0", probe.FamilyV4)
				h.Probe(healthy("primary", probe.FamilyV4))
			},
			want: []string{
				"route home v4 eth0 via 192.0.2.2",
				"route home v6 eth0 via 2001:db8::1",
			},
			active: "primary",
		},
		{
			name:   "repaired Selection writes nothing",
			do:     func(h *harness) { h.Probe(healthy("primary", probe.FamilyV4)) },
			active: "primary",
		},
	})
	hooks := h.hooks()
	if len(hooks) != 1 {
		t.Fatalf("Hooks delivered = %d, want 1 (up)", len(hooks))
	}
	group := h.State().Groups["home"]
	if group.DecisionsTotal != 1 {
		t.Errorf("decisionsTotal = %d, want 1", group.DecisionsTotal)
	}
	if group.SelectionSince == nil || !group.SelectionSince.Equal(hooks[0].Timestamp) {
		t.Errorf("selectionSince = %v, want the up Hook's %v", group.SelectionSince, hooks[0].Timestamp)
	}
	if n := readCounter(t, h.metrics.ApplyRouteErrors.WithLabelValues("home", "v4")); n != 2 {
		t.Errorf("apply_route_errors_total{home,v4} = %v, want 2", n)
	}
}
