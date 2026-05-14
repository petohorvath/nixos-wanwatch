package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// listenNotify binds a unixgram socket in a temp dir, points
// $NOTIFY_SOCKET at it, and returns the conn for the test to read
// datagrams from. These tests touch process env, so none of them
// can run with t.Parallel().
func listenNotify(t *testing.T) *net.UnixConn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatalf("ListenUnixgram: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("NOTIFY_SOCKET", path)
	return conn
}

// readDatagram reads one datagram under a deadline, so a missing
// send fails the test instead of hanging it.
func readDatagram(t *testing.T, conn *net.UnixConn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return string(buf[:n])
}

func TestSdNotifyNoSocketIsNoop(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := sdNotify("READY=1"); err != nil {
		t.Errorf("sdNotify with no socket = %v, want nil", err)
	}
}

func TestSdNotifyDeliversDatagram(t *testing.T) {
	conn := listenNotify(t)
	if err := sdNotify("READY=1"); err != nil {
		t.Fatalf("sdNotify = %v, want nil", err)
	}
	if got := readDatagram(t, conn); got != "READY=1" {
		t.Errorf("datagram = %q, want %q", got, "READY=1")
	}
}

func TestSdNotifyAbstractSocket(t *testing.T) {
	// systemd hands an abstract-namespace socket as "@name"; Go's net
	// package wants a leading NUL. Bind the abstract listener and
	// confirm sdNotify translates the prefix.
	name := "\x00wanwatch-test-" + strconv.Itoa(os.Getpid())
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatalf("ListenUnixgram abstract: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("NOTIFY_SOCKET", "@"+name[1:])

	if err := sdNotify("WATCHDOG=1"); err != nil {
		t.Fatalf("sdNotify = %v, want nil", err)
	}
	if got := readDatagram(t, conn); got != "WATCHDOG=1" {
		t.Errorf("datagram = %q, want %q", got, "WATCHDOG=1")
	}
}

func TestWatchdogInterval(t *testing.T) {
	tests := []struct {
		name string
		usec string
		pid  string
		want time.Duration
	}{
		{"unset", "", "", 0},
		{"set, no pid scope", "30000000", "", 15 * time.Second},
		{"set, our pid", "30000000", strconv.Itoa(os.Getpid()), 15 * time.Second},
		{"set, other pid", "30000000", "1", 0},
		{"malformed usec", "not-a-number", "", 0},
		{"zero usec", "0", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WATCHDOG_USEC", tt.usec)
			t.Setenv("WATCHDOG_PID", tt.pid)
			if got := watchdogInterval(); got != tt.want {
				t.Errorf("watchdogInterval() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunWatchdogDisabledReturns(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan struct{})
	go func() {
		runWatchdog(context.Background(), logger)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWatchdog with no watchdog configured did not return")
	}
}

func TestRunWatchdogPingsUntilCancelled(t *testing.T) {
	conn := listenNotify(t)
	// 20 ms timeout → 10 ms ping cadence: fast enough to observe a
	// ping without slowing the suite.
	t.Setenv("WATCHDOG_USEC", "20000")
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runWatchdog(ctx, logger)
		close(done)
	}()

	if got := readDatagram(t, conn); got != "WATCHDOG=1" {
		t.Errorf("datagram = %q, want %q", got, "WATCHDOG=1")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWatchdog did not return after ctx cancel")
	}
}
