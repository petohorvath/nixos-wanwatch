package metrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// DefaultSocketMode is the file mode applied to the metrics socket
// when Server.Mode is zero. 0o660 lets the daemon's `wanwatch`
// group read it — Telegraf joins via supplementary group per
// PLAN §7.1.
const DefaultSocketMode os.FileMode = 0o660

// shutdownTimeout bounds how long Serve waits for in-flight scrapes
// to finish after ctx is cancelled. Prometheus scrapes are short
// (<1s typical) so 5s is generous.
const shutdownTimeout = 5 * time.Second

// Server binds the metrics endpoint to a Unix domain socket. One
// per daemon instance — Serve blocks until ctx is cancelled, so
// the daemon usually runs it in its own goroutine.
type Server struct {
	// Socket is the filesystem path of the Unix socket — required.
	// Pre-existing files at this path are removed at Serve so a
	// stale socket from a crash doesn't block startup.
	Socket string
	// Mode is the chmod applied after bind. Zero → DefaultSocketMode.
	Mode os.FileMode
	// Handler is the HTTP handler mounted at /metrics — typically
	// Registry.Handler(). Required.
	Handler http.Handler
}

// Serve listens on s.Socket and serves /metrics until ctx is
// cancelled or the listener errors. On ctx cancellation, in-flight
// requests get shutdownTimeout to finish before the listener is
// torn down.
func (s *Server) Serve(ctx context.Context) error {
	if s.Socket == "" {
		return fmt.Errorf("metrics: Socket is empty")
	}
	if s.Handler == nil {
		return fmt.Errorf("metrics: Handler is nil")
	}
	mode := s.Mode
	if mode == 0 {
		mode = DefaultSocketMode
	}

	// Stale socket from a previous crash blocks bind with EADDRINUSE.
	// Remove unconditionally — operating-on-then-handling-error is
	// safer than pre-checking with os.Stat (TOCTOU).
	_ = os.Remove(s.Socket)

	listener, err := net.Listen("unix", s.Socket)
	if err != nil {
		return fmt.Errorf("metrics: listen %s: %w", s.Socket, err)
	}
	if err := os.Chmod(s.Socket, mode); err != nil {
		_ = listener.Close()
		return fmt.Errorf("metrics: chmod %s: %w", s.Socket, err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", s.Handler)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
