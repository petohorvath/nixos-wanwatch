package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// TestHookScriptRunsFromConfiguredDir pins the wiring from the
// configured Hook directory to Hook delivery, and that a running script
// does not hold up the event loop. Only the TestHookScript tests run
// real Hook scripts; every other test records Hooks synchronously.
func TestHookScriptRunsFromConfiguredDir(t *testing.T) {
	t.Parallel()
	cfg := baseConfig()
	// The script blocks until Stop kills it; no timeout may end it first.
	cfg.Global.HookTimeoutMs = 60_000
	h := newScriptHarness(t, cfg)

	tmp := t.TempDir()
	capture := filepath.Join(tmp, "capture")
	blocker := filepath.Join(tmp, "blocker")
	// Nothing ever writes the FIFO, so reading it blocks the script.
	if err := syscall.Mkfifo(blocker, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	upDir := filepath.Join(cfg.Global.HooksDir, "up.d")
	if err := os.MkdirAll(upDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// The capture is renamed into place, so it is complete once it exists.
	script := "#!/bin/sh\n" +
		`printf '%s|%s|%s\n' "$WANWATCH_EVENT" "$WANWATCH_GROUP" "$WANWATCH_WAN_NEW" > '` + capture + `.tmp'` + "\n" +
		`mv '` + capture + `.tmp' '` + capture + `'` + "\n" +
		`read -r _ < '` + blocker + `'` + "\n"
	if err := os.WriteFile(filepath.Join(upDir, "capture.sh"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := h.Start(); err != nil {
		t.Fatalf("harness start: %v", err)
	}

	h.Link(carrier("eth0", true))

	if got, want := string(waitForFile(t, capture)), "up|home|primary\n"; got != want {
		t.Errorf("Hook script saw %q, want %q", got, want)
	}
	// The script is still blocked: the event loop handles this event
	// and acknowledges the harness barrier regardless.
	h.Link(carrier("wwan0", true))
	if got := selectionOf(h.State(), "home"); got != "primary" {
		t.Errorf("State Selection = %q, want primary", got)
	}
}

// TestHookScriptSeesDecisionVariables pins the WANWATCH_* names and
// values a real Hook script sees, including both sides' Families on a
// switch between WANs that serve different Families.
func TestHookScriptSeesDecisionVariables(t *testing.T) {
	t.Parallel()
	cfg := baseConfig()
	backup := cfg.Wans["backup"]
	backup.Probe.Targets.V6 = nil
	cfg.Wans["backup"] = backup
	h := newScriptHarness(t, cfg)
	capture := captureHookEnv(t, cfg.Global.HooksDir, "switch")
	if err := h.Start(); err != nil {
		t.Fatalf("harness start: %v", err)
	}

	h.Route(gateway("eth0", probe.FamilyV4, "192.0.2.1"))
	h.Route(gateway("eth0", probe.FamilyV6, "2001:db8::1"))
	h.Route(gateway("wwan0", probe.FamilyV4, "198.51.100.1"))
	h.Link(carrier("wwan0", true))
	h.Link(carrier("eth0", true))

	env := parseEnv(waitForFile(t, capture))
	want := map[string]string{
		"WANWATCH_EVENT":          "switch",
		"WANWATCH_GROUP":          "home",
		"WANWATCH_WAN_OLD":        "backup",
		"WANWATCH_WAN_NEW":        "primary",
		"WANWATCH_INTERFACE_OLD":  "wwan0",
		"WANWATCH_INTERFACE_NEW":  "eth0",
		"WANWATCH_GATEWAY_V4_OLD": "198.51.100.1",
		"WANWATCH_GATEWAY_V4_NEW": "192.0.2.1",
		"WANWATCH_GATEWAY_V6_OLD": "",
		"WANWATCH_GATEWAY_V6_NEW": "2001:db8::1",
		"WANWATCH_FAMILIES_OLD":   "v4",
		"WANWATCH_FAMILIES_NEW":   "v4,v6",
		"WANWATCH_TABLE":          "100",
		"WANWATCH_MARK":           "256",
	}
	for name, value := range want {
		if got, ok := env[name]; !ok || got != value {
			t.Errorf("%s = %q (set: %v), want %q", name, got, ok, value)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, env["WANWATCH_TIMESTAMP"]); err != nil {
		t.Errorf("WANWATCH_TIMESTAMP = %q: %v", env["WANWATCH_TIMESTAMP"], err)
	}
	for _, name := range []string{"WANWATCH_IFACE_OLD", "WANWATCH_IFACE_NEW", "WANWATCH_FAMILIES", "WANWATCH_TS"} {
		if value, ok := env[name]; ok {
			t.Errorf("pre-release variable %s = %q is still set", name, value)
		}
	}
}

// TestHookScriptInheritsOnlyAllowlistedEnvironment pins which of the
// daemon's environment variables reach a Hook. It sets the daemon's
// environment, so it cannot run in parallel.
func TestHookScriptInheritsOnlyAllowlistedEnvironment(t *testing.T) {
	inherited := map[string]string{
		"PATH":           os.Getenv("PATH"),
		"LANG":           "C.UTF-8",
		"LC_ALL":         "C.UTF-8",
		"LC_TIME":        "en_DK.UTF-8",
		"TZ":             "Europe/Budapest",
		"HOME":           "/var/empty",
		"USER":           "wanwatch",
		"LOGNAME":        "wanwatch",
		"JOURNAL_STREAM": "8:12345",
		"INVOCATION_ID":  "0123456789abcdef0123456789abcdef",
	}
	withheld := map[string]string{
		"NOTIFY_SOCKET":          "/run/systemd/notify",
		"WATCHDOG_USEC":          "30000000",
		"WATCHDOG_PID":           "1",
		"LISTEN_FDS":             "1",
		"LISTEN_PID":             "1",
		"LISTEN_FDNAMES":         "metrics",
		"CREDENTIALS_DIRECTORY":  "/run/credentials/wanwatch.service",
		"WANWATCH_TEST_UNLISTED": "secret",
		"LANGUAGE":               "hu",
	}
	for name, value := range inherited {
		t.Setenv(name, value)
	}
	for name, value := range withheld {
		t.Setenv(name, value)
	}
	cfg := baseConfig()
	h := newScriptHarness(t, cfg)
	capture := captureHookEnv(t, cfg.Global.HooksDir, "up")
	if err := h.Start(); err != nil {
		t.Fatalf("harness start: %v", err)
	}

	h.Link(carrier("eth0", true))

	env := parseEnv(waitForFile(t, capture))
	for name, value := range inherited {
		if got, ok := env[name]; !ok || got != value {
			t.Errorf("%s = %q (set: %v), want %q inherited", name, got, ok, value)
		}
	}
	for name := range withheld {
		if value, ok := env[name]; ok {
			t.Errorf("%s = %q reached the Hook, want it withheld", name, value)
		}
	}
	if got := env["WANWATCH_EVENT"]; got != "up" {
		t.Errorf("WANWATCH_EVENT = %q, want up", got)
	}
}

// captureHookEnv installs a Hook for event under hooksDir that dumps
// its environment, and returns the path the dump appears at, complete,
// once the Hook has run.
func captureHookEnv(tb testing.TB, hooksDir, event string) string {
	tb.Helper()
	capture := filepath.Join(tb.TempDir(), "env")
	eventDir := filepath.Join(hooksDir, event+".d")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		tb.Fatalf("MkdirAll: %v", err)
	}
	script := "#!/bin/sh\n" +
		`env > '` + capture + `.tmp'` + "\n" +
		`mv '` + capture + `.tmp' '` + capture + `'` + "\n"
	if err := os.WriteFile(filepath.Join(eventDir, "env.sh"), []byte(script), 0o755); err != nil {
		tb.Fatalf("WriteFile: %v", err)
	}
	return capture
}

// parseEnv parses env(1) output. Values must not contain newlines.
func parseEnv(data []byte) map[string]string {
	env := make(map[string]string)
	for line := range strings.Lines(string(data)) {
		name, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		env[name] = value
	}
	return env
}

// waitForFile polls until path exists and returns its contents. Only
// the real-script Hook tests wait like this; the writer must create
// path atomically.
func waitForFile(tb testing.TB, path string) []byte {
	tb.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
		if !errors.Is(err, fs.ErrNotExist) {
			tb.Fatalf("read %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			tb.Fatalf("%s was not written within 10s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
