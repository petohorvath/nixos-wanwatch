// wanwatchd is the wanwatch daemon — one process per host. Reads
// the JSON config rendered by `lib/internal/config.nix`, drives one
// ICMP prober per (WAN, family), listens for rtnetlink link events,
// and emits Decisions that mutate kernel routing state plus the
// externalized state.json.
//
// File layout:
//   - main.go     — process lifecycle (flags, logging, signals)
//   - daemon.go   — startProbers/startSubscriber + eventLoop
//   - decision.go — pure helpers (thresholds, family policy, sort)
//   - state.go    — daemon struct + Decision pipeline
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
func run(parent context.Context, args []string, logSink *os.File) error {
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

	levelName := cfg.Global.LogLevel
	if f.logLevel != "" {
		levelName = f.logLevel
	}
	level, err := parseLogLevel(levelName)
	if err != nil {
		return fmt.Errorf("log-level: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(logSink, &slog.HandlerOptions{Level: level}))
	logger.Info("wanwatchd starting",
		"config", f.configPath,
		"wans", len(cfg.Wans),
		"groups", len(cfg.Groups),
	)

	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	mreg := metrics.New()
	mreg.BuildInfo.WithLabelValues(version, goVersion, commit).Set(1)

	mserver := &metrics.Server{
		Socket:  cfg.Global.MetricsSocket,
		Handler: mreg.Handler(),
	}
	mErrCh := make(chan error, 1)
	go func() { mErrCh <- mserver.Serve(ctx) }()

	logger.Info("metrics endpoint listening", "socket", cfg.Global.MetricsSocket)

	d := newDaemon(&cfg, mreg, logger)
	if err := d.bootstrap(); err != nil {
		cancel()
		<-mErrCh
		return fmt.Errorf("bootstrap: %w", err)
	}

	probeResults, err := startProbers(ctx, &cfg, logger)
	if err != nil {
		cancel()
		<-mErrCh
		return fmt.Errorf("probers: %w", err)
	}
	linkEvents := startSubscriber(ctx, &cfg, logger)
	routeEvents := startRouteSubscriber(ctx, &cfg, logger)

	eventLoop(ctx, d, probeResults, linkEvents, routeEvents)
	logger.Info("shutdown signal received", "err", ctx.Err())

	if err := <-mErrCh; err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("metrics server: %w", err)
	}
	return nil
}
