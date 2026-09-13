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
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type routeAttempt struct {
	wan      string
	families []probe.Family
}

type transition struct{ old, next string }

func groupConfig() selector.Group {
	return selector.Group{
		Name: "home", Strategy: "primary-backup", Table: 100, Mark: 0x100,
		Members: []selector.Member{{Wan: "primary", Priority: 1}, {Wan: "backup", Priority: 2}},
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
		healthy       []string // Recompute unless probeWAN or gatewayWAN is set.
		probeWAN      string
		gatewayWAN    string
		family        probe.Family
		reason        string
		fail          bool
		wantActive    string
		wantDecisions int
		wantCommit    *transition
		wantRoutes    []routeAttempt
	}
	all := func(wan string) []routeAttempt { return []routeAttempt{{wan: wan}} }
	cases := []struct {
		name  string
		steps []step
	}{
		{
			name: "cold start, switch, all-down and recovery",
			steps: []step{
				{name: "no healthy members"},
				{name: "no Selection to refresh", gatewayWAN: "primary", family: probe.FamilyV4},
				{name: "up", healthy: []string{"primary", "backup"}, reason: "carrier", wantActive: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary"}, wantRoutes: all("primary")},
				{name: "unchanged", healthy: []string{"primary", "backup"}, wantActive: "primary", wantDecisions: 1},
				{name: "switch", healthy: []string{"backup"}, wantActive: "backup", wantDecisions: 2, wantCommit: &transition{"primary", "backup"}, wantRoutes: all("backup")},
				{name: "down retains routes", wantDecisions: 3, wantCommit: &transition{"backup", ""}},
				{name: "recovery", healthy: []string{"primary"}, wantActive: "primary", wantDecisions: 4, wantCommit: &transition{"", "primary"}, wantRoutes: all("primary")},
			},
		},
		{
			name: "hard failure, retries and committed Gateway refresh",
			steps: []step{
				{name: "failed Apply", healthy: []string{"primary"}, fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "unchanged pending target", healthy: []string{"primary"}, wantDecisions: 1},
				{name: "unrelated Probe", probeWAN: "backup", wantDecisions: 1},
				{name: "failed Probe retry", probeWAN: "primary", fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "unrelated Gateway", gatewayWAN: "backup", family: probe.FamilyV4, wantDecisions: 1},
				{name: "failed Gateway retry uses all families", gatewayWAN: "primary", family: probe.FamilyV4, fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "Probe commits", probeWAN: "primary", wantActive: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary"}, wantRoutes: all("primary")},
				{name: "committed Probe does nothing", probeWAN: "primary", wantActive: "primary", wantDecisions: 1},
				{name: "v4 refresh", gatewayWAN: "primary", family: probe.FamilyV4, wantActive: "primary", wantDecisions: 1, wantRoutes: []routeAttempt{{"primary", []probe.Family{probe.FamilyV4}}}},
				{name: "failed v6 refresh", gatewayWAN: "primary", family: probe.FamilyV6, fail: true, wantActive: "primary", wantDecisions: 1, wantRoutes: []routeAttempt{{"primary", []probe.Family{probe.FamilyV6}}}},
				{name: "refresh failure creates no pending Decision", probeWAN: "primary", wantActive: "primary", wantDecisions: 1},
			},
		},
		{
			name: "superseding target commits on Gateway discovery",
			steps: []step{
				{name: "primary pending", healthy: []string{"primary", "backup"}, fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "backup supersedes primary", healthy: []string{"backup"}, fail: true, wantDecisions: 2, wantRoutes: all("backup")},
				{name: "superseded Probe ignored", probeWAN: "primary", wantDecisions: 2},
				{name: "superseded Gateway ignored", gatewayWAN: "primary", family: probe.FamilyV4, wantDecisions: 2},
				{name: "backup commits", gatewayWAN: "backup", family: probe.FamilyV4, wantActive: "backup", wantDecisions: 2, wantCommit: &transition{"", "backup"}, wantRoutes: all("backup")},
			},
		},
		{
			name: "failed switch preserves committed Selection",
			steps: []step{
				{name: "primary committed", healthy: []string{"primary"}, wantActive: "primary", wantDecisions: 1, wantCommit: &transition{"", "primary"}, wantRoutes: all("primary")},
				{name: "backup pending", healthy: []string{"backup"}, fail: true, wantActive: "primary", wantDecisions: 2, wantRoutes: all("backup")},
				{name: "old Selection Gateway ignored", gatewayWAN: "primary", family: probe.FamilyV6, wantActive: "primary", wantDecisions: 2},
				{name: "old Selection Probe ignored", probeWAN: "primary", wantActive: "primary", wantDecisions: 2},
				{name: "recovery reapplies primary to repair partial switch", healthy: []string{"primary"}, wantActive: "primary", wantDecisions: 3, wantCommit: &transition{"primary", "primary"}, wantRoutes: all("primary")},
				{name: "superseded backup Probe ignored", probeWAN: "backup", wantActive: "primary", wantDecisions: 3},
			},
		},
		{
			name: "all-down supersedes an uncommitted target",
			steps: []step{
				{name: "primary pending", healthy: []string{"primary"}, fail: true, wantDecisions: 1, wantRoutes: all("primary")},
				{name: "all-down", wantDecisions: 2, wantCommit: &transition{"", ""}},
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
				case s.gatewayWAN != "":
					got = g.GatewayChanged(t.Context(), s.gatewayWAN, s.family)
				default:
					got = g.Recompute(t.Context(), memberHealth(s.healthy...), reason)
				}
				if s.wantCommit == nil {
					if got != nil {
						t.Fatalf("%s: unexpected commit %+v", s.name, got)
					}
				} else {
					if got == nil || got.Old != active(s.wantCommit.old) || got.New != active(s.wantCommit.next) {
						t.Fatalf("%s: commit = %+v, want %+v", s.name, got, s.wantCommit)
					}
					if got.At.Before(started) || got.At.After(time.Now()) || got.At.Location() != time.UTC {
						t.Errorf("%s: invalid commit timestamp %v", s.name, got.At)
					}
					if got.New.Has {
						since = &got.At
					}
				}
				if !reflect.DeepEqual(attempts, s.wantRoutes) {
					t.Errorf("%s: route attempts = %+v, want %+v", s.name, attempts, s.wantRoutes)
				}
				snap := g.Snapshot()
				wan := ""
				if snap.Active != nil {
					wan = *snap.Active
				}
				if wan != s.wantActive || snap.DecisionsTotal != s.wantDecisions || snap.Strategy != "primary-backup" || !reflect.DeepEqual(snap.ActiveSince, since) {
					t.Errorf("%s: snapshot = %+v (active %q), want active %q, %d Decisions, since %v", s.name, snap, wan, s.wantActive, s.wantDecisions, since)
				}
				counts[reason] += s.wantDecisions - before.DecisionsTotal
				for label, want := range counts {
					if value := metricValue(t, mreg.GroupDecisions.WithLabelValues("home", label)); value != float64(want) {
						t.Errorf("%s: Decisions{%s} = %v, want %d", s.name, label, value, want)
					}
				}
				for _, member := range groupConfig().Members {
					want := 0.0
					if member.Wan == s.wantActive {
						want = 1
					}
					if value := metricValue(t, mreg.GroupActive.WithLabelValues("home", member.Wan)); value != want {
						t.Errorf("%s: active{%s} = %v, want %v", s.name, member.Wan, value, want)
					}
				}
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
	*snap.Active = "backup"
	*snap.ActiveSince = time.Time{}
	snap.DecisionsTotal = 99
	got := g.Snapshot()
	if got.Active == nil || *got.Active != "primary" || got.ActiveSince == nil || !got.ActiveSince.Equal(committed.At) || got.DecisionsTotal != 1 {
		t.Errorf("caller mutated Selection through snapshot: %+v", got)
	}
}
