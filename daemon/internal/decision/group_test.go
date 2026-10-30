package decision_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/decision"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type routeAttempt struct {
	wan      string
	families []probe.Family
}

// transition is a Commit's old and new Selection ("" for none) and the
// Event it is classified as.
type transition struct {
	old, next string
	event     state.Event
}

func groupConfig() selector.Group {
	return selector.Group{
		Name: "home", Strategy: "primary-backup", Table: 100, Mark: 0x100,
		Members: map[string]selector.Member{
			"primary": {Wan: "primary", Priority: 1},
			"backup":  {Wan: "backup", Priority: 2},
		},
	}
}

func memberHealth(healthy ...string) []selector.MemberHealth {
	var members []selector.MemberHealth
	for _, m := range groupConfig().Members {
		members = append(members, selector.MemberHealth{Member: m, Healthy: slices.Contains(healthy, m.Wan)})
	}
	return members
}

func active(wan string) selector.Active {
	return selector.Active{Wan: wan, Has: wan != ""}
}

func metricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	var m dto.Metric
	if err := metric.Write(&m); err != nil {
		t.Fatal(err)
	}
	if m.Gauge != nil {
		return m.GetGauge().GetValue()
	}
	return m.GetCounter().GetValue()
}

// Exercise complete event sequences through the same interface as the
// daemon. No assertion or setup reaches into pending Selection state.
func TestGroupProgression(t *testing.T) {
	t.Parallel()
	type step struct {
		name          string
		healthy       []string // Recompute unless probeWAN or routeWAN is set.
		probeWAN      string
		routeWAN      string
		family        probe.Family
		reason        string
		fail          bool
		wantSelection string
		wantDecisions int
		wantCommit    *transition
		wantRoutes    []routeAttempt
	}
	all := func(wan string) []routeAttempt { return []routeAttempt{{wan: wan}} }
	only := func(wan string, family probe.Family) []routeAttempt {
		return []routeAttempt{{wan, []probe.Family{family}}}
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{
			name: "cold start, switch, all-down and recovery",
			steps: []step{
				{name: "no healthy members"},
				{name: "no Selection to refresh", routeWAN: "primary", family: probe.FamilyV4},
				{name: "up", healthy: []string{"primary", "backup"}, reason: "carrier", wantSelection: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "unchanged", healthy: []string{"primary", "backup"}, wantSelection: "primary", wantDecisions: 1},
				{name: "switch", healthy: []string{"backup"}, wantSelection: "backup", wantDecisions: 2, wantCommit: &transition{"primary", "backup", state.EventSwitch}, wantRoutes: all("backup")},
				{name: "down retains routes", wantDecisions: 3, wantCommit: &transition{"backup", "", state.EventDown}},
				{name: "recovery", healthy: []string{"primary"}, wantSelection: "primary", wantDecisions: 4, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
			},
		},
		{
			name: "hard failure, retries and committed Gateway refresh",
			steps: []step{
				{name: "failed Apply", healthy: []string{"primary"}, fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "unchanged pending target", healthy: []string{"primary"}, wantDecisions: 1},
				{name: "unrelated Probe", probeWAN: "backup", wantDecisions: 1},
				{name: "failed Probe retry", probeWAN: "primary", fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "unrelated Gateway", routeWAN: "backup", family: probe.FamilyV4, wantDecisions: 1},
				{name: "failed Gateway retry uses all families", routeWAN: "primary", family: probe.FamilyV4, fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "Probe commits", probeWAN: "primary", wantSelection: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "committed Probe does nothing", probeWAN: "primary", wantSelection: "primary", wantDecisions: 1},
				{name: "v4 refresh", routeWAN: "primary", family: probe.FamilyV4, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV4)},
				{name: "failed v6 refresh", routeWAN: "primary", family: probe.FamilyV6, fail: true, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV6)},
				{name: "refresh failure creates no pending Decision, Probe repairs every family", probeWAN: "primary", wantSelection: "primary", wantDecisions: 1, wantRoutes: all("primary")},
				{name: "repaired Selection Probe does nothing", probeWAN: "primary", wantSelection: "primary", wantDecisions: 1},
			},
		},
		{
			name: "failed refresh retries on the selected WAN's Probe cycles",
			steps: []step{
				{name: "primary committed", healthy: []string{"primary", "backup"}, wantSelection: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "failed v4 refresh", routeWAN: "primary", family: probe.FamilyV4, fail: true, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV4)},
				{name: "unselected WAN Probe does not repair", probeWAN: "backup", wantSelection: "primary", wantDecisions: 1},
				{name: "failed repair", probeWAN: "primary", fail: true, wantSelection: "primary", wantDecisions: 1, wantRoutes: all("primary")},
				{name: "repair", probeWAN: "primary", wantSelection: "primary", wantDecisions: 1, wantRoutes: all("primary")},
				{name: "repaired Selection Probe does nothing", probeWAN: "primary", wantSelection: "primary", wantDecisions: 1},
			},
		},
		{
			name: "route observation refreshes its family, or every family after a failure",
			steps: []step{
				{name: "primary committed", healthy: []string{"primary"}, wantSelection: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "unchanged v4 observation refreshes v4", routeWAN: "primary", family: probe.FamilyV4, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV4)},
				{name: "unselected WAN observation does nothing", routeWAN: "backup", family: probe.FamilyV4, wantSelection: "primary", wantDecisions: 1},
				{name: "failed v6 refresh", routeWAN: "primary", family: probe.FamilyV6, fail: true, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV6)},
				{name: "failed repair on v4 observation", routeWAN: "primary", family: probe.FamilyV4, fail: true, wantSelection: "primary", wantDecisions: 1, wantRoutes: all("primary")},
				{name: "v4 observation repairs every family", routeWAN: "primary", family: probe.FamilyV4, wantSelection: "primary", wantDecisions: 1, wantRoutes: all("primary")},
				{name: "repaired: v4 observation refreshes v4", routeWAN: "primary", family: probe.FamilyV4, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV4)},
			},
		},
		{
			name: "a switch replaces a due Repair",
			steps: []step{
				{name: "primary committed", healthy: []string{"primary", "backup"}, wantSelection: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "failed v4 refresh", routeWAN: "primary", family: probe.FamilyV4, fail: true, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV4)},
				{name: "switch", healthy: []string{"backup"}, wantSelection: "backup", wantDecisions: 2, wantCommit: &transition{"primary", "backup", state.EventSwitch}, wantRoutes: all("backup")},
				{name: "switched Selection Probe does nothing", probeWAN: "backup", wantSelection: "backup", wantDecisions: 2},
				{name: "old Selection Probe does nothing", probeWAN: "primary", wantSelection: "backup", wantDecisions: 2},
			},
		},
		{
			name: "a pending switch holds a due Repair until its revert commits",
			steps: []step{
				{name: "primary committed", healthy: []string{"primary", "backup"}, wantSelection: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "failed v4 refresh", routeWAN: "primary", family: probe.FamilyV4, fail: true, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV4)},
				{name: "backup pending", healthy: []string{"backup"}, fail: true, wantSelection: "primary", wantDecisions: 2, wantRoutes: all("backup")},
				{name: "old Selection Probe does not repair", probeWAN: "primary", wantSelection: "primary", wantDecisions: 2},
				{name: "revert reapplies primary", healthy: []string{"primary"}, wantSelection: "primary", wantDecisions: 3, wantCommit: &transition{"primary", "primary", ""}, wantRoutes: all("primary")},
				{name: "reverted Selection Probe does nothing", probeWAN: "primary", wantSelection: "primary", wantDecisions: 3},
			},
		},
		{
			name: "a down drops a due Repair",
			steps: []step{
				{name: "primary committed", healthy: []string{"primary"}, wantSelection: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "failed v4 refresh", routeWAN: "primary", family: probe.FamilyV4, fail: true, wantSelection: "primary", wantDecisions: 1, wantRoutes: only("primary", probe.FamilyV4)},
				{name: "down retains routes", wantDecisions: 2, wantCommit: &transition{"primary", "", state.EventDown}},
				{name: "no Selection to repair", probeWAN: "primary", wantDecisions: 2},
				{name: "no Selection to refresh", routeWAN: "primary", family: probe.FamilyV4, wantDecisions: 2},
				{name: "recovery", healthy: []string{"primary"}, wantSelection: "primary", wantDecisions: 3, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "recovered Selection Probe does nothing", probeWAN: "primary", wantSelection: "primary", wantDecisions: 3},
			},
		},
		{
			name: "superseding target commits on Gateway discovery",
			steps: []step{
				{name: "primary pending", healthy: []string{"primary", "backup"}, fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "backup supersedes primary", healthy: []string{"backup"}, fail: true, wantDecisions: 2, wantRoutes: all("backup")},
				{name: "superseded Probe ignored", probeWAN: "primary", wantDecisions: 2},
				{name: "superseded Gateway ignored", routeWAN: "primary", family: probe.FamilyV4, wantDecisions: 2},
				{name: "backup commits", routeWAN: "backup", family: probe.FamilyV4, wantSelection: "backup", wantDecisions: 2, wantCommit: &transition{"", "backup", state.EventUp}, wantRoutes: all("backup")},
			},
		},
		{
			name: "failed switch preserves committed Selection",
			steps: []step{
				{name: "primary committed", healthy: []string{"primary"}, wantSelection: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary", state.EventUp}, wantRoutes: all("primary")},
				{name: "backup pending", healthy: []string{"backup"}, fail: true, wantSelection: "primary", wantDecisions: 2, wantRoutes: all("backup")},
				{name: "old Selection Gateway ignored", routeWAN: "primary", family: probe.FamilyV6, wantSelection: "primary", wantDecisions: 2},
				{name: "old Selection Probe ignored", probeWAN: "primary", wantSelection: "primary", wantDecisions: 2},
				{name: "recovery reapplies primary over the partial switch", healthy: []string{"primary"}, wantSelection: "primary", wantDecisions: 3, wantCommit: &transition{"primary", "primary", ""}, wantRoutes: all("primary")},
				{name: "superseded backup Probe ignored", probeWAN: "backup", wantSelection: "primary", wantDecisions: 3},
			},
		},
		{
			name: "all-down supersedes an uncommitted target",
			steps: []step{
				{name: "primary pending", healthy: []string{"primary"}, fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "all-down", wantDecisions: 2, wantCommit: &transition{"", "", ""}},
				{name: "superseded Probe ignored", probeWAN: "primary", wantDecisions: 2},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mreg := metrics.New()
			var attempts []routeAttempt
			var fail bool
			g := decision.New(groupConfig(), func(ctx context.Context, wan string, families ...probe.Family) error {
				if ctx != t.Context() {
					t.Error("Apply did not receive the caller's context")
				}
				attempts = append(attempts, routeAttempt{wan, slices.Clone(families)})
				if fail {
					return errors.New("route write failed")
				}
				return nil
			}, mreg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			var since *time.Time
			counts := map[string]int{"health": 0, "carrier": 0}
			for _, s := range tc.steps {
				attempts = nil
				fail = s.fail
				before := g.Snapshot()
				reason := s.reason
				if reason == "" {
					reason = "health"
				}
				var got *decision.Commit
				started := time.Now()
				switch {
				case s.probeWAN != "":
					got = g.Probe(t.Context(), s.probeWAN)
				case s.routeWAN != "":
					got = g.RouteObserved(t.Context(), s.routeWAN, s.family)
				default:
					got = g.Recompute(t.Context(), memberHealth(s.healthy...), reason)
				}
				if s.wantCommit == nil {
					if got != nil {
						t.Fatalf("%s: unexpected commit %+v", s.name, got)
					}
				} else {
					if got == nil || got.Old != active(s.wantCommit.old) || got.New != active(s.wantCommit.next) || got.Event() != s.wantCommit.event {
						t.Fatalf("%s: commit = %+v, want %+v", s.name, got, s.wantCommit)
					}
					if got.At.Before(started) || got.At.After(time.Now()) || got.At.Location() != time.UTC {
						t.Errorf("%s: invalid commit timestamp %v", s.name, got.At)
					}
					// Every Selection change restamps, a down included;
					// a commit that keeps its Selection does not.
					if s.wantCommit.event != "" {
						since = &got.At
					}
				}
				if !reflect.DeepEqual(attempts, s.wantRoutes) {
					t.Errorf("%s: route attempts = %+v, want %+v", s.name, attempts, s.wantRoutes)
				}
				snap := g.Snapshot()
				wan := ""
				if snap.Selection != nil {
					wan = *snap.Selection
				}
				if wan != s.wantSelection || snap.DecisionsTotal != s.wantDecisions || snap.Strategy != "primary-backup" || !reflect.DeepEqual(snap.SelectionSince, since) {
					t.Errorf("%s: snapshot = %+v (Selection %q), want Selection %q, %d Decisions, since %v", s.name, snap, wan, s.wantSelection, s.wantDecisions, since)
				}
				counts[reason] += s.wantDecisions - before.DecisionsTotal
				for label, want := range counts {
					if value := metricValue(t, mreg.GroupDecisions.WithLabelValues("home", label)); value != float64(want) {
						t.Errorf("%s: Decisions{%s} = %v, want %d", s.name, label, value, want)
					}
				}
				for _, member := range groupConfig().Members {
					want := 0.0
					if member.Wan == s.wantSelection {
						want = 1
					}
					if value := metricValue(t, mreg.GroupSelected.WithLabelValues("home", member.Wan)); value != want {
						t.Errorf("%s: selected{%s} = %v, want %v", s.name, member.Wan, value, want)
					}
				}
			}
		})
	}
}

func TestCommitEvent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		old, next string
		want      state.Event
	}{
		{"none to WAN is up", "", "primary", state.EventUp},
		{"WAN to none is down", "primary", "", state.EventDown},
		{"WAN to another WAN is switch", "primary", "backup", state.EventSwitch},
		{"same WAN is unchanged", "primary", "primary", ""},
		{"none to none is unchanged", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := decision.Commit{Old: active(tc.old), New: active(tc.next)}
			if got := c.Event(); got != tc.want {
				t.Errorf("Commit{%q→%q}.Event() = %q, want %q", tc.old, tc.next, got, tc.want)
			}
		})
	}
}

func TestUnknownStrategyDoesNotApply(t *testing.T) {
	t.Parallel()
	cfg := groupConfig()
	cfg.Strategy = "unknown"
	mreg := metrics.New()
	g := decision.New(cfg, func(context.Context, string, ...probe.Family) error {
		t.Fatal("Apply called for an unknown Strategy")
		return nil
	}, mreg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	before := g.Snapshot()
	if got := g.Recompute(t.Context(), memberHealth("primary"), "health"); got != nil {
		t.Fatalf("commit = %+v, want nil", got)
	}
	if !reflect.DeepEqual(g.Snapshot(), before) {
		t.Errorf("unknown Strategy changed State: %+v", g.Snapshot())
	}
	if got := metricValue(t, mreg.GroupDecisions.WithLabelValues("home", "health")); got != 0 {
		t.Errorf("Decisions = %v, want 0", got)
	}
}

func TestSnapshotDoesNotExposeMutableSelection(t *testing.T) {
	t.Parallel()
	g := decision.New(groupConfig(), func(context.Context, string, ...probe.Family) error { return nil },
		metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	committed := g.Recompute(t.Context(), memberHealth("primary"), "health")
	if committed == nil {
		t.Fatal("expected cold-start commit")
	}
	snap := g.Snapshot()
	*snap.Selection = "backup"
	*snap.SelectionSince = time.Time{}
	snap.DecisionsTotal = 99
	got := g.Snapshot()
	if got.Selection == nil || *got.Selection != "primary" || got.SelectionSince == nil || !got.SelectionSince.Equal(committed.At) || got.DecisionsTotal != 1 {
		t.Errorf("caller mutated Selection through snapshot: %+v", got)
	}
}
