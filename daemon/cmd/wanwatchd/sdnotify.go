package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"
)

// sdNotify sends one status datagram — "READY=1", "WATCHDOG=1", … —
// to the socket in $NOTIFY_SOCKET. It returns nil when the variable
// is unset (the daemon isn't running under a Type=notify unit), so a
// `go run` launch or the test binary treats the no-systemd case as
// success rather than an error.
//
// No dependency: the sd_notify protocol is a single datagram write,
// and the env contract is ABI-stable, so stdlib net is enough.
func sdNotify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	// systemd encodes an abstract-namespace socket with a leading
	// '@'; Go's net package wants a leading NUL byte for the same.
	name := socket
	if name[0] == '@' {
		name = "\x00" + name[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("sd_notify: dial %s: %w", socket, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("sd_notify: write %q: %w", state, err)
	}
	return nil
}

// watchdogInterval returns the cadence at which the daemon must send
// WATCHDOG=1, or 0 when the unit declared no WatchdogSec. systemd
// puts the timeout in WATCHDOG_USEC; the daemon pings at half that,
// the documented safety margin against a late keepalive.
func watchdogInterval() time.Duration {
	usecStr := os.Getenv("WATCHDOG_USEC")
	if usecStr == "" {
		return 0
	}
	// WATCHDOG_PID, when set, scopes the watchdog to one PID — a
	// value naming a different process means the keepalive isn't ours
	// to send.
	if pidStr := os.Getenv("WATCHDOG_PID"); pidStr != "" {
		if pid, err := strconv.Atoi(pidStr); err != nil || pid != os.Getpid() {
			return 0
		}
	}
	usec, err := strconv.Atoi(usecStr)
	if err != nil || usec <= 0 {
		return 0
	}
	return time.Duration(usec) * time.Microsecond / 2
}

// watchdogChallenge asks eventLoop to prove it can still dispatch.
// Closing ack is the response; a fresh channel per challenge makes
// acknowledgments impossible to reuse across watchdog intervals.
type watchdogChallenge struct {
	ack chan struct{}
}

// runWatchdog challenges the event loop every watchdogInterval() and
// sends WATCHDOG=1 only after that challenge is acknowledged. If the
// event loop is wedged, this goroutine waits without sending another
// keepalive, allowing systemd to restart the process. Cancellation
// unblocks both sides of the handshake during orderly shutdown.
//
// A failed ping is logged, not fatal: missing the keepalive and
// letting systemd restart the daemon is the intended failure mode.
func runWatchdog(ctx context.Context, logger *slog.Logger, challenges chan<- watchdogChallenge) {
	interval := watchdogInterval()
	if interval <= 0 {
		return
	}
	logger.Info("sd_notify watchdog active", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			challenge := watchdogChallenge{ack: make(chan struct{})}
			select {
			case <-ctx.Done():
				return
			case challenges <- challenge:
			}
			select {
			case <-ctx.Done():
				return
			case <-challenge.ack:
			}
			if err := sdNotify("WATCHDOG=1"); err != nil {
				logger.Warn("sd_notify watchdog ping failed", "err", err)
			}
		}
	}
}
