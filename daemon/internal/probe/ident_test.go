package probe

import (
	"fmt"
	"testing"
)

func TestAllocateIdentsAssignsUniqueValues(t *testing.T) {
	t.Parallel()
	keys := []IdentKey{
		{Wan: "primary", Family: FamilyV4},
		{Wan: "primary", Family: FamilyV6},
		{Wan: "backup", Family: FamilyV4},
		{Wan: "backup", Family: FamilyV6},
	}
	got, err := AllocateIdents(keys)
	if err != nil {
		t.Fatalf("AllocateIdents: %v", err)
	}
	if len(got) != len(keys) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(keys))
	}
	seen := make(map[uint16]IdentKey)
	for k, v := range got {
		if owner, dup := seen[v]; dup {
			t.Errorf("ident %d assigned to both %+v and %+v", v, owner, k)
		}
		seen[v] = k
	}
}

func TestAllocateIdentsIsDeterministic(t *testing.T) {
	t.Parallel()
	keys := []IdentKey{
		{Wan: "primary", Family: FamilyV4},
		{Wan: "backup", Family: FamilyV6},
	}
	first, err := AllocateIdents(keys)
	if err != nil {
		t.Fatalf("AllocateIdents: %v", err)
	}
	second, err := AllocateIdents(keys)
	if err != nil {
		t.Fatalf("AllocateIdents (rerun): %v", err)
	}
	for k, v1 := range first {
		v2 := second[k]
		if v1 != v2 {
			t.Errorf("non-deterministic: %+v got %d then %d", k, v1, v2)
		}
	}
}

func TestAllocateIdentsRejectsDuplicates(t *testing.T) {
	t.Parallel()
	keys := []IdentKey{
		{Wan: "primary", Family: FamilyV4},
		{Wan: "primary", Family: FamilyV4}, // exact dup
	}
	if _, err := AllocateIdents(keys); err == nil {
		t.Error("AllocateIdents(duplicate) = nil err, want error")
	}
}

func TestAllocateIdentsHandlesHashCollision(t *testing.T) {
	t.Parallel()
	// Build enough keys that the SHA-256 initial slot must collide
	// somewhere — linear probe needs to hand the colliders distinct
	// idents. 2048 keys in a 16-bit space puts the no-collision
	// probability at ~e^-32 (≈1.3e-14), so the "if taken { continue }"
	// branch is exercised on every run. The previous 256-key count
	// only triggered a collision ~30% of the time — the test asserted
	// the *output* property (no duplicates) but didn't always reach
	// the probe-displacement code path.
	keys := make([]IdentKey, 2048)
	for i := range keys {
		keys[i] = IdentKey{Wan: fmt.Sprintf("wan-%d", i), Family: FamilyV4}
	}
	got, err := AllocateIdents(keys)
	if err != nil {
		t.Fatalf("AllocateIdents: %v", err)
	}
	seen := make(map[uint16]struct{}, len(keys))
	for _, v := range got {
		if _, dup := seen[v]; dup {
			t.Errorf("collision survived linear probe: ident %d", v)
		}
		seen[v] = struct{}{}
	}
	// Belt-and-braces: at least one key must have ended up at an
	// ident different from its initialIdent (i.e. probe-displaced),
	// confirming the test actually reached the displacement branch
	// rather than getting lucky on a hash run.
	displaced := 0
	for k, v := range got {
		if v != initialIdent(k) {
			displaced++
		}
	}
	if displaced == 0 {
		t.Errorf("no displaced idents in %d-key allocation — probe branch unreachable", len(keys))
	}
}

func TestAllocateIdentsRejectsOversizedKeyset(t *testing.T) {
	t.Parallel()
	// 65537 keys exceed the 16-bit identifier space — fail fast at
	// startup, never silently reuse.
	keys := make([]IdentKey, identSpace+1)
	for i := range keys {
		keys[i] = IdentKey{Wan: fmt.Sprintf("w-%d", i), Family: FamilyV4}
	}
	if _, err := AllocateIdents(keys); err == nil {
		t.Error("AllocateIdents(>65536 keys) = nil err, want error")
	}
}
