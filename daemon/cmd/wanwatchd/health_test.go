package main

import (
	"reflect"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// TestHealthPublication pins which ProbeResults republish State
// (docs/specs/daemon-state.md): a Family transition does even when WAN
// Health holds and no Decision follows, a partial Window does not, and
// each publication snapshots the Window statistics.
func TestHealthPublication(t *testing.T) {
	t.Parallel()
	partialStats := probe.FamilyStats{RTTMicros: 12_500, JitterMicros: 800, LossRatio: 0.05}
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
			v4:        state.FamilyHealth{LossRatio: 1, Targets: []string{"198.51.100.53"}},
		},
		{
			name:      "a Family flip under policy any republishes without a Decision",
			policy:    "any",
			result:    lossy("primary", probe.FamilyV4),
			active:    "primary",
			published: true,
			healthy:   true,
			v4:        state.FamilyHealth{LossRatio: 1, Targets: []string{"198.51.100.53"}},
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
				RTTSeconds:    0.0125,
				JitterSeconds: 0.0008,
				LossRatio:     0.05,
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
