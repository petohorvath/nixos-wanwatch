package probe

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
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
	windows := map[string]*WindowStats{"1.1.1.1": NewWindow(p.WindowSize)}
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
	windows := map[string]*WindowStats{"1.1.1.1": NewWindow(p.WindowSize)}
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
	windows := map[string]*WindowStats{"1.1.1.1": NewWindow(p.WindowSize)}
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
// mirrors what a real kernel would return.
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
