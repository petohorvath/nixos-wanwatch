package main

import (
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

func TestIdentKeysForFromProbeTargets(t *testing.T) {
	t.Parallel()
	keys := identKeysFor(testCfg())
	// Sorted by wan name: backup (v4) < primary (v4, v6) ⇒ 3 keys.
	want := []probe.IdentKey{
		{Wan: "backup", Family: probe.FamilyV4},
		{Wan: "primary", Family: probe.FamilyV4},
		{Wan: "primary", Family: probe.FamilyV6},
	}
	if len(keys) != len(want) {
		t.Fatalf("len = %d, want %d (keys=%+v)", len(keys), len(want), keys)
	}
	for i, w := range want {
		if keys[i] != w {
			t.Errorf("keys[%d] = %+v, want %+v", i, keys[i], w)
		}
	}
}

func TestIdentKeysForIsDeterministic(t *testing.T) {
	t.Parallel()
	// Map iteration is randomized but identKeysFor must produce a
	// stable order so the ident allocation is reproducible across
	// restarts (PLAN §8).
	a := identKeysFor(testCfg())
	b := identKeysFor(testCfg())
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("a[%d]=%+v b[%d]=%+v", i, a[i], i, b[i])
		}
	}
}

func TestTargetsForReturnsPerFamilyBucket(t *testing.T) {
	t.Parallel()
	wan := config.Wan{
		Probe: config.Probe{
			Targets: config.Targets{
				V4: []string{"1.1.1.1", "8.8.8.8"},
				V6: []string{"2606:4700:4700::1111"},
			},
		},
	}

	if got := targetsFor(wan, probe.FamilyV4); !equalUnorderedStrings(got, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Errorf("targetsFor(v4) = %v, want [1.1.1.1 8.8.8.8]", got)
	}
	if got := targetsFor(wan, probe.FamilyV6); !equalUnorderedStrings(got, []string{"2606:4700:4700::1111"}) {
		t.Errorf("targetsFor(v6) = %v, want [2606:4700:4700::1111]", got)
	}
}

func TestTargetsForEmptyBucket(t *testing.T) {
	t.Parallel()
	wan := config.Wan{Probe: config.Probe{}}
	if got := targetsFor(wan, probe.FamilyV4); len(got) != 0 {
		t.Errorf("targetsFor on empty Targets = %v, want empty", got)
	}
	if got := targetsFor(wan, probe.FamilyV6); len(got) != 0 {
		t.Errorf("targetsFor on empty Targets = %v, want empty", got)
	}
}

func TestFamiliesFromTargets(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		t    config.Targets
		v4   bool
		v6   bool
	}{
		{"empty", config.Targets{}, false, false},
		{"v4 only", config.Targets{V4: []string{"1.1.1.1"}}, true, false},
		{"v6 only", config.Targets{V6: []string{"2606:4700:4700::1111"}}, false, true},
		{
			"both",
			config.Targets{V4: []string{"1.1.1.1"}, V6: []string{"2606:4700:4700::1111"}},
			true,
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := familiesFromTargets(tc.t)
			if got.v4 != tc.v4 || got.v6 != tc.v6 {
				t.Errorf("familiesFromTargets(%+v) = %+v, want {v4:%v v6:%v}", tc.t, got, tc.v4, tc.v6)
			}
		})
	}
}

// equalUnorderedStrings returns true if a and b contain the same
// elements regardless of order. targetsFor preserves the input
// order today, but asserting on order would couple the test to
// an internal detail that's not part of the contract.
func equalUnorderedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
