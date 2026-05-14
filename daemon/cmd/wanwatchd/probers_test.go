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

func TestTargetsForFiltersByFamily(t *testing.T) {
	t.Parallel()
	wan := config.Wan{
		Probe: config.Probe{
			Targets: []string{"1.1.1.1", "2606:4700:4700::1111", "8.8.8.8", "not-an-ip"},
		},
	}

	v4 := targetsFor(wan, probe.FamilyV4)
	want4 := []string{"1.1.1.1", "8.8.8.8"}
	if !equalUnorderedStrings(v4, want4) {
		t.Errorf("targetsFor(v4) = %v, want %v", v4, want4)
	}

	v6 := targetsFor(wan, probe.FamilyV6)
	want6 := []string{"2606:4700:4700::1111"}
	if !equalUnorderedStrings(v6, want6) {
		t.Errorf("targetsFor(v6) = %v, want %v", v6, want6)
	}
}

func TestTargetsForEmpty(t *testing.T) {
	t.Parallel()
	wan := config.Wan{Probe: config.Probe{}}
	if got := targetsFor(wan, probe.FamilyV4); len(got) != 0 {
		t.Errorf("targetsFor on empty Targets = %v, want []", got)
	}
}

func TestTargetsForAllInvalid(t *testing.T) {
	t.Parallel()
	// Non-IP strings shouldn't crash and shouldn't be emitted.
	wan := config.Wan{Probe: config.Probe{Targets: []string{"not-ip", "also.not"}}}
	if got := targetsFor(wan, probe.FamilyV4); len(got) != 0 {
		t.Errorf("targetsFor on non-IP input = %v, want []", got)
	}
}

// TestFamiliesFromTargetsSkipsNonIP: non-IP strings in the
// targets list are silently dropped — the config layer should
// have rejected them already, but the daemon doesn't trust its
// input and must not crash on garbage.
func TestFamiliesFromTargetsSkipsNonIP(t *testing.T) {
	t.Parallel()
	got := familiesFromTargets([]string{"not-an-ip", "1.1.1.1", "also.not"})
	if !got.v4 {
		t.Error("v4 = false; want true (1.1.1.1 should still count)")
	}
	if got.v6 {
		t.Error("v6 = true; want false (no v6 literal)")
	}
}

func TestFamiliesFromTargetsAllInvalid(t *testing.T) {
	t.Parallel()
	got := familiesFromTargets([]string{"", "abc", "256.256.256.256"})
	if got.v4 || got.v6 {
		t.Errorf("got = %+v, want both false (no valid IPs)", got)
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
