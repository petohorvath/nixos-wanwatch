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
	"time"
)

// Event is the type of Decision being signalled to hooks. Matches
// the PLAN §5.5 `WANWATCH_EVENT` enumeration.
type Event string

const (
	EventUp     Event = "up"
	EventDown   Event = "down"
	EventSwitch Event = "switch"
)

// HookContext carries every field needed to populate the env vars
// defined in PLAN §5.5. Empty-string fields are emitted as empty
// env vars (not unset).
type HookContext struct {
	Event     Event
	Group     string
	WanOld    string
	WanNew    string
	IfaceOld  string
	IfaceNew  string
	GatewayV4Old string
	GatewayV4New string
	GatewayV6Old string
	GatewayV6New string
	Families  []string // e.g. ["v4", "v6"]
	Table     int
	Mark      int
	Timestamp time.Time
}

// HookResult records the outcome of one hook invocation. Returned
// in a slice from Runner.Run; hooks that timed out have
// `TimedOut = true` and `ExitCode = -1`.
type HookResult struct {
	Path     string
	ExitCode int
	TimedOut bool
	Err      error
	Duration time.Duration
}

// Runner dispatches hooks for a given Event by scanning
// `<Dir>/<event>.d/` for executable files and running each with
// the env vars derived from HookContext.
//
// Default per-hook Timeout is 5s (PLAN §12 OQ #5; configurable in
// v0.2 if users complain). Zero Timeout means no timeout.
type Runner struct {
	Dir     string
	Timeout time.Duration
}

// DefaultHookTimeout is the per-hook deadline applied when
// Runner.Timeout is zero. Matches PLAN §12 OQ #5.
const DefaultHookTimeout = 5 * time.Second

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

// Run executes every hook under `<Dir>/<ctx.Event>.d/` with the
// env vars in PLAN §5.5. Returns one HookResult per file. A
// missing event directory returns nil — not an error; users with
// no hooks shouldn't see noise in the logs.
//
// Files are executed in lexicographic order (matching `run-parts`
// convention). Each invocation runs in its own context with the
// configured timeout.
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
	for _, p := range paths {
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
	err := cmd.Run()
	dur := time.Since(start)

	res := HookResult{Path: path, Duration: dur}
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
