package probe

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestRunReopensSocketOfRecreatedInterface: a recreated interface
// has a new ifindex, so every send on the old socket fails — with
// ENODEV on v4 and ENETUNREACH on v6. The Pinger must close the
// stale socket, re-dial, and resume measuring the WAN.
func TestRunReopensSocketOfRecreatedInterface(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		sendErr error
	}{
		{"v4 ENODEV", unix.ENODEV},
		{"v6 ENETUNREACH", unix.ENETUNREACH},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPinger([]string{"1.1.1.1"})
			stale := &fakeConn{writeErr: tc.sendErr}
			fresh := &fakeConn{echo: true}
			d := &dialer{steps: []dialStep{{conn: stale, ifindex: 1}, {conn: fresh, ifindex: 2}}}
			sock := p.newSocket(d.dial, ifindexIs(2))

			runUntil(t, p, sock, func(r ProbeResult) bool { return r.Stats.RTTMicros != nil })

			if !stale.isClosed() {
				t.Error("stale socket was not closed")
			}
			if got := d.count(); got != 2 {
				t.Errorf("dials = %d, want 2 (initial and one re-dial)", got)
			}
		})
	}
}

// TestRunKeepsSocketWhileInterfaceIsDown: a down interface keeps
// its ifindex, so the socket is still valid and re-dialing would
// only churn sockets every Interval. Its Samples are lost.
func TestRunKeepsSocketWhileInterfaceIsDown(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	down := &fakeConn{writeErr: unix.ENETUNREACH}
	d := &dialer{steps: []dialStep{{conn: down, ifindex: 1}}}
	sock := p.newSocket(d.dial, ifindexIs(1))

	results := runUntil(t, p, sock, func(r ProbeResult) bool { return r.Stats.WindowFilled })

	if got := d.count(); got != 1 {
		t.Errorf("dials = %d, want 1 (no re-dial while the ifindex is unchanged)", got)
	}
	if down.isClosed() {
		t.Error("socket of a down interface was closed")
	}
	if loss := results[len(results)-1].Stats.LossRatio; loss == nil || *loss != 1 {
		t.Errorf("loss = %s, want 1 (sends fail while the interface is down)", show(loss))
	}
}

// TestRunWaitsForAbsentInterface: an interface absent at startup,
// such as a PPP link not yet up, is not fatal. The Pinger records
// lost Samples and retries the dial each Interval until the
// interface appears.
func TestRunWaitsForAbsentInterface(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	fresh := &fakeConn{echo: true}
	d := &dialer{steps: []dialStep{{err: unix.ENODEV}, {err: unix.ENODEV}, {conn: fresh, ifindex: 3}}}
	sock := p.newSocket(d.dial, ifindexIs(3))

	results := runUntil(t, p, sock, func(r ProbeResult) bool { return r.Stats.RTTMicros != nil })

	if loss := results[0].Stats.LossRatio; loss == nil || *loss != 1 {
		t.Errorf("first loss = %s, want 1 (no socket while the interface is absent)", show(loss))
	}
	if got := d.count(); got != 3 {
		t.Errorf("dials = %d, want 3 (two absent, one success)", got)
	}
}

// TestRunFailsOnFatalDialError: a dial error other than an absent
// interface, such as EPERM without CAP_NET_RAW, does not heal by
// retrying, so it stops the Pinger, at startup and on a re-dial.
func TestRunFailsOnFatalDialError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		steps []dialStep
	}{
		{"startup", []dialStep{{err: unix.EPERM}}},
		{"re-dial", []dialStep{{conn: &fakeConn{writeErr: unix.ENODEV}, ifindex: 1}, {err: unix.EPERM}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPinger([]string{"1.1.1.1"})
			d := &dialer{steps: tc.steps}
			sock := p.newSocket(d.dial, ifindexIs(2))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := p.run(ctx, sock, make(chan ProbeResult, 16))

			if !errors.Is(err, unix.EPERM) {
				t.Errorf("run err = %v, want EPERM", err)
			}
		})
	}
}

// TestRunEndsReadAtOnceWhenCancelledWhileArmingDeadline: a shutdown
// that lands while a cycle arms its read deadline must still end the
// read at once. A cancellation that pulled the deadline in just
// before the cycle pushed it out again would leave the read waiting
// out the full Timeout.
func TestRunEndsReadAtOnceWhenCancelledWhileArmingDeadline(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	p.Timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &fakeConn{}
	conn.onSetReadDeadline = func() {
		cancel()
		// Give a cancellation watcher time to pull the deadline in
		// before the cycle's own deadline lands.
		time.Sleep(50 * time.Millisecond)
	}
	d := &dialer{steps: []dialStep{{conn: conn, ifindex: 1}}}
	sock := p.newSocket(d.dial, ifindexIs(1))

	errCh := make(chan error, 1)
	go func() { errCh <- p.run(ctx, sock, make(chan ProbeResult, 16)) }()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("run err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run still reading 5s after cancellation; its deadline was overwritten")
	}
}

// TestSocketRecheck: after a failed send, the socket survives only
// when the interface still has the ifindex it is bound to. A lookup
// failure also closes it: re-dialing an intact interface is
// harmless, keeping a stale socket is not.
func TestSocketRecheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		ifindex  func(string) (int, error)
		wantKept bool
	}{
		{"same ifindex", ifindexIs(1), true},
		{"new ifindex", ifindexIs(2), false},
		{"interface gone", func(string) (int, error) { return 0, errors.New("no such network interface") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPinger([]string{"1.1.1.1"})
			conn := &fakeConn{}
			d := &dialer{steps: []dialStep{{conn: conn, ifindex: 1}}}
			sock := p.newSocket(d.dial, tc.ifindex)
			if err := sock.open(); err != nil {
				t.Fatalf("open: %v", err)
			}

			sock.recheck()

			if kept := sock.conn != nil && !conn.isClosed(); kept != tc.wantKept {
				t.Errorf("socket kept = %v, want %v", kept, tc.wantKept)
			}
		})
	}
}

// TestSocketOpenKeepsOpenSocket: open dials only when no socket is
// open, so calling it every cycle costs nothing while the socket is
// healthy.
func TestSocketOpenKeepsOpenSocket(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	d := &dialer{steps: []dialStep{{conn: &fakeConn{}, ifindex: 1}}}
	sock := p.newSocket(d.dial, ifindexIs(1))

	for range 3 {
		if err := sock.open(); err != nil {
			t.Fatalf("open: %v", err)
		}
	}

	if got := d.count(); got != 1 {
		t.Errorf("dials = %d, want 1", got)
	}
}

func TestInterfaceIndex(t *testing.T) {
	t.Parallel()
	if ifindex, err := interfaceIndex("lo"); err != nil || ifindex <= 0 {
		t.Errorf(`interfaceIndex("lo") = %d, %v; want a positive ifindex`, ifindex, err)
	}
	if _, err := interfaceIndex("wanwatch-none0"); err == nil {
		t.Error("interfaceIndex(missing) = nil err, want an error")
	}
}
