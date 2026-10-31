package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestParseFlagsDefaults(t *testing.T) {
	t.Parallel()
	f, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if f.configPath != "/etc/wanwatch/config.json" {
		t.Errorf("configPath = %q, want default", f.configPath)
	}
	if f.logLevel != "" {
		t.Errorf("logLevel = %q, want empty (use config global.logLevel)", f.logLevel)
	}
}

func TestParseFlagsOverrides(t *testing.T) {
	t.Parallel()
	f, err := parseFlags([]string{"-config", "/tmp/wan.json", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if f.configPath != "/tmp/wan.json" {
		t.Errorf("configPath = %q", f.configPath)
	}
	if f.logLevel != "debug" {
		t.Errorf("logLevel = %q", f.logLevel)
	}
}

func TestParseFlagsHelp(t *testing.T) {
	t.Parallel()
	// -help triggers flag.ErrHelp which run() treats as a clean
	// exit. parseFlags returns the wrapped error untouched.
	_, err := parseFlags([]string{"-help"})
	if err == nil {
		t.Fatal("parseFlags(-help) = nil, want flag.ErrHelp")
	}
	if !strings.Contains(err.Error(), flag.ErrHelp.Error()) {
		t.Errorf("err = %v, want wrapping flag.ErrHelp", err)
	}
}

func TestParseLogLevelKnown(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"":      slog.LevelInfo, // empty → info, so config.global default lands here
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for name, want := range cases {
		got, err := parseLogLevel(name)
		if err != nil {
			t.Errorf("parseLogLevel(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseLogLevelRejectsTypo(t *testing.T) {
	t.Parallel()
	// A typo in config.global.logLevel must fail loudly — silent
	// fallback would hide the daemon's actual visibility.
	if _, err := parseLogLevel("verbose"); err == nil {
		t.Error("parseLogLevel(verbose) = nil, want error")
	}
}

func TestIsCleanShutdown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cause error
		want  bool
	}{
		{"nil", nil, true},
		{"errShutdown", errShutdown, true},
		{"wrapped errShutdown", fmt.Errorf("daemon ctx: %w", errShutdown), true},
		{"context.Canceled", context.Canceled, true},
		{"subsystem error", errors.New("prober primary/v4: socket died"), false},
		{"wrapped subsystem error", fmt.Errorf("link subscriber: %w", errors.New("ENOBUFS")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isCleanShutdown(tc.cause); got != tc.want {
				t.Errorf("isCleanShutdown(%v) = %v, want %v", tc.cause, got, tc.want)
			}
		})
	}
}

func TestExitError(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name    string
		cause   error
		wantErr bool
	}{
		{"signal → clean exit", errShutdown, false},
		{"cancelled parent → clean exit", context.Canceled, false},
		{"subsystem failure → non-nil error", errors.New("link subscriber: ENOBUFS"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(tc.cause)
			metricsDone := make(chan struct{})
			close(metricsDone)

			err := exitError(ctx, metricsDone, logger)
			if (err != nil) != tc.wantErr {
				t.Fatalf("exitError = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, tc.cause) {
				t.Errorf("exitError = %v, want %%w-wrapped %v", err, tc.cause)
			}
		})
	}
}

// TestSuperviseSubsystem pins how a subsystem's exit maps to the
// daemon's exit. While the daemon runs, any exit takes it down, so
// systemd restarts it instead of leaving failover running without the
// subsystem: an error, a nil return, or a Canceled from a context
// other than the daemon's. Once the daemon is stopping, exits are
// the orderly path and keep the shutdown cause.
func TestSuperviseSubsystem(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errSocket := errors.New("socket died")
	cases := []struct {
		name     string
		stopping bool
		exit     error
		// wantCause is a sentinel the cause must wrap.
		wantCause error
		wantErr   bool
	}{
		{"error while running", false, errSocket, errSocket, true},
		{"nil while running", false, nil, errSubsystemStopped, true},
		{"Canceled while running", false, context.Canceled, errSubsystemStopped, true},
		{"wrapped Canceled while running", false, fmt.Errorf("read: %w", context.Canceled), errSubsystemStopped, true},
		{"Canceled while stopping", true, context.Canceled, errShutdown, false},
		{"wrapped Canceled while stopping", true, fmt.Errorf("read: %w", context.Canceled), errShutdown, false},
		{"nil while stopping", true, nil, errShutdown, false},
		{"error while stopping", true, errSocket, errShutdown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancelCause(t.Context())
			if tc.stopping {
				cancel(errShutdown)
			}

			<-superviseSubsystem(ctx, cancel, logger, "prober primary/v4", func() error { return tc.exit })

			cause := context.Cause(ctx)
			if !errors.Is(cause, tc.wantCause) {
				t.Errorf("cause = %v, want %%w-wrapped %v", cause, tc.wantCause)
			}
			if tc.wantErr && !strings.Contains(cause.Error(), "prober primary/v4") {
				t.Errorf("cause = %q, want it to name the subsystem", cause)
			}
			closed := make(chan struct{})
			close(closed)
			if err := exitError(ctx, closed, logger); (err != nil) != tc.wantErr {
				t.Errorf("exitError = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestSuperviseSubsystemFirstCauseWins: when two subsystems fail in
// turn, the daemon reports the first, which is what took it down.
func TestSuperviseSubsystemFirstCauseWins(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancelCause(t.Context())
	errFirst := errors.New("ENOBUFS")
	errSecond := errors.New("socket died")

	<-superviseSubsystem(ctx, cancel, logger, "link subscriber", func() error { return errFirst })
	<-superviseSubsystem(ctx, cancel, logger, "prober primary/v4", func() error { return errSecond })

	if cause := context.Cause(ctx); !errors.Is(cause, errFirst) || errors.Is(cause, errSecond) {
		t.Errorf("cause = %v, want the first failure %v", cause, errFirst)
	}
}
