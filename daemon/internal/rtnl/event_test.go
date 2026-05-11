package rtnl

import (
	"sort"
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
		{Carrier(99), "unknown"}, // out-of-range falls through to unknown
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

// sortByName returns events sorted by Name so tests don't depend on
// map iteration order. Diff's contract is order-unspecified.
func sortByName(events []LinkEvent) []LinkEvent {
	sort.Slice(events, func(i, j int) bool { return events[i].Name < events[j].Name })
	return events
}

func TestDiffEmitsForNewInterfaces(t *testing.T) {
	t.Parallel()
	prev := map[string]LinkState{}
	cur := map[string]LinkState{
		"eth0": {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp},
	}

	got := sortByName(Diff(prev, cur))
	want := []LinkEvent{{Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp}}

	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("Diff first-seen = %+v, want %+v", got, want)
	}
}

func TestDiffSuppressesUnchanged(t *testing.T) {
	t.Parallel()
	state := map[string]LinkState{
		"eth0":  {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp},
		"wwan0": {Name: "wwan0", Carrier: CarrierDown, Operstate: OperstateDown},
	}
	got := Diff(state, state)
	if len(got) != 0 {
		t.Errorf("Diff identical = %v, want empty", got)
	}
}

func TestDiffEmitsOnCarrierChange(t *testing.T) {
	t.Parallel()
	prev := map[string]LinkState{
		"eth0": {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp},
	}
	cur := map[string]LinkState{
		"eth0": {Name: "eth0", Carrier: CarrierDown, Operstate: OperstateUp},
	}
	got := Diff(prev, cur)
	if len(got) != 1 || got[0].Carrier != CarrierDown {
		t.Errorf("Diff carrier-down = %v, want one event with Carrier=down", got)
	}
}

func TestDiffEmitsOnOperstateChange(t *testing.T) {
	t.Parallel()
	prev := map[string]LinkState{
		"eth0": {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp},
	}
	cur := map[string]LinkState{
		"eth0": {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateDormant},
	}
	got := Diff(prev, cur)
	if len(got) != 1 || got[0].Operstate != OperstateDormant {
		t.Errorf("Diff operstate-dormant = %v, want one event with Operstate=dormant", got)
	}
}

func TestDiffOnlyEmitsChangedInterfaces(t *testing.T) {
	t.Parallel()
	// eth0 unchanged, wwan0 changes carrier — only wwan0 should
	// appear in the diff.
	prev := map[string]LinkState{
		"eth0":  {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp},
		"wwan0": {Name: "wwan0", Carrier: CarrierDown, Operstate: OperstateDown},
	}
	cur := map[string]LinkState{
		"eth0":  {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp},
		"wwan0": {Name: "wwan0", Carrier: CarrierUp, Operstate: OperstateUp},
	}
	got := Diff(prev, cur)
	if len(got) != 1 || got[0].Name != "wwan0" {
		t.Errorf("Diff partial = %v, want only wwan0", got)
	}
}

func TestDiffIgnoresVanishedInterfaces(t *testing.T) {
	t.Parallel()
	// eth0 was in prev but absent from cur — Diff must NOT emit an
	// event. RTM_DELLINK is handled separately by the subscriber.
	prev := map[string]LinkState{
		"eth0": {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp},
	}
	cur := map[string]LinkState{}

	got := Diff(prev, cur)
	if len(got) != 0 {
		t.Errorf("Diff vanished = %v, want empty (RTM_DELLINK is handled out-of-band)", got)
	}
}

func TestDiffEventTimeLeftZero(t *testing.T) {
	t.Parallel()
	// The pure Diff must not stamp Time; the subscriber owns
	// timestamping at emit so tests of the change-detection
	// contract can compare events by value cleanly.
	cur := map[string]LinkState{
		"eth0": {Name: "eth0", Carrier: CarrierUp, Operstate: OperstateUp},
	}
	got := Diff(nil, cur)
	if len(got) != 1 || !got[0].Time.IsZero() {
		t.Errorf("Diff Time = %v, want zero (subscriber stamps later)", got[0].Time)
	}
}
