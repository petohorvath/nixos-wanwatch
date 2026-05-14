// wanwatchd is the wanwatch daemon — one process per host. Reads
// the JSON config rendered by `lib/internal/config.nix`, drives one
// ICMP prober per (WAN, family), listens for rtnetlink link events,
// and emits Decisions that mutate kernel routing state plus the
// externalized state.json.
//
// File layout:
//   - main.go         — process lifecycle (flags, logging, signals)
//   - daemon.go       — daemon struct + Decision pipeline (handlers,
//     recompute, applyRoutes, writeStateSnapshot, runHooks)
//   - probers.go      — startProbers + probe-target helpers
//     (identKeysFor, targetsFor, familiesFromTargets)
//   - subscribers.go  — startLinkSubscriber, startRouteSubscriber
//   - eventloop.go    — eventLoop (central event dispatch)
//   - decision.go     — pure helpers (thresholds, family policy,
//     sort, hookEventFor) and the decisionReason enum
//   - gateway.go      — GatewayCache (kernel default-route mirror)
//   - helpers.go      — small free utilities (boolToFloat, …)
//   - buildinfo.go    — version / commit / goVersion (ldflags)
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

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
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
	logger.Info("wanwatchd starting",
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
	mreg.BuildInfo.WithLabelValues(version, goVersion, commit).Set(1)

	mserver := &metrics.Server{
		Socket:  cfg.Global.MetricsSocket,
		Handler: mreg.Handler(),
	}
	metricsDone := make(chan struct{})
	go func() {
		defer close(metricsDone)
		err := mserver.Serve(ctx)
		onSubsystemExit(cancel, logger, "metrics server", err)
	}()

	logger.Info("metrics endpoint listening", "socket", cfg.Global.MetricsSocket)

	d := newDaemon(&cfg, mreg, logger)
	if err := d.bootstrap(ctx); err != nil {
		cancel(fmt.Errorf("bootstrap: %w", err))
		return exitError(ctx, metricsDone, logger)
	}

	probeResults, err := startProbers(ctx, cancel, &cfg, logger)
	if err != nil {
		cancel(fmt.Errorf("probers: %w", err))
		return exitError(ctx, metricsDone, logger)
	}
	linkEvents, err := startLinkSubscriber(ctx, cancel, &cfg, logger)
	if err != nil {
		cancel(fmt.Errorf("link subscriber: %w", err))
		return exitError(ctx, metricsDone, logger)
	}
	routeEvents, err := startRouteSubscriber(ctx, cancel, &cfg, logger)
	if err != nil {
		cancel(fmt.Errorf("route subscriber: %w", err))
		return exitError(ctx, metricsDone, logger)
	}

	eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
	return exitError(ctx, metricsDone, logger)
}

// errShutdown is the context cancellation cause recorded for an
// orderly, signal-driven stop. Any other non-nil cause means a
// background subsystem died and cancelled the daemon context to
// force a non-zero exit, so systemd's Restart=on-failure brings the
// whole process back — see modules/wanwatch.nix.
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

// onSubsystemExit handles a background goroutine returning. A
// context-cancellation error is the orderly-shutdown path and is
// ignored; any other error is logged and recorded as the daemon
// context's cancellation cause, so exitError reports it and the
// process exits non-zero for systemd to restart.
func onSubsystemExit(cancel context.CancelCauseFunc, logger *slog.Logger, name string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	logger.Error("subsystem exited", "subsystem", name, "err", err)
	cancel(fmt.Errorf("%s: %w", name, err))
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
