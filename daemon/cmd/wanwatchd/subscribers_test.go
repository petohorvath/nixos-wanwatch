package main

import (
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
)

func TestWatchedInterfaces(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Wans: map[string]config.Wan{
			"primary": {Interface: "eth0"},
			"backup":  {Interface: "wwan0"},
		},
	}
	got := watchedInterfaces(cfg)
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (got %v)", len(got), got)
	}
	if _, ok := got["eth0"]; !ok {
		t.Error("eth0 missing from watched set")
	}
	if _, ok := got["wwan0"]; !ok {
		t.Error("wwan0 missing from watched set")
	}
}

func TestWatchedInterfacesCollapsesDuplicates(t *testing.T) {
	t.Parallel()
	// Two WANs on the same interface (not a useful config, but the
	// set-of-interfaces contract should collapse them to one entry).
	cfg := &config.Config{
		Wans: map[string]config.Wan{
			"alpha": {Interface: "eth0"},
			"beta":  {Interface: "eth0"},
		},
	}
	got := watchedInterfaces(cfg)
	if len(got) != 1 {
		t.Errorf("len = %d, want 1 (deduped to eth0); got %v", len(got), got)
	}
}

func TestWatchedInterfacesEmpty(t *testing.T) {
	t.Parallel()
	got := watchedInterfaces(&config.Config{})
	if len(got) != 0 {
		t.Errorf("empty cfg → %v, want empty map", got)
	}
}
