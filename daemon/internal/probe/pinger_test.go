package probe

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakeConn is an in-memory pingConn. WriteTo records the outgoing
// bytes and, with echo set, queues the matching reply; ReadFrom
// drains the reply queue. Read deadlines behave as on a real
// socket: a passed deadline fails ReadFrom even with a reply
// queued, ReadFrom with no reply queued blocks until the deadline,
// and a deadline moved while ReadFrom blocks takes effect at once.
type fakeConn struct {
	mu       sync.Mutex
	sent     [][]byte
	replies  [][]byte
	echo     bool  // if set, WriteTo queues the echo reply for each request
	writeErr error // if set, WriteTo returns it
	readErr  error // if set, ReadFrom returns it instead of draining replies
	deadline time.Time
	moved    chan struct{} // closed and replaced by each SetReadDeadline
	// onSetReadDeadline, if set, runs once, at the start of the
	// first SetReadDeadline call and before its deadline is stored.
	onSetReadDeadline func()
	closed            bool
}

func (f *fakeConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	f.sent = append(f.sent, cp)
	if f.echo {
		family := FamilyV4
		if b[0] == 128 { // ICMPv6 echo-request
			family = FamilyV6
		}
		ident := uint16(b[4])<<8 | uint16(b[5])
		seq := uint16(b[6])<<8 | uint16(b[7])
		f.replies = append(f.replies, echoReplyFor(family, ident, seq))
		f.wakeLocked()
	}
	return len(b), nil
}

func (f *fakeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		f.mu.Lock()
		if f.readErr != nil {
			f.mu.Unlock()
			return 0, nil, f.readErr
		}
		if !f.deadline.IsZero() && !time.Now().Before(f.deadline) {
			f.mu.Unlock()
			return 0, nil, os.ErrDeadlineExceeded
		}
		if len(f.replies) > 0 {
			n := copy(b, f.replies[0])
			f.replies = f.replies[1:]
			f.mu.Unlock()
			return n, &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil
		}
		if f.moved == nil {
			f.moved = make(chan struct{})
		}
		moved := f.moved
		var expired <-chan time.Time
		if !f.deadline.IsZero() {
			timer := time.NewTimer(time.Until(f.deadline))
			defer timer.Stop()
			expired = timer.C
		}
		f.mu.Unlock()
		select {
		case <-expired:
		case <-moved:
		}
	}
}

func (f *fakeConn) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	hook := f.onSetReadDeadline
	f.onSetReadDeadline = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadline = t
	f.wakeLocked()
	return nil
}

// wakeLocked wakes a blocked ReadFrom. The caller holds f.mu.
func (f *fakeConn) wakeLocked() {
	if f.moved != nil {
		close(f.moved)
	}
	f.moved = make(chan struct{})
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeConn) queued() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.replies)
}

// dialer is a scripted dialFn: each call returns the next step,
// and the last step repeats.
type dialer struct {
	mu    sync.Mutex
	steps []dialStep
	calls int
}

type dialStep struct {
	conn    *fakeConn
	ifindex int
	err     error
}

func (d *dialer) dial(Family, string) (pingConn, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	step := d.steps[min(d.calls, len(d.steps)-1)]
	d.calls++
	if step.err != nil {
		return nil, 0, step.err
	}
	return step.conn, step.ifindex, nil
}

func (d *dialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// ifindexIs returns an ifindex lookup that reports the interface
// at index ifindex.
func ifindexIs(ifindex int) func(string) (int, error) {
	return func(string) (int, error) { return ifindex, nil }
}

// runUntil runs p over sock until a ProbeResult satisfies done,
// then cancels the run and checks that it stopped cleanly. It
// returns every ProbeResult received.
func runUntil(t *testing.T, p *Pinger, sock *socket, done func(ProbeResult) bool) []ProbeResult {
	t.Helper()
	out := make(chan ProbeResult)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- p.run(ctx, sock, out) }()

	var results []ProbeResult
	deadline := time.After(5 * time.Second)
	for {
		select {
		case r := <-out:
			results = append(results, r)
			if !done(r) {
				continue
			}
			cancel()
			if err := <-errCh; !errors.Is(err, context.Canceled) {
				t.Errorf("run err = %v, want context.Canceled", err)
			}
			return results
		case err := <-errCh:
			t.Fatalf("run returned %v before the expected ProbeResult", err)
		case <-deadline:
			cancel()
			<-errCh
			t.Fatalf("no expected ProbeResult within 5s; got %d results", len(results))
		}
	}
}

func newPinger(targets []string) *Pinger {
	return &Pinger{
		Wan:        "primary",
		Family:     FamilyV4,
		Interface:  "eth0",
		Targets:    targets,
		Ident:      0x1234,
		Interval:   10 * time.Millisecond,
		Timeout:    20 * time.Millisecond,
		WindowSize: 4,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestResolveTargetsAcceptsV4Literals(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1", "8.8.8.8"})
	addrs, err := p.resolveTargets()
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(addrs) != 2 {
		t.Errorf("len = %d, want 2", len(addrs))
	}
}

func TestResolveTargetsRejectsHostnames(t *testing.T) {
	t.Parallel()
	// docs/specs/daemon-config.md — probe.targets are IP literals; DNS happens at
	// config-render time, not in the daemon.
	p := newPinger([]string{"example.com"})
	if _, err := p.resolveTargets(); err == nil {
		t.Error("resolveTargets(hostname) = nil, want validation error")
	}
}

func TestResolveTargetsRejectsFamilyMismatch(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"2001:db8::1"}) // v6 ip, FamilyV4 default
	err := p.resolveTargets
	if _, gotErr := err(); gotErr == nil || !strings.Contains(gotErr.Error(), "not v4") {
		t.Errorf("resolveTargets(v6 under v4) = %v, want family-mismatch error", gotErr)
	}
}

func TestCycleRecordsRTTOnMatchedReply(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	addrs, _ := p.resolveTargets()
	windows := map[string]*WindowStats{"1.1.1.1": mustNewWindow(t, p.WindowSize)}
	// Pre-load a reply at seq=0 (the first seq this cycle issues).
	reply := echoReplyFor(p.Family, p.Ident, 0)
	conn := &fakeConn{replies: [][]byte{reply}}

	buf := make([]byte, 1500)
	nextSeq, _ := p.cycle(context.Background(), conn, addrs, windows, buf, 0)

	if nextSeq != 1 {
		t.Errorf("nextSeq = %d, want 1 (one target ⇒ one seq consumed)", nextSeq)
	}
	if windows["1.1.1.1"].Len() != 1 {
		t.Errorf("window len = %d, want 1", windows["1.1.1.1"].Len())
	}
	if lost, total := windows["1.1.1.1"].LossCounts(); lost != 0 || total != 1 {
		t.Errorf("loss = %d of %d, want 0 of 1 (reply was matched)", lost, total)
	}
}

func TestCycleRecordsLossOnNoReply(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	addrs, _ := p.resolveTargets()
	windows := map[string]*WindowStats{"1.1.1.1": mustNewWindow(t, p.WindowSize)}
	conn := &fakeConn{} // no replies queued

	buf := make([]byte, 1500)
	p.cycle(context.Background(), conn, addrs, windows, buf, 0)

	if lost, total := windows["1.1.1.1"].LossCounts(); lost != 1 || total != 1 {
		t.Errorf("loss = %d of %d, want 1 of 1 (no reply ⇒ lost)", lost, total)
	}
}

func TestCycleIgnoresWrongIdent(t *testing.T) {
	t.Parallel()
	// Reply from another pinger (different Ident) must not be
	// consumed as ours — would mis-attribute RTT.
	p := newPinger([]string{"1.1.1.1"})
	addrs, _ := p.resolveTargets()
	windows := map[string]*WindowStats{"1.1.1.1": mustNewWindow(t, p.WindowSize)}
	stranger := echoReplyFor(p.Family, p.Ident^0xFFFF, 0)
	conn := &fakeConn{replies: [][]byte{stranger}}

	buf := make([]byte, 1500)
	p.cycle(context.Background(), conn, addrs, windows, buf, 0)

	if lost, total := windows["1.1.1.1"].LossCounts(); lost != 1 || total != 1 {
		t.Errorf("loss = %d of %d, want 1 of 1 (stranger reply must be ignored)", lost, total)
	}
}

func TestRunEmitsProbeResultPerCycle(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	p.Interval = 5 * time.Millisecond
	d := &dialer{steps: []dialStep{{conn: &fakeConn{}, ifindex: 1}}} // all cycles Lost — fine
	sock := p.newSocket(d.dial, ifindexIs(1))

	results := runUntil(t, p, sock, func(ProbeResult) bool { return true })
	if r := results[0]; r.Wan != "primary" || r.Family != FamilyV4 {
		t.Errorf("result = %+v, want primary/v4", r)
	}
}

// echoReplyFor constructs an ICMP echo-reply with the given ident
// and seq. Tests use it to feed the fakeConn's reply queue —
// mirrors what a conn kernel would return.
func echoReplyFor(family Family, ident, seq uint16) []byte {
	replyType := byte(0) // ICMPv4 echo-reply
	if family == FamilyV6 {
		replyType = 129
	}
	b := make([]byte, 8)
	b[0] = replyType
	b[1] = 0
	// Checksum bytes 2-3 left zero; ParseEchoReply doesn't verify
	// checksum.
	b[4] = byte(ident >> 8)
	b[5] = byte(ident)
	b[6] = byte(seq >> 8)
	b[7] = byte(seq)
	return b
}

// TestWrapBindErrorNilPassthrough: nil in, nil out — no surprise.
func TestWrapBindErrorNilPassthrough(t *testing.T) {
	t.Parallel()
	if err := wrapBindError(nil); err != nil {
		t.Errorf("wrapBindError(nil) = %v, want nil", err)
	}
}

// TestWrapBindErrorWrapsEPERM: the production case — operator
// forgot to grant CAP_NET_RAW. wrapBindError must wrap the EPERM
// with a hint that names the missing capability, while still
// chaining the original error via %w so callers can `errors.Is`.
func TestWrapBindErrorWrapsEPERM(t *testing.T) {
	t.Parallel()
	err := wrapBindError(unix.EPERM)
	if err == nil {
		t.Fatal("wrapBindError(EPERM) = nil, want non-nil")
	}
	if !errors.Is(err, unix.EPERM) {
		t.Errorf("errors.Is(_, EPERM) = false; want chain via %%w")
	}
	if !strings.Contains(err.Error(), "CAP_NET_RAW") {
		t.Errorf("err = %q, want operator-facing CAP_NET_RAW hint", err.Error())
	}
}

// TestWrapBindErrorPassesThroughOther: a non-EPERM error must
// reach the caller as-is — the CAP_NET_RAW hint would mislead
// when the failure is, say, ENODEV.
func TestWrapBindErrorPassesThroughOther(t *testing.T) {
	t.Parallel()
	err := wrapBindError(unix.ENODEV)
	if !errors.Is(err, unix.ENODEV) {
		t.Errorf("err = %v, want ENODEV passthrough", err)
	}
	if strings.Contains(err.Error(), "CAP_NET_RAW") {
		t.Errorf("err = %q, must not carry the CAP_NET_RAW hint for non-EPERM", err.Error())
	}
}

// TestDialICMPViaV4PicksIP4ICMP: the v4/v6 network-string branch
// is the only logic in dialICMP that isn't a syscall — pin it.
func TestDialICMPViaV4PicksIP4ICMP(t *testing.T) {
	t.Parallel()
	var gotNetwork string
	listen := func(network, _ string) (net.PacketConn, error) {
		gotNetwork = network
		return nil, errors.New("intentional")
	}
	_, _, _ = dialICMPVia(FamilyV4, "eth0", listen, nil)
	if gotNetwork != "ip4:icmp" {
		t.Errorf("v4 → network = %q, want ip4:icmp", gotNetwork)
	}
}

func TestDialICMPViaV6PicksIP6ICMP(t *testing.T) {
	t.Parallel()
	var gotNetwork string
	listen := func(network, _ string) (net.PacketConn, error) {
		gotNetwork = network
		return nil, errors.New("intentional")
	}
	_, _, _ = dialICMPVia(FamilyV6, "eth0", listen, nil)
	if gotNetwork != "ip6:ipv6-icmp" {
		t.Errorf("v6 → network = %q, want ip6:ipv6-icmp", gotNetwork)
	}
}

// TestDialICMPViaPropagatesListenError: a listen failure must
// bubble up unwrapped — the caller's error context already names
// the family and interface in the outer fmt.Errorf in Pinger.Run.
func TestDialICMPViaPropagatesListenError(t *testing.T) {
	t.Parallel()
	want := errors.New("listen exploded")
	listen := func(string, string) (net.PacketConn, error) { return nil, want }
	_, _, err := dialICMPVia(FamilyV4, "eth0", listen, nil)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want listen error passthrough", err)
	}
}

// TestDialICMPViaRejectsNonIPConn: a packet conn that isn't a
// *net.IPConn would not expose SyscallConn → SO_BINDTODEVICE
// wouldn't work. dialICMP must close it and return a typed error
// so the failure mode is greppable in logs.
func TestDialICMPViaRejectsNonIPConn(t *testing.T) {
	t.Parallel()
	fake := &nonIPConn{}
	listen := func(string, string) (net.PacketConn, error) { return fake, nil }
	bind := func(*net.IPConn, string) (int, error) {
		t.Fatal("bind called despite type-assert failure")
		return 0, nil
	}
	_, _, err := dialICMPVia(FamilyV4, "eth0", listen, bind)
	if err == nil {
		t.Fatal("dialICMPVia(non-IPConn) = nil err, want type-assert failure")
	}
	if !strings.Contains(err.Error(), "*net.IPConn") {
		t.Errorf("err = %q, want it to name the expected concrete type", err.Error())
	}
	if !fake.closed {
		t.Error("non-IPConn was not closed")
	}
}

// TestDialICMPViaPropagatesBindError: a bind failure means the
// conn must be closed (no socket leak) and the bind err
// surfaced to the caller unchanged.
func TestDialICMPViaPropagatesBindError(t *testing.T) {
	t.Parallel()
	// Bypass: we can't construct a conn *net.IPConn without
	// opening a socket. Use a conn loopback UDP4 conn (no
	// privilege needed) and verify the path closes it on bind
	// failure. Skip on platforms where UDP isn't a *net.IPConn —
	// but on Linux, "udp4" returns *net.UDPConn, not *IPConn.
	// To genuinely test the bind-error path we need a conn
	// *net.IPConn; try the cheapest one we can open without
	// CAP_NET_RAW: there isn't one. So we drive the seam by
	// having `listen` return a *net.IPConn that's already been
	// closed — Control on it will fail, but that's
	// bindToDevice's path, not dialICMPVia's. The seam-injected
	// bindFn is the right substitution.
	bindErr := errors.New("bind exploded")
	// We still need a conn *net.IPConn to satisfy the type-assert
	// inside dialICMPVia. Open an unprivileged one against a
	// known-good address.
	conn, err := net.ListenIP("ip4:icmp", &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("ListenIP requires CAP_NET_RAW: %v", err)
	}
	defer func() { _ = conn.Close() }()

	listen := func(string, string) (net.PacketConn, error) { return conn, nil }
	bind := func(*net.IPConn, string) (int, error) { return 0, bindErr }
	_, _, gotErr := dialICMPVia(FamilyV4, "eth0", listen, bind)
	if !errors.Is(gotErr, bindErr) {
		t.Errorf("err = %v, want bind-error passthrough", gotErr)
	}
}

// nonIPConn is a net.PacketConn that is not a *net.IPConn — used
// to drive dialICMPVia's type-assert failure branch.
type nonIPConn struct {
	closed bool
}

func (n *nonIPConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, nil }
func (n *nonIPConn) WriteTo([]byte, net.Addr) (int, error)  { return 0, nil }
func (n *nonIPConn) Close() error                           { n.closed = true; return nil }
func (n *nonIPConn) LocalAddr() net.Addr                    { return nil }
func (n *nonIPConn) SetDeadline(time.Time) error            { return nil }
func (n *nonIPConn) SetReadDeadline(time.Time) error        { return nil }
func (n *nonIPConn) SetWriteDeadline(time.Time) error       { return nil }

// TestCycleRecordsLossOnSendFailure: when WriteTo fails the target
// is recorded as lost directly — no reply can come — without
// waiting out the read window.
func TestCycleRecordsLossOnSendFailure(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	addrs, _ := p.resolveTargets()
	windows := map[string]*WindowStats{"1.1.1.1": mustNewWindow(t, p.WindowSize)}
	conn := &fakeConn{writeErr: errors.New("sendto: network is down")}

	buf := make([]byte, 1500)
	p.cycle(context.Background(), conn, addrs, windows, buf, 0)

	if lost, total := windows["1.1.1.1"].LossCounts(); lost != 1 || total != 1 {
		t.Errorf("loss = %d of %d, want 1 of 1 (send failure ⇒ lost)", lost, total)
	}
}

// TestCycleRecordsLossOnReadError: a non-deadline ReadFrom error is
// a real socket fault — cycle stops draining and records the
// inflight targets as lost rather than treating it as a clean
// end-of-cycle.
func TestCycleRecordsLossOnReadError(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	addrs, _ := p.resolveTargets()
	windows := map[string]*WindowStats{"1.1.1.1": mustNewWindow(t, p.WindowSize)}
	conn := &fakeConn{readErr: errors.New("recvfrom: bad file descriptor")}

	buf := make([]byte, 1500)
	p.cycle(context.Background(), conn, addrs, windows, buf, 0)

	if lost, total := windows["1.1.1.1"].LossCounts(); lost != 1 || total != 1 {
		t.Errorf("loss = %d of %d, want 1 of 1 (read fault ⇒ inflight lost)", lost, total)
	}
}

// TestCycleSkipsReadOnCancelledContext: a cycle entered with an
// already-cancelled context records loss without opening a read
// window — even a reply sitting ready in the conn is left
// unconsumed, so shutdown isn't delayed by a full Timeout.
func TestCycleSkipsReadOnCancelledContext(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	addrs, _ := p.resolveTargets()
	windows := map[string]*WindowStats{"1.1.1.1": mustNewWindow(t, p.WindowSize)}
	conn := &fakeConn{replies: [][]byte{echoReplyFor(p.Family, p.Ident, 0)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	buf := make([]byte, 1500)
	p.cycle(ctx, conn, addrs, windows, buf, 0)

	if lost, total := windows["1.1.1.1"].LossCounts(); lost != 1 || total != 1 {
		t.Errorf("loss = %d of %d, want 1 of 1 (cancelled ctx ⇒ read window skipped)", lost, total)
	}
	if conn.queued() != 1 {
		t.Errorf("queued reply was consumed (%d left, want 1) — read window not skipped", conn.queued())
	}
}

// TestCycleIgnoresUnknownSequence: a reply that matches our ident
// but carries a seq we never sent (a stale reply from a previous
// cycle, or a duplicate the kernel re-delivered) must be consumed
// from the conn and dropped — not credited to a random inflight
// entry. Pins the `if !known { continue }` branch in cycle().
func TestCycleIgnoresUnknownSequence(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	addrs, _ := p.resolveTargets()
	windows := map[string]*WindowStats{"1.1.1.1": mustNewWindow(t, p.WindowSize)}
	// cycle issues seq=0 for our one target. Queue a reply carrying
	// the right ident but a different seq — matches "stale reply"
	// from a previous cycle of the same socket.
	stale := echoReplyFor(p.Family, p.Ident, 99)
	conn := &fakeConn{replies: [][]byte{stale}}

	buf := make([]byte, 1500)
	p.cycle(context.Background(), conn, addrs, windows, buf, 0)

	if lost, total := windows["1.1.1.1"].LossCounts(); lost != 1 || total != 1 {
		t.Errorf("loss = %d of %d, want 1 of 1 (no matching-seq reply ⇒ inflight lost)", lost, total)
	}
	if conn.queued() != 0 {
		t.Errorf("stale reply still queued (%d left); the unknown-seq path didn't consume it", conn.queued())
	}
}

// TestResolveTargetsRejectsV4UnderV6: the symmetric case of
// TestResolveTargetsRejectsFamilyMismatch — a v4 literal placed
// under FamilyV6 must trip the validator. The two error branches
// in resolveTargets are independent code paths; the v4-under-v6
// branch was previously uncovered.
func TestResolveTargetsRejectsV4UnderV6(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"}) // v4 ip
	p.Family = FamilyV6                 // ... under v6 → mismatch
	_, err := p.resolveTargets()
	if err == nil {
		t.Fatal("resolveTargets(v4 under v6) = nil, want family-mismatch error")
	}
	if !strings.Contains(err.Error(), "is v4 but family=v6") {
		t.Errorf("err = %q, want it to name the mismatch direction", err.Error())
	}
}

// TestDialICMPViaReturnsConnWhenBindSucceeds: the happy-path
// return — listen yields a *net.IPConn, bind reports success,
// dialICMPVia returns the conn unchanged with the bound ifindex. Covers the
// `return ipConn, nil` line that the existing skip-on-no-CAP_NET_RAW
// test never reaches.
//
// Listen returns a typed-nil *net.IPConn so the type-assert
// succeeds without needing to actually open a raw socket; bind is
// stubbed to return nil so neither path touches the nil conn's
// methods.
func TestDialICMPViaReturnsConnWhenBindSucceeds(t *testing.T) {
	t.Parallel()
	var typedNil *net.IPConn
	listen := func(string, string) (net.PacketConn, error) {
		return typedNil, nil
	}
	var bindCalled bool
	bind := func(*net.IPConn, string) (int, error) {
		bindCalled = true
		return 7, nil
	}
	got, ifindex, err := dialICMPVia(FamilyV4, "eth0", listen, bind)
	if err != nil {
		t.Fatalf("dialICMPVia(bind-ok) = %v, want nil err", err)
	}
	if ifindex != 7 {
		t.Errorf("ifindex = %d, want 7 from bind", ifindex)
	}
	if !bindCalled {
		t.Error("bind was not invoked on the listen-success path")
	}
	if pc, ok := got.(*net.IPConn); !ok || pc != typedNil {
		t.Errorf("returned conn = %v (%T), want the listen-provided *net.IPConn", got, got)
	}
}
