package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeHook writes an executable shell script at <eventDir>/<name>.
// `script` is the bash body (without shebang); the test util adds
// the shebang and sets 0o755. Returns the full path.
func writeHook(t *testing.T, eventDir, name, script string) string {
	t.Helper()
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	full := filepath.Join(eventDir, name)
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(full, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile %s: %v", full, err)
	}
	return full
}

func TestRunNoEventDirReturnsNil(t *testing.T) {
	t.Parallel()
	r := Runner{Dir: t.TempDir()}
	results := r.Run(context.Background(), HookContext{Event: EventUp})
	if results != nil {
		t.Errorf("Run with no event dir = %v, want nil", results)
	}
}

func TestRunExecutesHookSuccessfully(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHook(t, filepath.Join(dir, "up.d"), "ok.sh", "exit 0")

	r := Runner{Dir: dir}
	results := r.Run(context.Background(), HookContext{Event: EventUp})
	if got := len(results); got != 1 {
		t.Fatalf("len(results) = %d, want 1", got)
	}
	if results[0].ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", results[0].ExitCode)
	}
	if results[0].Err != nil {
		t.Errorf("Err = %v, want nil", results[0].Err)
	}
}

func TestRunCapturesNonZeroExit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHook(t, filepath.Join(dir, "down.d"), "bad.sh", "exit 7")

	r := Runner{Dir: dir}
	results := r.Run(context.Background(), HookContext{Event: EventDown})
	if results[0].ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", results[0].ExitCode)
	}
	if results[0].Err == nil {
		t.Errorf("Err = nil, want non-nil for nonzero exit")
	}
}

func TestRunHonorsTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHook(t, filepath.Join(dir, "switch.d"), "slow.sh", "sleep 5")

	r := Runner{Dir: dir, Timeout: 200 * time.Millisecond}
	start := time.Now()
	results := r.Run(context.Background(), HookContext{Event: EventSwitch})
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("Run took %v — timeout did not fire", elapsed)
	}
	if !results[0].TimedOut {
		t.Errorf("TimedOut = false, want true")
	}
	if results[0].ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for timeout", results[0].ExitCode)
	}
}

func TestRunPassesEnvVars(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outFile := filepath.Join(dir, "captured.txt")
	writeHook(t, filepath.Join(dir, "up.d"), "env.sh",
		`echo "$WANWATCH_EVENT|$WANWATCH_GROUP|$WANWATCH_WAN_NEW|$WANWATCH_GATEWAY_V4_NEW|$WANWATCH_FAMILIES|$WANWATCH_TABLE" > `+outFile)

	r := Runner{Dir: dir}
	r.Run(context.Background(), HookContext{
		Event:        EventUp,
		Group:        "home",
		WanNew:       "primary",
		GatewayV4New: "192.0.2.1",
		Families:     []string{"v4", "v6"},
		Table:        100,
	})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "up|home|primary|192.0.2.1|v4,v6|100"
	if got != want {
		t.Errorf("env captured = %q, want %q", got, want)
	}
}

func TestRunIgnoresNonExecutableFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "up.d")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "data.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := Runner{Dir: dir}
	results := r.Run(context.Background(), HookContext{Event: EventUp})
	if len(results) != 0 {
		t.Errorf("Run with only non-executable files = %v, want empty", results)
	}
}

func TestRunIgnoresSubdirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "up.d")
	if err := os.MkdirAll(filepath.Join(eventDir, "subdir"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeHook(t, eventDir, "real.sh", "exit 0")

	r := Runner{Dir: dir}
	results := r.Run(context.Background(), HookContext{Event: EventUp})
	if got := len(results); got != 1 {
		t.Errorf("len(results) = %d, want 1 (subdirs should be skipped)", got)
	}
}

func TestRunSortsHooksLexicographically(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "up.d")
	out := filepath.Join(dir, "order.txt")

	// `c` registers first, `a` second, `b` third — but we expect
	// a, b, c order on output.
	writeHook(t, eventDir, "c-third.sh", "echo c >> "+out)
	writeHook(t, eventDir, "a-first.sh", "echo a >> "+out)
	writeHook(t, eventDir, "b-second.sh", "echo b >> "+out)

	r := Runner{Dir: dir}
	r.Run(context.Background(), HookContext{Event: EventUp})

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := strings.TrimSpace(string(data))
	if got != "a\nb\nc" {
		t.Errorf("order = %q, want %q", got, "a\\nb\\nc")
	}
}

func TestRunExecutesAllHooksEvenAfterFailure(t *testing.T) {
	t.Parallel()
	// One hook fails, the next must still execute — best-effort
	// dispatch.
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "up.d")
	out := filepath.Join(dir, "ran.txt")

	writeHook(t, eventDir, "a-fails.sh", "exit 5")
	writeHook(t, eventDir, "b-ok.sh", "echo ran >> "+out)

	r := Runner{Dir: dir}
	results := r.Run(context.Background(), HookContext{Event: EventUp})

	if got := len(results); got != 2 {
		t.Errorf("len(results) = %d, want 2", got)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("second hook did not run: %v", err)
	}
}

func TestRunTimestampFallback(t *testing.T) {
	t.Parallel()
	// HookContext.Timestamp zero → buildEnv should use time.Now().
	dir := t.TempDir()
	out := filepath.Join(dir, "ts.txt")
	writeHook(t, filepath.Join(dir, "up.d"), "ts.sh", `echo "$WANWATCH_TS" > `+out)

	r := Runner{Dir: dir}
	r.Run(context.Background(), HookContext{Event: EventUp})

	data, _ := os.ReadFile(out)
	got := strings.TrimSpace(string(data))
	if got == "" {
		t.Errorf("WANWATCH_TS empty when Timestamp zero — want auto-stamp")
	}
}

func TestEventConstants(t *testing.T) {
	t.Parallel()
	// PLAN §5.5 fixes these names. The systemd hook directory layout
	// depends on them too: /etc/wanwatch/hooks/up.d, down.d, switch.d.
	if EventUp != "up" || EventDown != "down" || EventSwitch != "switch" {
		t.Errorf("event names drifted from PLAN §5.5: up=%q, down=%q, switch=%q",
			EventUp, EventDown, EventSwitch)
	}
}

// TestRunCapturesNonExitError: when the kernel refuses to exec the
// script at all (missing interpreter), cmd.Run() returns a non-
// `*exec.ExitError`. The runOne handler must still produce a
// HookResult with ExitCode=-1 and a wrapped Err — not panic on
// the errors.As branch.
func TestRunCapturesNonExitError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "up.d")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Shebang points at a path that doesn't exist on any host;
	// `execve` returns ENOENT, which Go surfaces as a PathError
	// rather than an ExitError.
	body := "#!/this/interpreter/does/not/exist\n:\n"
	if err := os.WriteFile(filepath.Join(eventDir, "broken.sh"), []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := Runner{Dir: dir}
	results := r.Run(context.Background(), HookContext{Event: EventUp})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Err == nil {
		t.Error("Err = nil, want non-nil for missing-interpreter exec failure")
	}
	if results[0].ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for non-ExitError failure", results[0].ExitCode)
	}
	if results[0].TimedOut {
		t.Error("TimedOut = true, want false for non-timeout failure")
	}
}
