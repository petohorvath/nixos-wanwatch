package config

import (
	"slices"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// TestTargetsFamilies pins which Families a WAN serves: those whose
// Target bucket is non-empty, v4 first.
func TestTargetsFamilies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		t    Targets
		want []probe.Family
	}{
		{"empty", Targets{}, []probe.Family{}},
		{"v4 only", Targets{V4: []string{"192.0.2.1"}}, []probe.Family{probe.FamilyV4}},
		{"v6 only", Targets{V6: []string{"2001:db8::1"}}, []probe.Family{probe.FamilyV6}},
		{
			"both",
			Targets{V4: []string{"192.0.2.1"}, V6: []string{"2001:db8::1"}},
			[]probe.Family{probe.FamilyV4, probe.FamilyV6},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.t.Families(); !slices.Equal(got, tc.want) {
				t.Errorf("Families() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTargetsFor pins the per-Family bucket lookup, in config order.
func TestTargetsFor(t *testing.T) {
	t.Parallel()
	targets := Targets{
		V4: []string{"192.0.2.1", "198.51.100.1"},
		V6: []string{"2001:db8::1"},
	}
	cases := []struct {
		name   string
		t      Targets
		family probe.Family
		want   []string
	}{
		{"v4 bucket", targets, probe.FamilyV4, []string{"192.0.2.1", "198.51.100.1"}},
		{"v6 bucket", targets, probe.FamilyV6, []string{"2001:db8::1"}},
		{"empty v4", Targets{}, probe.FamilyV4, nil},
		{"empty v6", Targets{}, probe.FamilyV6, nil},
		{"unknown Family", targets, probe.Family(0), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.t.For(tc.family); !slices.Equal(got, tc.want) {
				t.Errorf("For(%v) = %v, want %v", tc.family, got, tc.want)
			}
		})
	}
}
