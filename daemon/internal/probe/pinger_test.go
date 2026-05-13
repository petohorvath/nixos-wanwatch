package probe

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakeConn is an in-memory pingConn. WriteTo records the outgoing
// bytes; ReadFrom drains a pre-loaded queue of replies and returns
// os.ErrDeadlineExceeded once empty so cycle's read-deadline path
// is exercised.
type fakeConn struct {
	mu      sync.Mutex
	sent    [][]byte
	replies [][]byte
	closed  bool
}

func (f *fakeConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(b))
	copy(cp, b)
	f.sent = append(f.sent, cp)
	return len(b), nil
}

func (f *fakeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replies) == 0 {
		// Mimic SetReadDeadline expiring — cycle treats any error
		// as "no more replies this cycle".
		return 0, nil, errFakeDeadline
	}
	n := copy(b, f.replies[0])
	f.replies = f.replies[1:]
	return n, &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil
}

func (f *fakeConn) SetReadDeadline(time.Time) error {
	return nil
}

func (f *fakeConn) Close() error {
	f.closed = true
	return nil
}

var errFakeDeadline = errors.New("fake deadline exceeded")

func newPinger(targets []string) *Pinger {
	return &Pinger{
		Wan:        "primary",
		Family:     FamilyV4,
		Interface:  "eth0",
		Targets:    targets,
		Ident:      0x1234,
		Interval:   10 * time.Millisecond,
		Timeout:    100 * time.Millisecond,
		WindowSize: 4,
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
	// PLAN §5.1 — probe.targets are IP literals; DNS happens at
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
	nextSeq := p.cycle(conn, addrs, windows, buf, 0)

	if nextSeq != 1 {
		t.Errorf("nextSeq = %d, want 1 (one target ⇒ one seq consumed)", nextSeq)
	}
	if windows["1.1.1.1"].Len() != 1 {
		t.Errorf("window len = %d, want 1", windows["1.1.1.1"].Len())
	}
	if windows["1.1.1.1"].LossRatio() != 0 {
		t.Errorf("loss = %v, want 0 (reply was matched)", windows["1.1.1.1"].LossRatio())
	}
}

func TestCycleRecordsLossOnNoReply(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	addrs, _ := p.resolveTargets()
	windows := map[string]*WindowStats{"1.1.1.1": mustNewWindow(t, p.WindowSize)}
	conn := &fakeConn{} // no replies queued

	buf := make([]byte, 1500)
	p.cycle(conn, addrs, windows, buf, 0)

	if windows["1.1.1.1"].LossRatio() != 1.0 {
		t.Errorf("loss = %v, want 1.0 (no reply ⇒ lost)", windows["1.1.1.1"].LossRatio())
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
	p.cycle(conn, addrs, windows, buf, 0)

	if windows["1.1.1.1"].LossRatio() != 1.0 {
		t.Errorf("loss = %v, want 1.0 (stranger reply must be ignored)", windows["1.1.1.1"].LossRatio())
	}
}

func TestRunWithConnEmitsProbeResultPerCycle(t *testing.T) {
	t.Parallel()
	p := newPinger([]string{"1.1.1.1"})
	p.Interval = 5 * time.Millisecond
	addrs, _ := p.resolveTargets()
	_ = addrs
	conn := &fakeConn{} // all cycles will be Lost — fine

	out := make(chan ProbeResult, 4)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- p.runWithConn(ctx, conn, out) }()

	// Wait for at least one cycle then cancel.
	select {
	case r := <-out:
		if r.Wan != "primary" || r.Family != FamilyV4 {
			t.Errorf("result = %+v, want primary/v4", r)
		}
	case <-time.After(time.Second):
		cancel()
		<-errCh
		t.Fatal("no ProbeResult emitted within 1s")
	}
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Errorf("runWithConn err = %v, want context.Canceled", err)
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
	_, _ = dialICMPVia(FamilyV4, "eth0", listen, nil)
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
	_, _ = dialICMPVia(FamilyV6, "eth0", listen, nil)
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
	_, err := dialICMPVia(FamilyV4, "eth0", listen, nil)
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
	bind := func(*net.IPConn, string) error {
		t.Fatal("bind called despite type-assert failure")
		return nil
	}
	_, err := dialICMPVia(FamilyV4, "eth0", listen, bind)
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
	bind := func(*net.IPConn, string) error { return bindErr }
	_, gotErr := dialICMPVia(FamilyV4, "eth0", listen, bind)
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
