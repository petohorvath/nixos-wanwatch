package rtnl

import (
	"testing"
)

func TestCarrierString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		c    Carrier
		want string
	}{
		{CarrierUp, "up"},
		{CarrierDown, "down"},
		{CarrierUnknown, "unknown"},
		// Carrier is internal — no external producer can mint new
		// values, so out-of-range collapses to "unknown" (unlike
		// Operstate which mirrors a kernel ABI that may grow).
		{Carrier(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("Carrier(%d).String() = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestOperstateString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		o    Operstate
		want string
	}{
		{OperstateUnknown, "unknown"},
		{OperstateNotPresent, "notpresent"},
		{OperstateDown, "down"},
		{OperstateLowerLayerDown, "lowerlayerdown"},
		{OperstateTesting, "testing"},
		{OperstateDormant, "dormant"},
		{OperstateUp, "up"},
	}
	for _, tc := range cases {
		if got := tc.o.String(); got != tc.want {
			t.Errorf("Operstate(%d).String() = %q, want %q", tc.o, got, tc.want)
		}
	}
}

func TestOperstateStringOutOfRange(t *testing.T) {
	t.Parallel()
	// A kernel ABI bump that adds OperstateFoo = 7 must surface in
	// logs as `operstate(7)` rather than collapsing to "unknown"
	// alongside the real OperstateUnknown=0.
	got := Operstate(7).String()
	want := "operstate(7)"
	if got != want {
		t.Errorf("Operstate(7).String() = %q, want %q", got, want)
	}
}
