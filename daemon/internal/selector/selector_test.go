package selector

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestKnownStrategiesContainsPrimaryBackup(t *testing.T) {
	t.Parallel()
	names := KnownStrategies()
	if !slices.Contains(names, "primary-backup") {
		t.Errorf("KnownStrategies() = %v, want it to contain %q", names, "primary-backup")
	}
}

func TestApplyDispatchesToStrategy(t *testing.T) {
	t.Parallel()
	g := Group{Name: "home", Strategy: "primary-backup"}
	members := []MemberHealth{
		{Member: Member{Wan: "primary", Priority: 1}, Healthy: true},
	}
	sel, err := Apply(g, members)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if sel.Group != "home" {
		t.Errorf("sel.Group = %q, want %q", sel.Group, "home")
	}
	if sel.Active == nil || *sel.Active != "primary" {
		t.Errorf("sel.Active = %v, want pointer to %q", sel.Active, "primary")
	}
}

func TestApplyRejectsUnknownStrategy(t *testing.T) {
	t.Parallel()
	g := Group{Name: "home", Strategy: "magical"}
	_, err := Apply(g, nil)
	if err == nil {
		t.Fatal("Apply error = nil, want non-nil for unknown strategy")
	}
	if !strings.Contains(err.Error(), "magical") {
		t.Errorf("error %q does not mention the offending strategy name", err.Error())
	}
}

func TestApplyErrorWrapsForErrorsIs(t *testing.T) {
	t.Parallel()
	// Even though we don't expose a sentinel, the error must satisfy
	// the basic `errors` interface contract (non-nil error path).
	_, err := Apply(Group{Strategy: "bad"}, nil)
	if errors.Is(err, nil) {
		t.Errorf("errors.Is(err, nil) = true, want false")
	}
}
