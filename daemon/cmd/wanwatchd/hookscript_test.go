package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestHookScriptRunsFromConfiguredDir is the one test that runs a real
// Hook script. It pins the wiring from the configured Hook directory to
// Hook delivery, and that a running script does not hold up the event
// loop. Every other test records Hooks synchronously.
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

// waitForFile polls until path exists and returns its contents. Only
// the real-script Hook test waits like this; the writer must create
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
