package main

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

// TestStartupInstallsFwmarkRules covers bootstrap: Start installs one
// fwmark rule per Group and Family, then publishes a State with no
// Selection. A failed rule install fails Start, is metered against its
// Group, and publishes no State.
func TestStartupInstallsFwmarkRules(t *testing.T) {
	t.Parallel()
	guest := selector.Group{
		Name: "guest", Strategy: "primary-backup", Table: 200, Mark: 0x200,
		Members: map[string]selector.Member{"backup": {Wan: "backup", Priority: 1}},
	}
	for _, tc := range []struct {
		name    string
		extra   []selector.Group // Groups added to baseConfig
		failFam probe.Family     // a Family whose rule install fails; 0 for none
		want    []string         // the Start trace, in any order across Groups
		wantErr bool
	}{
		{
			name: "one Group",
			want: []string{
				"rule v4 mark=0x100 table=100",
				"rule v6 mark=0x100 table=100",
			},
		},
		{
			name:  "two Groups",
			extra: []selector.Group{guest},
			want: []string{
				"rule v4 mark=0x100 table=100",
				"rule v6 mark=0x100 table=100",
				"rule v4 mark=0x200 table=200",
				"rule v6 mark=0x200 table=200",
			},
		},
		{
			name:    "v4 rule fails",
			failFam: probe.FamilyV4,
			want:    []string{"rule v4 mark=0x100 table=100 ✗"},
			wantErr: true,
		},
		{
			name:    "v6 rule fails",
			failFam: probe.FamilyV6,
			want: []string{
				"rule v4 mark=0x100 table=100",
				"rule v6 mark=0x100 table=100 ✗",
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig()
			for _, g := range tc.extra {
				cfg.Groups[g.Name] = g
			}
			h := newHarness(t, cfg)
			if tc.failFam != 0 {
				h.kernel.fail(opRule, "", tc.failFam)
			}

			err := h.Start()
			got, want := slices.Sorted(slices.Values(h.Trace())), slices.Sorted(slices.Values(tc.want))
			assertTrace(t, "Start", got, want)

			if tc.wantErr {
				if !errors.Is(err, errScripted) {
					t.Fatalf("Start = %v, want the rule install failure", err)
				}
				if n := readCounter(t, h.metrics.ApplyOpErrors.WithLabelValues("home", "rule_install")); n != 1 {
					t.Errorf("apply_op_errors_total{home,rule_install} = %v, want 1", n)
				}
				if _, err := os.Stat(h.statePath); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("State after a failed Start: stat = %v, want none published", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Start = %v, want nil", err)
			}
			s := h.State()
			for name := range cfg.Groups {
				if _, ok := s.Groups[name]; !ok {
					t.Errorf("State has no Group %q", name)
					continue
				}
				if got := selectionOf(s, name); got != "" {
					t.Errorf("Group %q Selection = %q at startup, want none", name, got)
				}
			}
			if n := readCounter(t, h.metrics.StatePublications); n != 1 {
				t.Errorf("state_publications_total = %v, want 1", n)
			}
		})
	}
}

// TestStartupSnapshot covers priming: the daemon folds the startup
// route snapshot into its Gateway cache, then the link snapshot into
// Health as one batch, before the event loop handles any event. Each
// Group therefore makes at most one Decision, onto its preferred
// healthy Member, with every Gateway known, whatever order the
// snapshots list the WANs in. State then shows every Gateway, even
// when no Decision fires.
func TestStartupSnapshot(t *testing.T) {
	t.Parallel()
	routes := []rtnl.RouteEvent{
		gateway("eth0", probe.FamilyV4, "192.0.2.1"),
		gateway("eth0", probe.FamilyV6, "2001:db8::1"),
		gateway("wwan0", probe.FamilyV4, "198.51.100.1"),
		gateway("wwan0", probe.FamilyV6, "2001:db8:1::1"),
	}
	onPrimary := []string{
		"route home v4 eth0 via 192.0.2.1",
		"route home v6 eth0 via 2001:db8::1",
		"hook up home →primary",
	}
	for _, tc := range []struct {
		name         string
		links        []rtnl.LinkEvent
		want         []string // the trace priming produces
		active       string
		publications float64 // State publications, including bootstrap's
	}{
		{
			name:         "both up, primary listed first",
			links:        []rtnl.LinkEvent{carrier("eth0", true), carrier("wwan0", true)},
			want:         onPrimary,
			active:       "primary",
			publications: 3,
		},
		{
			name:         "both up, backup listed first",
			links:        []rtnl.LinkEvent{carrier("wwan0", true), carrier("eth0", true)},
			want:         onPrimary,
			active:       "primary",
			publications: 3,
		},
		{
			name:  "only backup up",
			links: []rtnl.LinkEvent{carrier("eth0", false), carrier("wwan0", true)},
			want: []string{
				"route home v4 wwan0 via 198.51.100.1",
				"route home v6 wwan0 via 2001:db8:1::1",
				"hook up home →backup",
			},
			active:       "backup",
			publications: 3,
		},
		{
			name:         "no carrier",
			links:        []rtnl.LinkEvent{carrier("eth0", false), carrier("wwan0", false)},
			publications: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, baseConfig())
			if err := h.StartFrom(startupSnapshot{links: tc.links, routes: routes}); err != nil {
				t.Fatalf("StartFrom = %v, want nil", err)
			}
			assertTrace(t, "priming", primingTrace(h), tc.want)
			s := h.State()
			if active := selectionOf(s, "home"); active != tc.active {
				t.Errorf("State Selection = %q, want %q", active, tc.active)
			}
			for wan, want := range map[string][2]string{
				"primary": {"192.0.2.1", "2001:db8::1"},
				"backup":  {"198.51.100.1", "2001:db8:1::1"},
			} {
				gws := s.Wans[wan].Gateways
				if v4, v6 := gatewayText(gws.V4), gatewayText(gws.V6); v4 != want[0] || v6 != want[1] {
					t.Errorf("State %s Gateways = %s, %s; want %s, %s", wan, v4, v6, want[0], want[1])
				}
			}
			if n := readCounter(t, h.metrics.StatePublications); n != tc.publications {
				t.Errorf("state_publications_total = %v, want %v", n, tc.publications)
			}
		})
	}
}

// TestStartupPrimesBeforeReady covers readiness: run() primes before
// it sends READY=1, so units ordered after wanwatch find the startup
// Decision's routes and State in place. StartFrom mirrors run(), so
// its return, without a barrier, stands for READY.
func TestStartupPrimesBeforeReady(t *testing.T) {
	t.Parallel()
	h := newHarness(t, baseConfig())
	err := h.StartFrom(startupSnapshot{
		links:  []rtnl.LinkEvent{carrier("eth0", true)},
		routes: []rtnl.RouteEvent{gateway("eth0", probe.FamilyV4, "192.0.2.1")},
	})
	if err != nil {
		t.Fatalf("StartFrom = %v, want nil", err)
	}
	got := slices.DeleteFunc(h.Trace(), func(line string) bool {
		return strings.HasPrefix(line, "rule ")
	})
	assertTrace(t, "at READY", got, []string{
		"route home v4 eth0 via 192.0.2.1",
		"hook up home →primary",
	})
	if active := selectionOf(h.State(), "home"); active != "primary" {
		t.Errorf("State Selection at READY = %q, want %q", active, "primary")
	}
}

// TestStartupLinkHistory delivers the LinkEvents the link subscriber
// queues after its snapshot, once it has reconciled notifications that
// raced the snapshot (internal/rtnl tests that reconciliation). Each
// event yields a trace and a Selection.
func TestStartupLinkHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		snapshot []rtnl.LinkEvent
		want     []string // the trace priming produces
		active   string
		steps    []step
	}{
		{
			// vm-recovery: the snapshot reads eth0 up and wwan0 down;
			// wwan0 then gains carrier and eth0 loses it.
			name:     "backup gains carrier during snapshot",
			snapshot: []rtnl.LinkEvent{carrier("eth0", true), carrier("wwan0", false)},
			want:     []string{"hook up home →primary"},
			active:   "primary",
			steps: []step{
				{name: "queued: wwan0 up", do: link("wwan0", true), active: "primary"},
				{name: "queued: eth0 down", do: link("eth0", false), want: []string{"hook switch home primary→backup"}, active: "backup"},
			},
		},
		{
			// eth0's down/up flap predates the snapshot, which already
			// reads it up, so the subscriber queues no flap.
			name:     "primary flaps before snapshot",
			snapshot: []rtnl.LinkEvent{carrier("eth0", true), carrier("wwan0", true)},
			want:     []string{"hook up home →primary"},
			active:   "primary",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, baseConfig())
			if err := h.StartFrom(startupSnapshot{links: tc.snapshot}); err != nil {
				t.Fatalf("StartFrom = %v, want nil", err)
			}
			assertTrace(t, "priming", primingTrace(h), tc.want)
			if active := selectionOf(h.State(), "home"); active != tc.active {
				t.Errorf("State Selection after priming = %q, want %q", active, tc.active)
			}
			runSteps(t, h, tc.steps)
		})
	}
}

// TestStartupRouteHistory delivers the RouteEvents the route
// subscriber queues after its snapshot when obsolete history (an old
// Gateway's add and delete, then the current Gateway's add) raced the
// snapshot. The subscriber reconciles each notification to the current
// Gateway (internal/rtnl tests that), so only the current Gateway
// reaches Apply and State. Priming learns the Gateway before eth0's
// carrier selects primary, and each later observation on the selected
// WAN rewrites its route.
func TestStartupRouteHistory(t *testing.T) {
	t.Parallel()
	for _, fam := range probe.AllFamilies {
		current := "192.0.2.3"
		if fam == probe.FamilyV6 {
			current = "2001:db8::3"
		}
		route := "route home " + fam.String() + " eth0 via " + current
		reconciled := func(h *harness) { h.Route(gateway("eth0", fam, current)) }
		t.Run(fam.String(), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, baseConfig())
			err := h.StartFrom(startupSnapshot{
				links:  []rtnl.LinkEvent{carrier("eth0", true)},
				routes: []rtnl.RouteEvent{gateway("eth0", fam, current)},
			})
			if err != nil {
				t.Fatalf("StartFrom = %v, want nil", err)
			}
			want := []string{route, "hook up home →primary"}
			assertTrace(t, "priming", primingTrace(h), want)
			runSteps(t, h, []step{
				{name: "queued: old Gateway added", do: reconciled, want: []string{route}, active: "primary"},
				{name: "queued: old Gateway deleted", do: reconciled, want: []string{route}, active: "primary"},
				{name: "queued: current Gateway added", do: reconciled, want: []string{route}, active: "primary"},
			})
			wan := h.State().Wans["primary"]
			gw := wan.Gateways.V4
			if fam == probe.FamilyV6 {
				gw = wan.Gateways.V6
			}
			if got := gatewayText(gw); got != current {
				t.Errorf("State Gateway = %s, want %s", got, current)
			}
		})
	}
}

// primingTrace returns the trace since Start without bootstrap's
// fwmark rule installs, once priming has finished.
func primingTrace(h *harness) []string {
	h.tb.Helper()
	h.barrier()
	return slices.DeleteFunc(h.Trace(), func(line string) bool {
		return strings.HasPrefix(line, "rule ")
	})
}

// link is a step action delivering iface's carrier.
func link(iface string, up bool) func(*harness) {
	return func(h *harness) {
		h.tb.Helper()
		h.Link(carrier(iface, up))
	}
}
