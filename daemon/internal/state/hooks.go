package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Event is the type of Decision being signalled to hooks. Matches
// the PLAN §5.5 `WANWATCH_EVENT` enumeration.
type Event string

// Hook events emitted on a Decision: a previously-absent Selection
// becomes present (up), a present one becomes absent (down), or
// the active member changes (switch).
const (
	EventUp     Event = "up"
	EventDown   Event = "down"
	EventSwitch Event = "switch"
)

// HookContext carries every field needed to populate the env vars
// defined in PLAN §5.5. Empty-string fields are emitted as empty
// env vars (not unset).
type HookContext struct {
	Event        Event
	Group        string
	WanOld       string
	WanNew       string
	IfaceOld     string
	IfaceNew     string
	GatewayV4Old string
	GatewayV4New string
	GatewayV6Old string
	GatewayV6New string
	Families     []string // e.g. ["v4", "v6"]
	Table        int
	Mark         int
	Timestamp    time.Time
}

// HookResult records the outcome of one hook: invoked (with its
// exit code, timeout flag, and captured output), or skipped because
// Runner.MaxHooks was reached. Returned in a slice from Runner.Run;
// hooks that timed out have `TimedOut = true` and `ExitCode = -1`.
type HookResult struct {
	Path     string
	ExitCode int
	TimedOut bool
	Skipped  bool
	Err      error
	Duration time.Duration
	Output   string // combined stdout+stderr, capped at maxHookOutput
}

// Runner dispatches hooks for a given Event by scanning
// `<Dir>/<event>.d/` for executable files and running each with
// the env vars derived from HookContext.
//
// Timeout is the per-hook deadline, wired from the config's
// `global.hookTimeoutMs` (PLAN §12 OQ #5). A zero Timeout falls back
// to DefaultHookTimeout — the value a Runner constructed directly,
// without a config, gets. MaxHooks caps how many hooks one event
// runs — the rest come back as HookResult{Skipped: true}; zero
// MaxHooks means unlimited.
type Runner struct {
	Dir      string
	Timeout  time.Duration
	MaxHooks int
}

// DefaultHookTimeout is the per-hook deadline applied when
// Runner.Timeout is zero. Matches PLAN §12 OQ #5.
const DefaultHookTimeout = 5 * time.Second

// maxHookOutput caps the combined stdout+stderr captured per hook.
// A hook that floods its output must not be able to grow the
// daemon's memory — everything past this is discarded and the
// captured output is marked truncated.
const maxHookOutput = 16 << 10

// Env-var names passed to every hook invocation. Fixed by PLAN §5.5
// — hook scripts depend on them. Exported so callers (and tests)
// can reference the contract by name rather than by string literal.
const (
	EnvEvent        = "WANWATCH_EVENT"
	EnvGroup        = "WANWATCH_GROUP"
	EnvWanOld       = "WANWATCH_WAN_OLD"
	EnvWanNew       = "WANWATCH_WAN_NEW"
	EnvIfaceOld     = "WANWATCH_IFACE_OLD"
	EnvIfaceNew     = "WANWATCH_IFACE_NEW"
	EnvGatewayV4Old = "WANWATCH_GATEWAY_V4_OLD"
	EnvGatewayV4New = "WANWATCH_GATEWAY_V4_NEW"
	EnvGatewayV6Old = "WANWATCH_GATEWAY_V6_OLD"
	EnvGatewayV6New = "WANWATCH_GATEWAY_V6_NEW"
	EnvFamilies     = "WANWATCH_FAMILIES"
	EnvTable        = "WANWATCH_TABLE"
	EnvMark         = "WANWATCH_MARK"
	EnvTimestamp    = "WANWATCH_TS"
)

// Run executes the hooks under `<Dir>/<ctx.Event>.d/` with the env
// vars in PLAN §5.5. Returns one HookResult per file. A missing
// event directory returns nil — not an error; users with no hooks
// shouldn't see noise in the logs.
//
// Files run in lexicographic order (matching `run-parts`), each in
// its own context with the configured timeout. At most MaxHooks of
// them run; the rest are returned with `Skipped = true` so the
// caller can surface the cap rather than starving them silently.
func (r *Runner) Run(parent context.Context, ctx HookContext) []HookResult {
	eventDir := filepath.Join(r.Dir, string(ctx.Event)+".d")
	entries, err := os.ReadDir(eventDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []HookResult{{Path: eventDir, Err: err}}
	}

	timeout := r.Timeout
	if timeout == 0 {
		timeout = DefaultHookTimeout
	}

	env := buildEnv(ctx)
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(eventDir, e.Name())
		info, err := os.Stat(full)
		if err != nil || info.Mode()&0o111 == 0 {
			continue
		}
		paths = append(paths, full)
	}
	sort.Strings(paths)

	results := make([]HookResult, 0, len(paths))
	for i, p := range paths {
		if r.MaxHooks > 0 && i >= r.MaxHooks {
			results = append(results, HookResult{Path: p, Skipped: true})
			continue
		}
		results = append(results, runOne(parent, p, env, timeout))
	}
	return results
}

func runOne(parent context.Context, path string, env []string, timeout time.Duration) HookResult {
	hookCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(hookCtx, path)
	cmd.Env = env
	// Capture combined stdout+stderr, bounded — see cappedBuffer.
	// Pointing both streams at one buffer interleaves them in write
	// order; os/exec serializes writes when Stdout == Stderr.
	out := &cappedBuffer{limit: maxHookOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	// WaitDelay bounds how long Wait blocks for the output pipe to
	// drain after the process exits or is cancelled — a backstop if a
	// descendant that escaped the killed process group still holds it.
	cmd.WaitDelay = 2 * time.Second
	// Run the hook in its own process group, and on timeout/cancel
	// SIGKILL the whole group rather than just the script process —
	// exec.CommandContext's default Cancel kills only the direct
	// child, orphaning anything the hook backgrounded.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return cancelHook(cmd.Process.Pid, syscall.Kill, cmd.Process.Kill)
	}
	err := cmd.Run()
	dur := time.Since(start)

	res := HookResult{Path: path, Duration: dur, Output: out.String()}
	if hookCtx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		res.Err = hookCtx.Err()
		return res
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
		res.Err = err
		return res
	}
	return res
}

// cancelHook is the body of cmd.Cancel for hook subprocesses. SIGKILL
// the whole process group first so backgrounded descendants die with
// the script (see TestRunTimeoutKillsBackgroundedDescendants), and on
// ESRCH fall back to a direct os.Process.Kill to disambiguate two
// otherwise-indistinguishable cases:
//
//  1. The child has truly exited and its process group is gone.
//  2. The child is alive but hasn't reached its own `setpgid(0,0)`
//     yet — the fork→setpgid race documented in
//     syscall/exec_linux.go:391. In this window the child's pgid
//     equals the *parent's* pgid, so kill(-childPid, …) targets a
//     nonexistent group and returns ESRCH even though the child is
//     running.
//
// The direct Process.Kill closes the race in (2). If it returns
// os.ErrProcessDone instead, we know we were in case (1) and
// propagate that to exec.Cmd so it skips its WaitDelay-driven
// fallback kill.
//
// pgrpKill and procKill are seams for the test — production wires
// them to syscall.Kill and cmd.Process.Kill.
func cancelHook(pid int, pgrpKill func(int, syscall.Signal) error, procKill func() error) error {
	err := pgrpKill(-pid, syscall.SIGKILL)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.ESRCH) {
		return err
	}
	if killErr := procKill(); killErr != nil {
		if errors.Is(killErr, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return killErr
	}
	return nil
}

// cappedBuffer is an io.Writer that keeps the first `limit` bytes
// written and discards the rest, so a hook that floods its output
// cannot grow the daemon's memory. Write always reports a full
// write so cmd.Wait does not see a short-write error. Not safe for
// concurrent use — runOne points cmd.Stdout and cmd.Stderr at one
// buffer, which os/exec serializes.
type cappedBuffer struct {
	limit   int
	buf     []byte
	written int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.written += len(p)
	if room := b.limit - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

// String returns the captured output, with a marker appended when
// the hook wrote past the cap.
func (b *cappedBuffer) String() string {
	if b.written > b.limit {
		return string(b.buf) + "\n[hook output truncated]"
	}
	return string(b.buf)
}

// buildEnv constructs the env-var slice for a hook invocation.
// Pulls from the parent process env (so PATH etc. are inherited)
// and appends the PLAN §5.5 `WANWATCH_*` variables.
func buildEnv(ctx HookContext) []string {
	base := os.Environ()
	families := strings.Join(ctx.Families, ",")
	ts := ctx.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	extras := []string{
		EnvEvent + "=" + string(ctx.Event),
		EnvGroup + "=" + ctx.Group,
		EnvWanOld + "=" + ctx.WanOld,
		EnvWanNew + "=" + ctx.WanNew,
		EnvIfaceOld + "=" + ctx.IfaceOld,
		EnvIfaceNew + "=" + ctx.IfaceNew,
		EnvGatewayV4Old + "=" + ctx.GatewayV4Old,
		EnvGatewayV4New + "=" + ctx.GatewayV4New,
		EnvGatewayV6Old + "=" + ctx.GatewayV6Old,
		EnvGatewayV6New + "=" + ctx.GatewayV6New,
		EnvFamilies + "=" + families,
		EnvTable + "=" + strconv.Itoa(ctx.Table),
		EnvMark + "=" + strconv.Itoa(ctx.Mark),
		EnvTimestamp + "=" + ts.Format(time.RFC3339Nano),
	}
	return append(base, extras...)
}
