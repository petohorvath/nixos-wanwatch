package main

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
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
			assertTrace(t, got, want)

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
				g, ok := s.Groups[name]
				if !ok {
					t.Errorf("State has no Group %q", name)
					continue
				}
				if g.Active != nil {
					t.Errorf("Group %q Selection = %q at startup, want none", name, *g.Active)
				}
			}
			if n := readCounter(t, h.metrics.StatePublications); n != 1 {
				t.Errorf("state_publications_total = %v, want 1", n)
			}
		})
	}
}

// TestStartupLinkHistory replays the LinkEvents the link subscriber
// queues at startup, once it has reconciled notifications that raced
// the link snapshot (internal/rtnl tests that reconciliation). Each
// event yields a trace and a Selection.
func TestStartupLinkHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{
			// vm-recovery: the snapshot reads eth0 up and wwan0 down;
			// wwan0 then gains carrier and eth0 loses it.
			name: "backup gains carrier during snapshot",
			steps: []step{
				{name: "snapshot: eth0 up", do: link("eth0", true), want: []string{"hook up home →primary"}, active: "primary"},
				{name: "snapshot: wwan0 down", do: link("wwan0", false), active: "primary"},
				{name: "queued: wwan0 up", do: link("wwan0", true), active: "primary"},
				{name: "queued: eth0 down", do: link("eth0", false), want: []string{"hook switch home primary→backup"}, active: "backup"},
			},
		},
		{
			// eth0's down/up flap predates the snapshot, which already
			// reads it up, so the subscriber queues no flap.
			name: "primary flaps before snapshot",
			steps: []step{
				{name: "snapshot: eth0 up", do: link("eth0", true), want: []string{"hook up home →primary"}, active: "primary"},
				{name: "snapshot: wwan0 up", do: link("wwan0", true), active: "primary"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startHarness(t, baseConfig())
			h.Trace()
			runSteps(t, h, tc.steps)
		})
	}
}

// TestStartupRouteHistory replays the RouteEvents the route subscriber
// queues at startup when obsolete history (an old Gateway's add and
// delete, then the current Gateway's add) raced the route snapshot.
// The subscriber reconciles each notification to the current Gateway
// (internal/rtnl tests that), so only the current Gateway reaches
// Apply and State, whether the link or the route snapshot is handled
// first.
func TestStartupRouteHistory(t *testing.T) {
	t.Parallel()
	for _, fam := range probe.AllFamilies {
		current := "192.0.2.3"
		if fam == probe.FamilyV6 {
			current = "2001:db8::3"
		}
		route := "route home " + fam.String() + " eth0 via " + current
		reconciled := func(h *harness) { h.Route(gateway("eth0", fam, current)) }
		for _, tc := range []struct {
			name  string
			steps []step
		}{
			{
				name: "carrier first",
				steps: []step{
					{name: "eth0 up", do: link("eth0", true), want: []string{"hook up home →primary"}, active: "primary"},
					{name: "snapshot: current Gateway", do: reconciled, want: []string{route}, active: "primary"},
					{name: "queued: old Gateway added", do: reconciled, active: "primary"},
					{name: "queued: old Gateway deleted", do: reconciled, active: "primary"},
					{name: "queued: current Gateway added", do: reconciled, active: "primary"},
				},
			},
			{
				name: "routes first",
				steps: []step{
					{name: "snapshot: current Gateway", do: reconciled},
					{name: "queued: old Gateway added", do: reconciled},
					{name: "queued: old Gateway deleted", do: reconciled},
					{name: "queued: current Gateway added", do: reconciled},
					{name: "eth0 up", do: link("eth0", true), want: []string{route, "hook up home →primary"}, active: "primary"},
				},
			},
		} {
			t.Run(fam.String()+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				h := startHarness(t, baseConfig())
				h.Trace()
				runSteps(t, h, tc.steps)
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
}

// link is a step action delivering iface's carrier.
func link(iface string, up bool) func(*harness) {
	return func(h *harness) {
		h.tb.Helper()
		h.Link(carrier(iface, up))
	}
}
