// wanwatchd is the wanwatch daemon — one process per host. Reads
// the JSON config rendered by `lib/internal/config.nix`, drives one
// ICMP prober per (WAN, family), listens for rtnetlink link events,
// and emits Decisions that mutate kernel routing state plus the
// externalized state.json.
//
// File layout:
//   - main.go         — process lifecycle (flags, logging, signals)
//   - daemon.go       — event handlers, Apply adapter, commit publication
//     and the Hook-sink seam
//     (WAN Health lives in internal/health, Selection progression
//     in internal/decision)
//   - probers.go      — startProbers + identKeysFor
//   - subscribers.go  — startLinkSubscriber, startRouteSubscriber
//   - eventloop.go    — eventLoop (central event dispatch) and serve
//   - kernel.go       — the kernel seam and its netlink adapter
//   - decision.go     — pure helper groupContainsWAN and the
//     decisionReason enum
//   - gateway.go      — GatewayCache (kernel default-route mirror)
//   - helpers.go      — small free utilities (interfaceIndex, …)
//   - buildinfo.go    — version / revision / goVersion (ldflags)
//   - sdnotify.go     — sd_notify READY + watchdog keepalive
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// flags are the daemon's command-line options. Kept minimal —
// everything else lives in the JSON config rendered by the NixOS
// module, so the flag surface stays stable across versions.
type flags struct {
	configPath string
	logLevel   string
}

func parseFlags(args []string) (flags, error) {
	fs := flag.NewFlagSet("wanwatchd", flag.ContinueOnError)
	f := flags{}
	fs.StringVar(&f.configPath, "config", "/etc/wanwatch/config.json", "path to daemon config JSON")
	fs.StringVar(&f.logLevel, "log-level", "", "override global.logLevel from config (debug|info|warn|error)")
	if err := fs.Parse(args); err != nil {
		return flags{}, err
	}
	return f, nil
}

// parseLogLevel maps the slog level names used in config.global to
// slog.Level values. Returns an error rather than silently falling
// back so a typo in config doesn't quietly downgrade the daemon's
// visibility.
func parseLogLevel(name string) (slog.Level, error) {
	switch name {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug|info|warn|error)", name)
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "wanwatchd: %v\n", err)
		os.Exit(1)
	}
}

// run is the testable entry point. Takes the argv tail and a sink
// for logs (os.Stderr in production, a buffer in tests) so the
// daemon's startup path is exercisable without spawning a process.
func run(parent context.Context, args []string, logSink io.Writer) error {
	f, err := parseFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("flags: %w", err)
	}

	cfg, err := config.Load(f.configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	level, err := parseLogLevel(cmp.Or(f.logLevel, cfg.Global.LogLevel))
	if err != nil {
		return fmt.Errorf("log-level: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(logSink, &slog.HandlerOptions{Level: level}))
	logger.Info(
		"wanwatchd starting",
		"config", f.configPath,
		"wans", len(cfg.Wans),
		"groups", len(cfg.Groups),
	)

	// ctx carries a cancellation *cause*: errShutdown for a
	// signal-driven stop, or a subsystem's error if a background
	// goroutine dies. The cause drives run()'s exit code via
	// exitError. defer cancel(nil) only satisfies vet's lostcancel
	// check — the meaningful causes are set by the explicit cancel
	// calls below, and the first one wins.
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	sigCtx, sigStop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer sigStop()
	go func() {
		<-sigCtx.Done()
		cancel(errShutdown)
	}()

	mreg := metrics.New()
	mreg.SetBuildInfo(version, revision, goVersion)

	mserver := &metrics.Server{
		Socket:  cfg.Global.MetricsSocket,
		Handler: mreg.Handler(),
	}
	metricsDone := superviseSubsystem(ctx, cancel, logger, "metrics server", func() error {
		return mserver.Serve(ctx)
	})

	logger.Info("metrics endpoint listening", "socket", cfg.Global.MetricsSocket)

	d := newDaemon(&cfg, netlinkKernel{}, newHookNotifier(ctx, &cfg, mreg, logger), mreg, logger)
	if err := d.bootstrap(ctx); err != nil {
		cancel(fmt.Errorf("bootstrap: %w", err))
		return exitError(ctx, metricsDone, logger)
	}

	probeResults, err := startProbers(ctx, cancel, &cfg, logger)
	if err != nil {
		cancel(fmt.Errorf("probers: %w", err))
		return exitError(ctx, metricsDone, logger)
	}
	linkSnapshot, linkEvents, err := startLinkSubscriber(ctx, cancel, &cfg, logger)
	if err != nil {
		cancel(fmt.Errorf("link subscriber: %w", err))
		return exitError(ctx, metricsDone, logger)
	}
	routeSnapshot, routeEvents, err := startRouteSubscriber(ctx, cancel, &cfg, logger)
	if err != nil {
		cancel(fmt.Errorf("route subscriber: %w", err))
		return exitError(ctx, metricsDone, logger)
	}

	// Prime before READY, so units ordered after wanwatch find the
	// startup Decision's Apply attempted and State published. Hooks
	// run asynchronously and may still be running at READY.
	d.prime(ctx, startupSnapshot{links: linkSnapshot, routes: routeSnapshot})

	// Every subsystem is wired and primed — tell systemd the daemon
	// is ready and start the watchdog keepalive. Both no-op outside a
	// Type=notify unit, so a non-systemd launch is unaffected. A
	// failed READY send isn't fatal: systemd's start timeout would
	// restart us anyway, and crashing on a transient socket hiccup
	// while otherwise healthy would be worse.
	if err := sdNotify("READY=1"); err != nil {
		logger.Warn("sd_notify READY failed", "err", err)
	}
	watchdogChallenges := make(chan watchdogChallenge)
	go runWatchdog(ctx, logger, watchdogChallenges)

	serve(ctx, d, probeResults, linkEvents, routeEvents, watchdogChallenges)
	return exitError(ctx, metricsDone, logger)
}

// newHookNotifier wires Hook delivery to the configured Hook directory
// and per-Hook timeout, bound to the daemon's lifetime context.
func newHookNotifier(ctx context.Context, cfg *config.Config, mreg *metrics.Registry, logger *slog.Logger) *state.HookNotifier {
	return state.NewHookNotifier(ctx, cfg.Global.HooksDir,
		time.Duration(cfg.Global.HookTimeoutMs)*time.Millisecond, mreg, logger)
}

// errShutdown is the context cancellation cause recorded for an
// orderly, signal-driven stop. Any other non-nil cause means a
// background subsystem died and cancelled the daemon context to
// force a non-zero exit, so systemd's Restart=on-failure brings the
// whole process back — see nixos/module.nix.
var errShutdown = errors.New("shutdown signal received")

// isCleanShutdown reports whether `cause` (context.Cause of the
// daemon context, read after eventLoop) represents an orderly stop —
// a signal, or a cancelled parent — rather than a subsystem failure
// that must exit non-zero.
func isCleanShutdown(cause error) bool {
	return cause == nil ||
		errors.Is(cause, errShutdown) ||
		errors.Is(cause, context.Canceled)
}

// errSubsystemStopped is the cause recorded for a subsystem that
// returned while the daemon was still running without reporting an
// error of its own: a nil return, or a Canceled from a context other
// than the daemon's. It must not wrap context.Canceled, which
// isCleanShutdown reads as an orderly stop.
var errSubsystemStopped = errors.New("exited while the daemon was running")

// superviseSubsystem runs a background subsystem's wait on its own
// goroutine and returns a channel closed once its exit is handled.
// Any exit while ctx is live takes the daemon down: the cause, which
// names the subsystem, cancels ctx, so exitError reports it and the
// process exits non-zero for systemd to restart. A subsystem that
// stopped silently would otherwise leave failover running blind, as
// READY and the watchdog only prove the event loop is alive. Once ctx
// is done, an exit is the orderly-shutdown path and keeps the first
// cause.
func superviseSubsystem(ctx context.Context, cancel context.CancelCauseFunc, logger *slog.Logger, name string, wait func() error) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := wait()
		if ctx.Err() != nil {
			return
		}
		logger.Error("subsystem exited", "subsystem", name, "err", err)
		switch {
		case err == nil:
			cancel(fmt.Errorf("%s: %w", name, errSubsystemStopped))
		case errors.Is(err, context.Canceled):
			// The text only: a wrapped Canceled would read as an
			// orderly stop.
			cancel(fmt.Errorf("%s: %w: %s", name, errSubsystemStopped, err.Error()))
		default:
			cancel(fmt.Errorf("%s: %w", name, err))
		}
	}()
	return done
}

// exitError waits for the metrics server to finish, then maps the
// daemon context's cancellation cause to run()'s return value: nil
// for an orderly stop (so the process exits 0), the cause otherwise
// (so main exits non-zero and systemd restarts the daemon).
func exitError(ctx context.Context, metricsDone <-chan struct{}, logger *slog.Logger) error {
	<-metricsDone
	cause := context.Cause(ctx)
	if isCleanShutdown(cause) {
		logger.Info("shutdown complete", "cause", cause)
		return nil
	}
	logger.Error("daemon exiting on failure", "cause", cause)
	return fmt.Errorf("daemon stopped: %w", cause)
}
