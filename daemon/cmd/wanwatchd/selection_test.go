package main

import (
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

// rawGroup reads group's object from state.json as a State reader
// sees it, without the daemon's own decoding.
func rawGroup(tb testing.TB, h *harness, group string) map[string]any {
	tb.Helper()
	groups, _ := rawState(tb, h)["groups"].(map[string]any)
	g, ok := groups[group].(map[string]any)
	if !ok {
		tb.Fatalf("State has no Group %q", group)
	}
	return g
}

// assertSelection checks that group's State names the Selection wan
// ("" for none) and its start since, under the State keys selection
// and selectionSince.
func assertSelection(tb testing.TB, h *harness, step, wan string, since time.Time) {
	tb.Helper()
	g := rawGroup(tb, h, "home")
	for _, old := range []string{"active", "activeSince"} {
		if _, ok := g[old]; ok {
			tb.Errorf("%s: State Group has the retired key %q", step, old)
		}
	}
	var want any
	if wan != "" {
		want = wan
	}
	if got, ok := g["selection"]; !ok || got != want {
		tb.Errorf("%s: selection = %#v (present %v), want %#v", step, got, ok, want)
	}
	raw, ok := g["selectionSince"].(string)
	if !ok {
		tb.Errorf("%s: selectionSince = %#v, want a timestamp", step, g["selectionSince"])
		return
	}
	got, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || !got.Equal(since) {
		tb.Errorf("%s: selectionSince = %q, want %v", step, raw, since)
	}
}

// TestStateNamesTheSelection pins the State keys of a Group's
// Selection: selectionSince moves with every Selection change, a down
// included, and stays put when the current Selection is reapplied.
func TestStateNamesTheSelection(t *testing.T) {
	t.Parallel()
	h := startHarness(t, baseConfig())
	cachedGateways(h)

	h.Link(carrier("eth0", true))
	up := h.hooks()[0].Timestamp
	assertSelection(t, h, "up", "primary", up)

	// backup's routes fail, so the switch to it stays pending; primary
	// recovers first and is reapplied.
	h.Link(carrier("wwan0", true))
	h.kernel.fail(opRoute, "wwan0")
	h.Probe(lossy("primary", probe.FamilyV4))
	h.Probe(healthy("primary", probe.FamilyV4))
	if n := len(h.hooks()); n != 1 {
		t.Fatalf("%d Hooks after the reapply, want only the up", n)
	}
	assertSelection(t, h, "reapply", "primary", up)

	h.kernel.heal(opRoute, "wwan0")
	h.Link(carrier("eth0", false))
	hooks := h.hooks()
	if n := len(hooks); n != 2 {
		t.Fatalf("%d Hooks after the switch, want 2", n)
	}
	assertSelection(t, h, "switch", "backup", hooks[1].Timestamp)

	h.Link(carrier("wwan0", false))
	hooks = h.hooks()
	if n := len(hooks); n != 3 {
		t.Fatalf("%d Hooks after the down, want 3", n)
	}
	assertSelection(t, h, "down", "", hooks[2].Timestamp)
}

// TestGroupSelectedGauge pins wanwatch_group_selected: every Member
// of every Group is exposed at 0 from startup, so a Group that never
// had a Selection is visible, and a Decision sets its Selection to 1.
func TestGroupSelectedGauge(t *testing.T) {
	t.Parallel()
	cfg := baseConfig()
	cfg.Groups["guest"] = selector.Group{
		Name: "guest", Strategy: "primary-backup", Table: 200, Mark: 0x200,
		Members: map[string]selector.Member{"backup": {Wan: "backup", Priority: 1}},
	}
	h := startHarness(t, cfg)
	cachedGateways(h)

	assertSeries := func(step string, want map[string]string) {
		t.Helper()
		got := scrapeSeries(t, h, "wanwatch_group_selected")
		if len(got) != len(want) {
			t.Errorf("%s: wanwatch_group_selected = %v, want %v", step, got, want)
			return
		}
		for labels, v := range want {
			if got[labels] != v {
				t.Errorf("%s: wanwatch_group_selected{%s} = %q, want %q", step, labels, got[labels], v)
			}
		}
	}

	assertSeries("before any Decision", map[string]string{
		`group="home",wan="primary"`: "0",
		`group="home",wan="backup"`:  "0",
		`group="guest",wan="backup"`: "0",
	})

	h.Link(carrier("eth0", true))
	if n := len(h.hooks()); n != 1 {
		t.Fatalf("%d Hooks, want the up of home", n)
	}
	assertSeries("after home's up", map[string]string{
		`group="home",wan="primary"`: "1",
		`group="home",wan="backup"`:  "0",
		`group="guest",wan="backup"`: "0",
	})
}
