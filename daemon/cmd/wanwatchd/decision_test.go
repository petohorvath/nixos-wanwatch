package main

import (
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

func TestGroupContainsWAN(t *testing.T) {
	t.Parallel()
	g := selector.Group{
		Name: "home",
		Members: map[string]selector.Member{
			"primary": {Wan: "primary"},
			"backup":  {Wan: "backup"},
		},
	}
	cases := []struct {
		name string
		wan  string
		want bool
	}{
		{"first member", "primary", true},
		{"second member", "backup", true},
		{"non-member", "ghost", false},
		{"empty string", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := groupContainsWAN(g, tc.wan); got != tc.want {
				t.Errorf("groupContainsWAN(_, %q) = %v, want %v", tc.wan, got, tc.want)
			}
		})
	}
}

func TestGroupContainsWANEmptyGroup(t *testing.T) {
	t.Parallel()
	if groupContainsWAN(selector.Group{Name: "empty"}, "anything") {
		t.Error("groupContainsWAN on empty group returned true")
	}
}
