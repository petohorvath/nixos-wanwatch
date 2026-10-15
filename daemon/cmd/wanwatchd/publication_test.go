package main

import (
	"slices"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// step is one harness action with the trace it must produce and the
// Selection State must show afterwards ("" for none).
type step struct {
	name   string
	do     func(*harness)
	want   []string
	active string
}

// runSteps runs each step and asserts its trace and published
// Selection.
func runSteps(tb testing.TB, h *harness, steps []step) {
	tb.Helper()
	for _, s := range steps {
		s.do(h)
		if got := h.Trace(); !slices.Equal(got, s.want) {
			tb.Errorf("step %q trace (-want +got):\n%s", s.name, lineDiff(s.want, got))
		}
		active := ""
		if a := h.State().Groups["home"].Active; a != nil {
			active = *a
		}
		if active != s.active {
			tb.Errorf("step %q: State Selection = %q, want %q", s.name, active, s.active)
		}
	}
}

// cachedGateways teaches the daemon a v4 and v6 Gateway for both
// baseConfig WANs, and gives both interfaces addresses to flush.
func cachedGateways(h *harness) {
	h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.1"))
	h.Route(gateway("eth0", probe.FamilyV6, "2001:db8::1"))
	h.Route(gateway("wwan0", probe.FamilyV4, "198.51.100.1"))
	h.Route(gateway("wwan0", probe.FamilyV6, "2001:db8:1::1"))
	h.kernel.setAddrs("eth0", "192.0.2.10", "2001:db8::10")
	h.kernel.setAddrs("wwan0", "198.51.100.10", "2001:db8:1::10")
}

// assertDecisionTimes checks that the Selection in State started when
// the last Hook was stamped. With exact, State's updatedAt must carry
// the same stamp: the Decision was the step's last State publication.
// Otherwise a later Health publication in the same step restamped it.
func assertDecisionTimes(tb testing.TB, h *harness, exact bool) {
	tb.Helper()
	hooks := h.hooks()
	if len(hooks) == 0 {
		tb.Fatal("no Hook delivered")
	}
	stamp := hooks[len(hooks)-1].Timestamp
	s := h.State()
	if since := s.Groups["home"].ActiveSince; since == nil || !since.Equal(stamp) {
		tb.Errorf("activeSince = %v, want the Hook timestamp %v", since, stamp)
	}
	if exact && !s.UpdatedAt.Equal(stamp) {
		tb.Errorf("updatedAt = %v, want the Hook timestamp %v", s.UpdatedAt, stamp)
	}
	if s.UpdatedAt.Before(stamp) {
		tb.Errorf("updatedAt = %v precedes the Decision at %v", s.UpdatedAt, stamp)
	}
}

// TestDecisionPublication pins ADR-0009 as the daemon behaves today:
// Apply converges before a Decision publishes State and then its Hook,
// and a failed Apply publishes neither until a retry succeeds.
func TestDecisionPublication(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		steps []step
		check func(testing.TB, *harness)
	}{
		{
			name: "cold-start up",
			steps: []step{
				{name: "gateways cached", do: cachedGateways},
				{
					name: "primary gains carrier",
					do:   func(h *harness) { h.Link(carrier("eth0", true)) },
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"route home v6 eth0 via 2001:db8::1",
						"hook up home →primary",
					},
					active: "primary",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				// The carrier change republishes State after the Decision.
				assertDecisionTimes(tb, h, false)
			},
		},
		{
			name: "switch flushes the vacated WAN",
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
					name: "primary recovers v4",
					do:   func(h *harness) { h.Probe(healthy("primary", probe.FamilyV4)) },
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"route home v6 eth0 via 2001:db8::1",
						"flush v4 198.51.100.10",
						"flush v6 2001:db8:1::10",
						"hook switch home backup→primary",
					},
					active: "primary",
				},
				{
					name:   "stop",
					do:     (*harness).Stop,
					want:   []string{"hooks closed"},
					active: "primary",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				// The Family verdict change republishes State after the Decision.
				assertDecisionTimes(tb, h, false)
			},
		},
		{
			name: "failed Apply publishes nothing until the retry",
			steps: []step{
				{name: "gateways cached", do: cachedGateways},
				{
					name: "backup gains carrier",
					do:   func(h *harness) { h.Link(carrier("wwan0", true)) },
					want: []string{
						"route home v4 wwan0 via 198.51.100.1",
						"route home v6 wwan0 via 2001:db8:1::1",
						"hook up home →backup",
					},
					active: "backup",
				},
				{
					name: "primary gains carrier, its v6 route fails",
					do: func(h *harness) {
						h.kernel.fail(opRoute, "eth0", probe.FamilyV6)
						h.Link(carrier("eth0", true))
					},
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"route home v6 eth0 via 2001:db8::1 ✗",
					},
					active: "backup",
				},
				{
					name: "primary's next probe cycle retries",
					do: func(h *harness) {
						h.kernel.heal(opRoute, "eth0", probe.FamilyV6)
						// A partial Window: no Health change, only the retry.
						h.Probe(probe.ProbeResult{Wan: "primary", Family: probe.FamilyV4})
					},
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"route home v6 eth0 via 2001:db8::1",
						"flush v4 198.51.100.10",
						"flush v6 2001:db8:1::10",
						"hook switch home backup→primary",
					},
					active: "primary",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				// The retry's Decision is the step's last publication.
				assertDecisionTimes(tb, h, true)
				if n := readCounter(tb, h.metrics.ApplyRouteErrors.WithLabelValues("home", "v6")); n != 1 {
					tb.Errorf("apply_route_errors_total{home,v6} = %v, want 1", n)
				}
				if got := h.State().Groups["home"].DecisionsTotal; got != 2 {
					tb.Errorf("decisionsTotal = %d, want 2", got)
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
