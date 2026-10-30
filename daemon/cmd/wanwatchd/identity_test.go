package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// rawState reads state.json's top-level object as a State reader sees
// it, without the daemon's own decoding.
func rawState(tb testing.TB, h *harness) map[string]any {
	tb.Helper()
	data, err := os.ReadFile(h.statePath)
	if err != nil {
		tb.Fatalf("read State: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		tb.Fatalf("decode State: %v", err)
	}
	return s
}

// startedAt returns State's startedAt as written, failing the test
// unless it is an RFC 3339 nanosecond UTC timestamp.
func startedAt(tb testing.TB, h *harness, step string) string {
	tb.Helper()
	v := rawState(tb, h)["startedAt"]
	raw, ok := v.(string)
	if !ok {
		tb.Fatalf("%s: startedAt = %#v, want a timestamp", step, v)
	}
	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil || !strings.HasSuffix(raw, "Z") {
		tb.Fatalf("%s: startedAt = %q, want RFC 3339 nanoseconds in UTC", step, raw)
	}
	return raw
}

// TestStateRecordsStartedAt pins State's startedAt: set when the
// daemon starts, so the first State already carries it, and unchanged
// by every later State write.
func TestStateRecordsStartedAt(t *testing.T) {
	t.Parallel()
	before := time.Now()
	h := startHarness(t, baseConfig())
	after := time.Now()

	first := startedAt(t, h, "startup")
	got, _ := time.Parse(time.RFC3339Nano, first)
	if got.Before(before) || got.After(after) {
		t.Errorf("startup: startedAt = %v, want between %v and %v", got, before, after)
	}

	cachedGateways(h)

	h.Link(carrier("eth0", true))
	if n := len(h.hooks()); n != 1 {
		t.Fatalf("%d Hooks, want the up", n)
	}
	if s := startedAt(t, h, "after the up"); s != first {
		t.Errorf("after the up: startedAt = %q, want %q from startup", s, first)
	}

	h.Route(gatewayGone("eth0", probe.FamilyV4))
	if s := startedAt(t, h, "after a Gateway change"); s != first {
		t.Errorf("after a Gateway change: startedAt = %q, want %q from startup", s, first)
	}
}

// TestStateRecordsVersion pins State's version to the daemon's linked
// version string.
func TestStateRecordsVersion(t *testing.T) {
	t.Parallel()
	h := startHarness(t, baseConfig())

	if got := rawState(t, h)["version"]; got != version {
		t.Errorf("version = %#v, want the linked version %q", got, version)
	}
}
