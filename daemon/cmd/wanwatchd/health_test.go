package main

import (
	"reflect"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// TestHealthPublication pins which ProbeResults republish State
// (docs/specs/daemon-state.md): a Family transition does even when WAN
// Health holds and no Decision follows, a partial Window does not, and
// each publication snapshots the Window statistics.
func TestHealthPublication(t *testing.T) {
	t.Parallel()
	partialStats := probe.FamilyStats{RTTMicros: ptr[uint64](12_500), JitterMicros: ptr[uint64](800), LossRatio: ptr(0.05)}
	fullStats := partialStats
	fullStats.WindowFilled = true
	for _, tc := range []struct {
		name      string
		policy    string // primary's familyHealthPolicy
		result    probe.ProbeResult
		want      []string
		active    string
		published bool
		healthy   bool               // primary's WAN Health in State
		v4        state.FamilyHealth // primary's v4 Family in State
	}{
		{
			name:      "a WAN Health flip switches the Group",
			policy:    "all",
			result:    lossy("primary", probe.FamilyV4),
			want:      []string{"hook switch home primary→backup"},
			active:    "backup",
			published: true,
			v4:        state.FamilyHealth{LossRatio: ptr(1.0), Targets: []string{"198.51.100.53"}},
		},
		{
			name:      "a Family flip under policy any republishes without a Decision",
			policy:    "any",
			result:    lossy("primary", probe.FamilyV4),
			active:    "primary",
			published: true,
			healthy:   true,
			v4:        state.FamilyHealth{LossRatio: ptr(1.0), Targets: []string{"198.51.100.53"}},
		},
		{
			name:    "a partial Window publishes nothing",
			policy:  "all",
			result:  probe.ProbeResult{Wan: "primary", Family: probe.FamilyV4, Stats: partialStats},
			active:  "primary",
			healthy: true,
			v4:      state.FamilyHealth{Targets: []string{"198.51.100.53"}},
		},
		{
			name:      "a first full Window publishes its statistics",
			policy:    "all",
			result:    probe.ProbeResult{Wan: "primary", Family: probe.FamilyV4, Stats: fullStats},
			active:    "primary",
			published: true,
			healthy:   true,
			v4: state.FamilyHealth{
				Healthy:       true,
				RTTSeconds:    ptr(0.0125),
				JitterSeconds: ptr(0.0008),
				LossRatio:     ptr(0.05),
				Targets:       []string{"198.51.100.53"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig()
			primary := cfg.Wans["primary"]
			primary.Probe.FamilyHealthPolicy = tc.policy
			cfg.Wans["primary"] = primary
			h := startHarness(t, cfg)
			h.Link(carrier("eth0", true))
			h.Link(carrier("wwan0", true))
			h.Trace()
			before := readCounter(t, h.metrics.StatePublications)

			h.Probe(tc.result)

			assertTrace(t, "probe result", h.Trace(), tc.want)
			published := readCounter(t, h.metrics.StatePublications) > before
			if published != tc.published {
				t.Errorf("State republished = %v, want %v", published, tc.published)
			}
			s := h.State()
			if active := selectionOf(s, "home"); active != tc.active {
				t.Errorf("State Selection = %q, want %q", active, tc.active)
			}
			wan := s.Wans["primary"]
			if wan.Healthy != tc.healthy {
				t.Errorf("State Health of primary = %v, want %v", wan.Healthy, tc.healthy)
			}
			if got := wan.Families["v4"]; !reflect.DeepEqual(got, tc.v4) {
				t.Errorf("State v4 Family of primary = %+v, want %+v", got, tc.v4)
			}
		})
	}
}

// TestLinkStateMetrics pins the link-state encodings of docs/metrics.md:
// wanwatch_wan_carrier is 1, 0, or -1 for up, down, or unknown, and
// wanwatch_wan_operstate is a state-set with exactly one series at 1.
// A kernel operstate outside the known set reads as unknown, in the
// metrics and in State (docs/specs/daemon-state.md).
func TestLinkStateMetrics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		events      []rtnl.LinkEvent
		wantCarrier float64
		wantState   string
	}{
		{
			name:        "no link report since startup",
			wantCarrier: -1,
			wantState:   "unknown",
		},
		{
			name:        "carrier up",
			events:      []rtnl.LinkEvent{carrier("eth0", true)},
			wantCarrier: 1,
			wantState:   "up",
		},
		{
			name:        "carrier down",
			events:      []rtnl.LinkEvent{carrier("eth0", false)},
			wantCarrier: 0,
			wantState:   "down",
		},
		{
			name: "carrier unknown after up",
			events: []rtnl.LinkEvent{
				carrier("eth0", true),
				{Name: "eth0", Carrier: rtnl.CarrierUnknown, Operstate: rtnl.OperstateUnknown},
			},
			wantCarrier: -1,
			wantState:   "unknown",
		},
		{
			name:        "operstate dormant",
			events:      []rtnl.LinkEvent{{Name: "eth0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateDormant}},
			wantCarrier: 1,
			wantState:   "dormant",
		},
		{
			name: "out-of-range operstate after up",
			events: []rtnl.LinkEvent{
				carrier("eth0", true),
				{Name: "eth0", Carrier: rtnl.CarrierUp, Operstate: rtnl.Operstate(42)},
			},
			wantCarrier: 1,
			wantState:   "unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startHarness(t, baseConfig())
			for _, e := range tc.events {
				h.Link(e)
			}

			if v := readGauge(t, h.metrics.WanCarrier.WithLabelValues("primary")); v != tc.wantCarrier {
				t.Errorf("wanwatch_wan_carrier{wan=primary} = %v, want %v", v, tc.wantCarrier)
			}
			for _, st := range []string{"up", "down", "dormant", "lowerlayerdown", "notpresent", "testing", "unknown"} {
				want := 0.0
				if st == tc.wantState {
					want = 1
				}
				if v := readGauge(t, h.metrics.WanOperstate.WithLabelValues("primary", st)); v != want {
					t.Errorf("wanwatch_wan_operstate{wan=primary,state=%s} = %v, want %v", st, v, want)
				}
			}
			if got := h.State().Wans["primary"].Operstate; got != tc.wantState {
				t.Errorf("State operstate of primary = %q, want %q", got, tc.wantState)
			}
		})
	}
}
