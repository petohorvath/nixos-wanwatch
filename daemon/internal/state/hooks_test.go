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

	// Surface elapsed, err, exit code, and output unconditionally so a
	// CI failure tells us *why* the script returned fast — not just
	// that TimedOut was false.
	t.Logf("elapsed=%v TimedOut=%v ExitCode=%d Err=%v Output=%q",
		elapsed, results[0].TimedOut, results[0].ExitCode, results[0].Err, results[0].Output)

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

// TestRunBoundsMultipleSlowHooks: N hooks each sleeping past the
// per-hook timeout must still complete within roughly N × Timeout
// — not N × sleep-duration. Pins the invariant that the per-hook
// deadline caps total wall time so a single misbehaving script
// can't pin the daemon's apply loop.
func TestRunBoundsMultipleSlowHooks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "switch.d")
	// Three hooks, each sleeping 5s. Per-hook timeout 100ms; total
	// should be ~300ms, not 15s.
	for _, name := range []string{"a-slow.sh", "b-slow.sh", "c-slow.sh"} {
		writeHook(t, eventDir, name, "sleep 5")
	}

	r := Runner{Dir: dir, Timeout: 100 * time.Millisecond}
	start := time.Now()
	results := r.Run(context.Background(), HookContext{Event: EventSwitch})
	elapsed := time.Since(start)

	if len(results) != 3 {
		t.Errorf("len(results) = %d, want 3", len(results))
	}
	for _, r := range results {
		if !r.TimedOut {
			t.Errorf("hook %q did not time out", r.Path)
		}
	}
	// 2 seconds is a generous upper bound — 3 × 100ms expected,
	// but CI runners can be slow.
	if elapsed > 2*time.Second {
		t.Errorf("Run elapsed = %v; per-hook timeout did not cap total", elapsed)
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

// TestRunTimeoutKillsBackgroundedDescendants: a hook that
// backgrounds work and then blocks past the timeout must have its
// whole process group killed — not just the direct child — so the
// descendant can't outlive the daemon's apply step.
func TestRunTimeoutKillsBackgroundedDescendants(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "orphan-ran.txt")
	// Background a subshell that would touch the sentinel after the
	// hook itself is long gone, then block so the hook times out.
	writeHook(t, filepath.Join(dir, "up.d"), "fork.sh",
		"(sleep 1; touch "+sentinel+") &\nsleep 5")

	r := Runner{Dir: dir, Timeout: 200 * time.Millisecond}
	results := r.Run(context.Background(), HookContext{Event: EventUp})
	if !results[0].TimedOut {
		t.Fatalf("setup: hook did not time out (TimedOut = false)")
	}

	// Well past the backgrounded subshell's 1s sleep: with the process
	// group killed on timeout, it never reaches the touch.
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("backgrounded descendant survived the hook timeout — process group not killed")
	}
}

// TestRunCapturesOutput: a hook's combined stdout+stderr is captured
// into HookResult.Output, so a failing hook is diagnosable beyond
// its exit code.
func TestRunCapturesOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHook(t, filepath.Join(dir, "down.d"), "noisy.sh",
		`echo "stdout line"; echo "stderr line" >&2; exit 3`)

	r := Runner{Dir: dir}
	results := r.Run(context.Background(), HookContext{Event: EventDown})

	// Surface ExitCode/Err/Output/Duration unconditionally so a CI
	// failure points at the actual cause (typical flake: cmd.Run
	// returns a non-ExitError fork/exec error and Output is empty).
	t.Logf("Duration=%v ExitCode=%d Err=%v Output=%q",
		results[0].Duration, results[0].ExitCode, results[0].Err, results[0].Output)

	if results[0].ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", results[0].ExitCode)
	}
	if !strings.Contains(results[0].Output, "stdout line") {
		t.Errorf("Output missing stdout: %q", results[0].Output)
	}
	if !strings.Contains(results[0].Output, "stderr line") {
		t.Errorf("Output missing stderr: %q", results[0].Output)
	}
}

// TestRunBoundsOutput: a hook that floods its output must not grow
// the capture without limit — Output stays bounded and is marked
// truncated.
func TestRunBoundsOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHook(t, filepath.Join(dir, "up.d"), "flood.sh",
		"head -c 200000 /dev/zero")

	r := Runner{Dir: dir}
	results := r.Run(context.Background(), HookContext{Event: EventUp})

	// Surface ExitCode/Err/Output-len/Duration on every run so a CI
	// failure points at the actual cause. The full Output is binary
	// zeros (NULs) so it's not useful in the log — its length is.
	out := results[0].Output
	t.Logf("Duration=%v ExitCode=%d Err=%v len(Output)=%d",
		results[0].Duration, results[0].ExitCode, results[0].Err, len(out))

	if len(out) > maxHookOutput+64 {
		t.Errorf("Output len = %d, want <= maxHookOutput (%d) + marker", len(out), maxHookOutput)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("flooded Output not marked truncated (len=%d)", len(out))
	}
}

// TestCappedBuffer: the bounded writer keeps the first `limit`
// bytes, reports full writes so cmd.Wait sees no short-write error,
// and marks output that ran past the cap.
func TestCappedBuffer(t *testing.T) {
	t.Parallel()
	// Within the cap: kept verbatim, no marker.
	under := &cappedBuffer{limit: 10}
	if _, err := under.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := under.String(); got != "hello" {
		t.Errorf("under cap: String() = %q, want %q", got, "hello")
	}

	// Past the cap: first `limit` bytes kept, marker appended, and
	// Write still reports the full length.
	over := &cappedBuffer{limit: 5}
	n, err := over.Write([]byte("abcdefghij"))
	if n != 10 || err != nil {
		t.Errorf("Write = (%d, %v), want (10, nil) — must report a full write", n, err)
	}
	got := over.String()
	if !strings.HasPrefix(got, "abcde") {
		t.Errorf("over cap: String() = %q, want prefix %q", got, "abcde")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("over cap: String() = %q, want a truncation marker", got)
	}
}

// TestRunCapsHookCount: with more executable hooks than MaxHooks,
// only the first MaxHooks (lexicographic) run; the rest come back
// marked Skipped so the daemon can log them rather than letting
// them silently starve.
func TestRunCapsHookCount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eventDir := filepath.Join(dir, "up.d")
	ran := filepath.Join(dir, "ran.txt")
	for _, n := range []string{"a.sh", "b.sh", "c.sh", "d.sh"} {
		writeHook(t, eventDir, n, "echo "+n+" >> "+ran)
	}

	r := Runner{Dir: dir, MaxHooks: 2}
	results := r.Run(context.Background(), HookContext{Event: EventUp})

	if len(results) != 4 {
		t.Fatalf("len(results) = %d, want 4 (2 run + 2 skipped)", len(results))
	}
	var ranCount, skipped int
	for _, res := range results {
		if res.Skipped {
			skipped++
		} else {
			ranCount++
		}
	}
	if ranCount != 2 || skipped != 2 {
		t.Errorf("ran=%d skipped=%d, want ran=2 skipped=2", ranCount, skipped)
	}
	// Only the first two (lexicographic) actually executed.
	data, _ := os.ReadFile(ran)
	if got := strings.TrimSpace(string(data)); got != "a.sh\nb.sh" {
		t.Errorf("executed hooks = %q, want %q", got, "a.sh\\nb.sh")
	}
}
