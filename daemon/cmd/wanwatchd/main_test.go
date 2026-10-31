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
	"testing/synctest"
	"time"
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

// TestKeepServingRetriesWithBackoff pins the metrics server's
// supervision: a failure, or a return without one, is retried after a
// delay that doubles from the minimum up to the cap, and resets after
// a run that lasted at least the cap. Nothing cancels the daemon.
func TestKeepServingRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	errListen := errors.New("listen: permission denied")
	backoff := retryBackoff{min: time.Second, max: 8 * time.Second}
	for _, tc := range []struct {
		name string
		// runs is how long each serve call runs before returning; the
		// call after the last blocks until the context is done.
		runs []time.Duration
		// wantGaps are the delays between one serve's return and the
		// next serve's start.
		wantGaps []time.Duration
	}{
		{"doubles up to the cap", make([]time.Duration, 6), []time.Duration{1, 2, 4, 8, 8, 8}},
		{"resets after a long run", []time.Duration{0, 0, 8 * time.Second, 0}, []time.Duration{1, 2, 1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				ctx, cancel := context.WithCancel(t.Context())
				var starts, returns []time.Time
				// Closed by the last call. Blocking on it, rather than on
				// synctest.Wait, lets the fake clock run through every
				// serve and delay.
				lastCall := make(chan struct{})
				serve := func() error {
					starts = append(starts, time.Now())
					call := len(starts) - 1
					if call == len(tc.runs) {
						close(lastCall)
						<-ctx.Done()
						return ctx.Err()
					}
					time.Sleep(tc.runs[call])
					returns = append(returns, time.Now())
					if call%2 == 0 {
						return errListen
					}
					return nil
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					keepServing(ctx, logger, "metrics server", serve, backoff)
				}()
				<-lastCall

				if got, want := len(starts), len(tc.runs)+1; got != want {
					t.Fatalf("serve calls = %d, want %d", got, want)
				}
				for i, want := range tc.wantGaps {
					if gap := starts[i+1].Sub(returns[i]); gap != want*time.Second {
						t.Errorf("delay before call %d = %v, want %v", i+2, gap, want*time.Second)
					}
				}
				if ctx.Err() != nil {
					t.Error("keepServing cancelled the daemon context")
				}
				cancel()
				synctest.Wait()
				select {
				case <-done:
				default:
					t.Error("keepServing still running after the context was cancelled")
				}
			})
		})
	}
}

// TestKeepServingStopsDuringBackoff: cancellation ends the wait before
// a retry, so shutdown doesn't sit out the delay.
func TestKeepServingStopsDuringBackoff(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			keepServing(ctx, logger, "metrics server", func() error {
				calls++
				return errors.New("listen: permission denied")
			}, retryBackoff{min: time.Minute, max: time.Hour})
		}()
		synctest.Wait() // waiting out the first delay
		start := time.Now()
		cancel()
		<-done
		if waited := time.Since(start); waited != 0 {
			t.Errorf("keepServing returned %v after cancellation, want at once", waited)
		}
		if calls != 1 {
			t.Errorf("serve calls = %d, want 1", calls)
		}
	})
}
