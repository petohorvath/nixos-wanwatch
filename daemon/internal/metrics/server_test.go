package metrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scrapeSocket opens a single HTTP scrape against `socket` and returns
// the body. Mirrors what Telegraf does — keeps the test honest
// about the Unix-socket wire path, not just the in-memory handler.
func scrapeSocket(socket string) (string, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socket)
			},
		},
		Timeout: 2 * time.Second,
	}
	// The "host" portion is irrelevant for unix-socket dials.
	resp, err := client.Get("http://wanwatch/metrics")
	if err != nil {
		return "", fmt.Errorf("GET /metrics: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ReadAll: %w", err)
	}
	return string(body), nil
}

// dialUnix is scrapeSocket for a server that runServer reports as serving.
func dialUnix(t *testing.T, socket string) string {
	t.Helper()
	body, err := scrapeSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// runServer starts Serve in a goroutine, waits until it answers a
// scrape, and returns the cancel func + the goroutine's error
// channel. Callers cancel to stop and read errCh for the result.
//
// The socket file appears at Listen, before Serve applies Mode and
// before a stale file is replaced; only an answered scrape shows
// that Serve has finished both.
func runServer(t *testing.T, s *Server) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := scrapeSocket(s.Socket)
		if err == nil {
			return cancel, errCh
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server on %s never served: %v", s.Socket, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeBindsAndServes(t *testing.T) {
	t.Parallel()
	r := New()
	r.WanCarrier.WithLabelValues("primary").Set(1)

	socket := filepath.Join(t.TempDir(), "metrics.sock")
	s := &Server{Socket: socket, Handler: r.Handler()}
	cancel, errCh := runServer(t, s)

	body := dialUnix(t, socket)
	if !strings.Contains(body, `wanwatch_wan_carrier{wan="primary"} 1`) {
		t.Errorf("scrape body missing the recorded gauge; got:\n%s", body)
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Errorf("Serve = %v, want context.Canceled", err)
	}
}

func TestServeAppliesSocketMode(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(t.TempDir(), "metrics.sock")
	s := &Server{Socket: socket, Handler: New().Handler(), Mode: 0o600}
	cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()

	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 0600", got)
	}
}

func TestServeRemovesStaleSocket(t *testing.T) {
	t.Parallel()
	// A stale file at Socket from a prior crash must not block the
	// listener bind — Serve removes it before listen.
	socket := filepath.Join(t.TempDir(), "metrics.sock")
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := &Server{Socket: socket, Handler: New().Handler()}
	cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()
	// Reaching runServer's socket-poll loop means listen succeeded
	// despite the stale file — the bare assertion is enough.
}

func TestServeRejectsEmptySocket(t *testing.T) {
	t.Parallel()
	s := &Server{Handler: New().Handler()}
	err := s.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Socket is empty") {
		t.Errorf("Serve(empty socket) = %v, want 'Socket is empty'", err)
	}
}

func TestServeRejectsNilHandler(t *testing.T) {
	t.Parallel()
	s := &Server{Socket: filepath.Join(t.TempDir(), "m.sock")}
	err := s.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Handler is nil") {
		t.Errorf("Serve(nil handler) = %v, want 'Handler is nil'", err)
	}
}
