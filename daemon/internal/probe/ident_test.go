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
	// Build enough keys that two of them collide on the SHA-256
	// initial slot — linear probe must hand them distinct idents.
	keys := make([]IdentKey, 256)
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
