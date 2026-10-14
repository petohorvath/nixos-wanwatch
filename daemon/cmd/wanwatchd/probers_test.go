package main

import (
	"testing"

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
	// restarts (docs/specs/failover.md).
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
