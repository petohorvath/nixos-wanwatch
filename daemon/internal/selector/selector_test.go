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

func TestSelectDispatchesToStrategy(t *testing.T) {
	t.Parallel()
	g := Group{Name: "home", Strategy: "primary-backup"}
	members := []MemberHealth{
		{Member: Member{Wan: "primary", Priority: 1}, Healthy: true},
	}
	sel, err := Select(g, members)
	if err != nil {
		t.Fatalf("Select error: %v", err)
	}
	if sel.Group != "home" {
		t.Errorf("sel.Group = %q, want %q", sel.Group, "home")
	}
	if !sel.Active.Has || sel.Active.Wan != "primary" {
		t.Errorf("sel.Active = %+v, want {Wan:primary Has:true}", sel.Active)
	}
}

func TestSelectRejectsUnknownStrategy(t *testing.T) {
	t.Parallel()
	g := Group{Name: "home", Strategy: "magical"}
	_, err := Select(g, nil)
	if err == nil {
		t.Fatal("Select error = nil, want non-nil for unknown strategy")
	}
	if !strings.Contains(err.Error(), "magical") {
		t.Errorf("error %q does not mention the offending strategy name", err.Error())
	}
}

func TestSelectUnknownStrategyMatchesSentinel(t *testing.T) {
	t.Parallel()
	_, err := Select(Group{Strategy: "bad"}, nil)
	if !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("errors.Is(err, ErrUnknownStrategy) = false, want true (err = %v)", err)
	}
}
