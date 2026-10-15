package main

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
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

// linkFlaps raises and drops eth0's carrier n times; eth0's Gateways
// must already be cached. eth0 carries home's preferred WAN, so each
// change is exactly one Decision.
func linkFlaps(n int) []step {
	steps := make([]step, 0, 2*n)
	for i := range n {
		steps = append(
			steps,
			step{
				name: fmt.Sprintf("flap %d: eth0 gains carrier", i),
				do:   func(h *harness) { h.Link(carrier("eth0", true)) },
				want: []string{
					"route home v4 eth0 via 192.0.2.1",
					"route home v6 eth0 via 2001:db8::1",
					"hook up home →primary",
				},
				active: "primary",
			},
			step{
				name: fmt.Sprintf("flap %d: eth0 loses carrier", i),
				do:   func(h *harness) { h.Link(carrier("eth0", false)) },
				want: []string{"hook down home primary→"},
			},
		)
	}
	return steps
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
		name   string
		config func(*config.Config) // changes baseConfig; nil keeps it
		steps  []step
		check  func(testing.TB, *harness)
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
			name: "up and down flush no conntrack",
			steps: []step{
				{
					name: "gateways cached, every address lookup fails",
					do: func(h *harness) {
						cachedGateways(h)
						// A failed lookup is traced, so any lookup attempt shows.
						h.kernel.fail(opAddrs, "")
					},
				},
				{
					name: "primary gains carrier: up vacates nothing",
					do:   func(h *harness) { h.Link(carrier("eth0", true)) },
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"route home v6 eth0 via 2001:db8::1",
						"hook up home →primary",
					},
					active: "primary",
				},
				{
					name:   "primary loses carrier: down has no successor",
					do:     func(h *harness) { h.Link(carrier("eth0", false)) },
					want:   []string{"hook down home primary→"},
					active: "",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				if n := readCounter(tb, h.metrics.ApplyOpErrors.WithLabelValues("home", "conntrack_flush")); n != 0 {
					tb.Errorf("apply_op_errors_total{home,conntrack_flush} = %v, want 0", n)
				}
			},
		},
		{
			name: "failed address lookup is metered and the switch commits",
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
					name: "primary loses v4, its address lookup fails",
					do: func(h *harness) {
						h.kernel.fail(opAddrs, "eth0")
						h.Probe(lossy("primary", probe.FamilyV4))
					},
					want: []string{
						"route home v4 wwan0 via 198.51.100.1",
						"route home v6 wwan0 via 2001:db8:1::1",
						"addrs eth0 ✗",
						"hook switch home primary→backup",
					},
					active: "backup",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				if n := readCounter(tb, h.metrics.ApplyOpErrors.WithLabelValues("home", "conntrack_flush")); n != 1 {
					tb.Errorf("apply_op_errors_total{home,conntrack_flush} = %v, want 1", n)
				}
				assertDecisionTimes(tb, h, false)
			},
		},
		{
			name: "failed flush is metered and the next Family still flushes",
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
					name: "primary loses v4, its v4 flush fails",
					do: func(h *harness) {
						h.kernel.fail(opFlush, "eth0", probe.FamilyV4)
						h.Probe(lossy("primary", probe.FamilyV4))
					},
					want: []string{
						"route home v4 wwan0 via 198.51.100.1",
						"route home v6 wwan0 via 2001:db8:1::1",
						"flush v4 192.0.2.10 ✗",
						"flush v6 2001:db8::10",
						"hook switch home primary→backup",
					},
					active: "backup",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				if n := readCounter(tb, h.metrics.ApplyOpErrors.WithLabelValues("home", "conntrack_flush")); n != 1 {
					tb.Errorf("apply_op_errors_total{home,conntrack_flush} = %v, want 1", n)
				}
				assertDecisionTimes(tb, h, false)
			},
		},
		{
			name: "Hooks carry the Decision's Gateways, Families, table and mark",
			config: func(cfg *config.Config) {
				backup := cfg.Wans["backup"]
				backup.Probe.Targets.V6 = nil
				cfg.Wans["backup"] = backup
			},
			steps: []step{
				{
					name: "gateways cached, backup has no v6",
					do: func(h *harness) {
						h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.1"))
						h.Route(gateway("eth0", probe.FamilyV6, "2001:db8::1"))
						h.Route(gateway("wwan0", probe.FamilyV4, "198.51.100.1"))
					},
				},
				{
					name: "backup gains carrier",
					do:   func(h *harness) { h.Link(carrier("wwan0", true)) },
					want: []string{
						"route home v4 wwan0 via 198.51.100.1",
						"hook up home →backup",
					},
					active: "backup",
				},
				{
					name: "primary gains carrier",
					do:   func(h *harness) { h.Link(carrier("eth0", true)) },
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"route home v6 eth0 via 2001:db8::1",
						"hook switch home backup→primary",
					},
					active: "primary",
				},
				{
					name:   "a Gateway refresh is no Decision and delivers no Hook",
					do:     func(h *harness) { h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.2")) },
					want:   []string{"route home v4 eth0 via 192.0.2.2"},
					active: "primary",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				want := []state.HookContext{
					{
						Event: state.EventUp, Group: "home",
						WanNew: "backup", IfaceNew: "wwan0",
						GatewayV4New: "198.51.100.1",
						Families:     []string{"v4"},
						Table:        100, Mark: 0x100,
					},
					{
						Event: state.EventSwitch, Group: "home",
						WanOld: "backup", WanNew: "primary",
						IfaceOld: "wwan0", IfaceNew: "eth0",
						GatewayV4Old: "198.51.100.1", GatewayV4New: "192.0.2.1",
						GatewayV6New: "2001:db8::1",
						Families:     []string{"v4", "v6"},
						Table:        100, Mark: 0x100,
					},
				}
				got := h.hooks()
				if len(got) != len(want) {
					tb.Fatalf("Hooks = %+v, want %d", got, len(want))
				}
				for i := range want {
					want[i].Timestamp = got[i].Timestamp
					if !reflect.DeepEqual(got[i], want[i]) {
						tb.Errorf("Hook %d = %+v, want %+v", i, got[i], want[i])
					}
				}
				if got[0].Timestamp.IsZero() || !got[1].Timestamp.After(got[0].Timestamp) {
					tb.Errorf("Hook timestamps %v, %v are not increasing", got[0].Timestamp, got[1].Timestamp)
				}
			},
		},
		{
			name: "failed ifindex holds the Decision until a Gateway change retries it",
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
					name: "primary gains carrier, its ifindex lookup fails",
					do: func(h *harness) {
						h.kernel.fail(opIfIndex, "eth0")
						h.Link(carrier("eth0", true))
					},
					want:   []string{"ifindex eth0 ✗"},
					active: "backup",
				},
				{
					name: "events for the old Selection retry nothing",
					do: func(h *harness) {
						h.kernel.heal(opIfIndex, "eth0")
						h.Probe(probe.ProbeResult{Wan: "backup", Family: probe.FamilyV4})
						h.Route(gateway("wwan0", probe.FamilyV4, "198.51.100.2"))
					},
					active: "backup",
				},
				{
					name: "a new primary Gateway retries the whole Apply",
					do:   func(h *harness) { h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.2")) },
					want: []string{
						"route home v4 eth0 via 192.0.2.2",
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
				// The Gateway cache change republishes State after the Decision.
				assertDecisionTimes(tb, h, false)
				if n := readCounter(tb, h.metrics.ApplyOpErrors.WithLabelValues("home", "ifindex_lookup")); n != 1 {
					tb.Errorf("apply_op_errors_total{home,ifindex_lookup} = %v, want 1", n)
				}
				if got := h.State().Groups["home"].DecisionsTotal; got != 2 {
					tb.Errorf("decisionsTotal = %d, want 2", got)
				}
			},
		},
		{
			name: "failed v4 write holds the Decision until a full Window retries it",
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
					name: "primary gains carrier, its v4 route fails",
					do: func(h *harness) {
						h.kernel.fail(opRoute, "eth0", probe.FamilyV4)
						h.Link(carrier("eth0", true))
					},
					// Apply still attempts the Family after the failed one.
					want: []string{
						"route home v4 eth0 via 192.0.2.1 ✗",
						"route home v6 eth0 via 2001:db8::1",
					},
					active: "backup",
				},
				{
					name: "primary's first full Window retries",
					do: func(h *harness) {
						h.kernel.heal(opRoute, "eth0", probe.FamilyV4)
						h.Probe(healthy("primary", probe.FamilyV4))
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
				// The first full Window republishes State after the Decision.
				assertDecisionTimes(tb, h, false)
				if n := readCounter(tb, h.metrics.ApplyRouteErrors.WithLabelValues("home", "v4")); n != 1 {
					tb.Errorf("apply_route_errors_total{home,v4} = %v, want 1", n)
				}
			},
		},
		{
			name: "a failure that repeats on the retry stays pending",
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
					name: "primary gains carrier, its v4 route fails",
					do: func(h *harness) {
						h.kernel.fail(opRoute, "eth0", probe.FamilyV4)
						h.Link(carrier("eth0", true))
					},
					want: []string{
						"route home v4 eth0 via 192.0.2.1 ✗",
						"route home v6 eth0 via 2001:db8::1",
					},
					active: "backup",
				},
				{
					name: "the retry fails again",
					do:   func(h *harness) { h.Probe(probe.ProbeResult{Wan: "primary", Family: probe.FamilyV4}) },
					want: []string{
						"route home v4 eth0 via 192.0.2.1 ✗",
						"route home v6 eth0 via 2001:db8::1",
					},
					active: "backup",
				},
				{
					name: "the next retry commits",
					do: func(h *harness) {
						h.kernel.heal(opRoute, "eth0", probe.FamilyV4)
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
				if n := readCounter(tb, h.metrics.ApplyRouteErrors.WithLabelValues("home", "v4")); n != 2 {
					tb.Errorf("apply_route_errors_total{home,v4} = %v, want 2", n)
				}
				if got := h.State().Groups["home"].DecisionsTotal; got != 2 {
					tb.Errorf("decisionsTotal = %d, want 2", got)
				}
			},
		},
		{
			name: "a superseded pending Decision publishes only the converged Selection",
			steps: []step{
				{
					name: "gateways cached, primary's ifindex lookup fails",
					do: func(h *harness) {
						cachedGateways(h)
						h.kernel.fail(opIfIndex, "eth0")
					},
				},
				{
					name: "primary gains carrier",
					do:   func(h *harness) { h.Link(carrier("eth0", true)) },
					want: []string{"ifindex eth0 ✗"},
				},
				{
					// The unchanged pending Selection is not retried here.
					name: "backup gains carrier, primary still preferred",
					do:   func(h *harness) { h.Link(carrier("wwan0", true)) },
				},
				{
					name: "primary loses carrier before it ever committed",
					do:   func(h *harness) { h.Link(carrier("eth0", false)) },
					// An up: the uncommitted primary was never vacated.
					want: []string{
						"route home v4 wwan0 via 198.51.100.1",
						"route home v6 wwan0 via 2001:db8:1::1",
						"hook up home →backup",
					},
					active: "backup",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				assertDecisionTimes(tb, h, false)
				if got := h.State().Groups["home"].DecisionsTotal; got != 2 {
					tb.Errorf("decisionsTotal = %d, want 2", got)
				}
			},
		},
		{
			name: "a missing Gateway commits first and refreshes later",
			steps: []step{
				{
					name: "only primary's v4 Gateway is known",
					do:   func(h *harness) { h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.1")) },
				},
				{
					name: "primary gains carrier",
					do:   func(h *harness) { h.Link(carrier("eth0", true)) },
					want: []string{
						"route home v4 eth0 via 192.0.2.1",
						"hook up home →primary",
					},
					active: "primary",
				},
				{
					name:   "the v6 Gateway appears",
					do:     func(h *harness) { h.Route(gateway("eth0", probe.FamilyV6, "2001:db8::1")) },
					want:   []string{"route home v6 eth0 via 2001:db8::1"},
					active: "primary",
				},
				{
					name:   "the same v6 Gateway again",
					do:     func(h *harness) { h.Route(gateway("eth0", probe.FamilyV6, "2001:db8::1")) },
					active: "primary",
				},
			},
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				if got := h.State().Groups["home"].DecisionsTotal; got != 1 {
					tb.Errorf("decisionsTotal = %d, want 1", got)
				}
				if got := len(h.hooks()); got != 1 {
					tb.Errorf("%d Hooks delivered, want 1", got)
				}
			},
		},
		{
			name: "a WAN in two Groups commits in one while the other's Apply fails",
			config: func(cfg *config.Config) {
				cfg.Groups["guest"] = selector.Group{
					Name: "guest", Strategy: "primary-backup", Table: 200, Mark: 0x200,
					Members: map[string]selector.Member{"backup": {Wan: "backup", Priority: 1}},
				}
			},
			steps: []step{
				{
					name: "gateways cached, primary's routes fail",
					do: func(h *harness) {
						cachedGateways(h)
						h.kernel.fail(opRoute, "eth0")
					},
				},
				{
					name: "primary gains carrier: only home holds it",
					do:   func(h *harness) { h.Link(carrier("eth0", true)) },
					want: []string{
						"route home v4 eth0 via 192.0.2.1 ✗",
						"route home v6 eth0 via 2001:db8::1 ✗",
					},
				},
				{
					// home keeps its pending primary and does not retry it.
					name: "backup gains carrier: guest commits, home stays pending",
					do:   func(h *harness) { h.Link(carrier("wwan0", true)) },
					want: []string{
						"route guest v4 wwan0 via 198.51.100.1",
						"route guest v6 wwan0 via 2001:db8:1::1",
						"hook up guest →backup",
					},
				},
				{
					name: "primary's next probe cycle retries home only",
					do: func(h *harness) {
						h.kernel.heal(opRoute, "eth0")
						h.Probe(probe.ProbeResult{Wan: "primary", Family: probe.FamilyV4})
					},
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
				guest := h.State().Groups["guest"]
				if guest.Active == nil || *guest.Active != "backup" || guest.DecisionsTotal != 1 {
					tb.Errorf("guest State = %+v, want backup after 1 Decision", guest)
				}
				for _, g := range []struct{ group, wan string }{{"home", "primary"}, {"guest", "backup"}} {
					if v := readGauge(tb, h.metrics.GroupActive.WithLabelValues(g.group, g.wan)); v != 1 {
						tb.Errorf("group_active{%s,%s} = %v, want 1", g.group, g.wan, v)
					}
				}
			},
		},
		{
			name: "a shared interface's link change makes no intermediate Decisions",
			config: func(cfg *config.Config) {
				backup := cfg.Wans["backup"]
				backup.Interface = "eth0"
				cfg.Wans["backup"] = backup
			},
			// Repeated flaps exercise both orders in which the daemon
			// can visit the two WANs on eth0.
			steps: append([]step{{
				name: "eth0 Gateways cached",
				do: func(h *harness) {
					h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.1"))
					h.Route(gateway("eth0", probe.FamilyV6, "2001:db8::1"))
				},
			}}, linkFlaps(16)...),
			check: func(tb testing.TB, h *harness) {
				tb.Helper()
				if got := h.State().Groups["home"].DecisionsTotal; got != 32 {
					tb.Errorf("decisionsTotal = %d, want 32", got)
				}
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
			cfg := baseConfig()
			if tc.config != nil {
				tc.config(cfg)
			}
			h := startHarness(t, cfg)
			h.Trace() // the bootstrap rules belong to the startup tests
			runSteps(t, h, tc.steps)
			tc.check(t, h)
		})
	}
}
