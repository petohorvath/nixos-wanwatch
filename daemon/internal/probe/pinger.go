package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// pingConn is the subset of *icmp.PacketConn the Pinger needs.
// Defined here so tests can substitute an in-memory fake without
// opening a raw socket.
type pingConn interface {
	WriteTo(b []byte, dst net.Addr) (int, error)
	ReadFrom(b []byte) (int, net.Addr, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// Pinger drives one ICMP socket per (WAN, family). It cycles
// through Targets at Interval, pushes a Sample into the per-target
// window after each cycle (or on timeout), aggregates into a
// FamilyStats, and emits a ProbeResult.
type Pinger struct {
	Wan        string
	Family     Family
	Interface  string // bound via SO_BINDTODEVICE per docs/specs/probe-algorithm.md
	Targets    []string
	Ident      uint16
	Interval   time.Duration
	Timeout    time.Duration
	WindowSize int
	Logger     *slog.Logger // nil is normalized to a discard logger
}

// Run probes until ctx is cancelled or a fatal error occurs.
// ProbeResult values are sent on `out` after each cycle. Its ICMP
// socket is bound to the WAN interface and re-dialed when the
// interface is recreated; an absent interface is retried each
// Interval, with every Sample lost meanwhile.
func (p *Pinger) Run(ctx context.Context, out chan<- ProbeResult) error {
	if p.Logger == nil {
		p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	sock := p.newSocket(dialICMP, interfaceIndex)
	defer sock.close()
	return p.run(ctx, sock, out)
}

// run is the cycle loop, extracted so tests can drive it with a
// scripted dialFn instead of opening a real socket.
func (p *Pinger) run(ctx context.Context, sock *socket, out chan<- ProbeResult) error {
	addrs, err := p.resolveTargets()
	if err != nil {
		return err
	}
	windows := make(map[string]*WindowStats, len(p.Targets))
	for _, t := range p.Targets {
		w, err := NewWindow(p.WindowSize)
		if err != nil {
			return fmt.Errorf("probe: target %q: %w", t, err)
		}
		windows[t] = w
	}
	if err := sock.open(); err != nil {
		return err
	}

	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()

	var seq uint16
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := sock.open(); err != nil {
				return err
			}
			if sock.conn == nil {
				// The interface is absent: no reply can come, so
				// every Target loses its Sample.
				for _, w := range windows {
					w.Push(Sample{Lost: true})
				}
			} else {
				var sendFailed bool
				seq, sendFailed = p.cycle(ctx, sock.conn, addrs, windows, buf, seq)
				if sendFailed {
					sock.recheck()
				}
			}
			result := ProbeResult{
				Wan:    p.Wan,
				Family: p.Family,
				Stats:  Aggregate(windows),
				Time:   time.Now().UTC(),
			}
			select {
			case out <- result:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// cycle issues one echo per target, drains replies until the cycle
// timeout fires (or ctx is cancelled), then pushes a Sample (with
// measured RTT, or Lost) into each target's window. Returns the
// next sequence to use, and whether any send failed.
func (p *Pinger) cycle(ctx context.Context, conn pingConn, addrs map[string]net.Addr, windows map[string]*WindowStats, buf []byte, seq uint16) (uint16, bool) {
	type pending struct {
		target string
		sent   time.Time
	}
	inflight := make(map[uint16]pending, len(p.Targets))
	var sendFailed bool
	for _, target := range p.Targets {
		req := EchoRequestBytes(p.Family, p.Ident, seq, nil)
		if _, err := conn.WriteTo(req, addrs[target]); err == nil {
			inflight[seq] = pending{target: target, sent: time.Now()}
		} else {
			// Send failed — record loss directly (no reply can come)
			// and log it: a persistently broken socket (ENETDOWN,
			// EPERM) otherwise looks identical to ordinary packet loss.
			p.Logger.Warn("probe send failed",
				"wan", p.Wan, "family", p.Family, "target", target, "err", err)
			windows[target].Push(Sample{Lost: true})
			sendFailed = true
		}
		seq++
	}

	recordLoss := func() {
		for _, entry := range inflight {
			windows[entry.target].Push(Sample{Lost: true})
		}
	}

	// A shutdown that landed mid-send skips the read window — opening
	// one would cost a needless read before the deadline pull below
	// ends it.
	if ctx.Err() != nil {
		recordLoss()
		return seq, sendFailed
	}

	_ = conn.SetReadDeadline(time.Now().Add(p.Timeout))
	// On cancellation, pull the deadline in so a blocked ReadFrom
	// returns at once instead of waiting out the Timeout. Registered
	// after the deadline is set, so the Timeout cannot overwrite it:
	// AfterFunc runs at once when ctx is already cancelled.
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetReadDeadline(time.Now())
	})
	defer stop()
	for len(inflight) > 0 {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			// os.ErrDeadlineExceeded ends the cycle normally — the
			// per-cycle deadline, or the cancellation's deadline pull.
			// Anything else is a real socket fault worth surfacing.
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				p.Logger.Warn("probe read failed",
					"wan", p.Wan, "family", p.Family, "err", err)
			}
			break
		}
		ident, replySeq, perr := ParseEchoReply(p.Family, buf[:n])
		if perr != nil || ident != p.Ident {
			continue
		}
		entry, known := inflight[replySeq]
		if !known {
			continue
		}
		delete(inflight, replySeq)
		//nolint:gosec // time.Since is monotonic, Microseconds() is non-negative
		rttMicros := uint64(time.Since(entry.sent).Microseconds())
		windows[entry.target].Push(Sample{RTTMicros: rttMicros})
	}
	recordLoss()
	return seq, sendFailed
}

// resolveTargets parses each Target string as an IP literal in the
// Pinger's family. Done once at Run startup so the per-cycle path
// is allocation-free.
func (p *Pinger) resolveTargets() (map[string]net.Addr, error) {
	out := make(map[string]net.Addr, len(p.Targets))
	for _, t := range p.Targets {
		ip := net.ParseIP(t)
		if ip == nil {
			return nil, fmt.Errorf("probe: target %q is not a valid IP literal", t)
		}
		isV4 := ip.To4() != nil
		if p.Family == FamilyV4 && !isV4 {
			return nil, fmt.Errorf("probe: target %q is not v4 but family=v4", t)
		}
		if p.Family == FamilyV6 && isV4 {
			return nil, fmt.Errorf("probe: target %q is v4 but family=v6", t)
		}
		out[t] = &net.IPAddr{IP: ip}
	}
	return out, nil
}

// dialICMP opens a raw ICMP socket for `family`, binds it to
// `iface` via SO_BINDTODEVICE, and returns the bound ifindex. Requires CAP_NET_RAW — the NixOS
// module hands the daemon that capability per docs/specs/probe-algorithm.md.
//
// We use net.ListenPacket rather than x/net/icmp because we own
// the wire format (EchoRequestBytes / ParseEchoReply in icmp.go)
// and net.IPConn exposes SyscallConn, which x/net/icmp's wrapper
// doesn't surface — and SyscallConn is required for the
// SO_BINDTODEVICE setsockopt that prevents probe-traffic leakage.
func dialICMP(family Family, iface string) (pingConn, int, error) {
	return dialICMPVia(family, iface, net.ListenPacket, bindToDevice)
}

// listenPacketFn matches net.ListenPacket. dialICMPVia takes it as
// a parameter so tests can substitute a stub that returns a
// chosen net.PacketConn (or an error) without needing CAP_NET_RAW.
type listenPacketFn func(network, address string) (net.PacketConn, error)

// bindToDeviceFn matches the bindToDevice signature for the same
// reason — without it, tests of dialICMPVia would still need a
// real raw socket to reach the bind step.
type bindToDeviceFn func(conn *net.IPConn, iface string) (int, error)

// dialICMPVia is dialICMP parameterized on its two syscall-touching
// dependencies. Same control flow, but the v4/v6 network-string
// branch, the *net.IPConn type-assert branch, and the Close-on-
// error branches are now testable without netlink permissions.
func dialICMPVia(family Family, iface string, listen listenPacketFn, bind bindToDeviceFn) (pingConn, int, error) {
	network := "ip4:icmp"
	if family == FamilyV6 {
		network = "ip6:ipv6-icmp"
	}
	pc, err := listen(network, "")
	if err != nil {
		return nil, 0, err
	}
	ipConn, ok := pc.(*net.IPConn)
	if !ok {
		_ = pc.Close()
		return nil, 0, fmt.Errorf("probe: net.ListenPacket(%s) returned %T, want *net.IPConn", network, pc)
	}
	ifindex, err := bind(ipConn, iface)
	if err != nil {
		_ = ipConn.Close()
		return nil, 0, err
	}
	return ipConn, ifindex, nil
}

// bindToDevice applies SO_BINDTODEVICE so the socket egresses out
// of the WAN under test rather than whichever interface the kernel
// would route to by default. Without this, a probe from `backup`
// could leak via `primary` and report `primary`'s health.
//
// It returns the ifindex the socket is bound to, read back from the
// socket rather than looked up by name, so a concurrent recreation
// of the interface cannot make the two disagree.
func bindToDevice(conn *net.IPConn, iface string) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var ifindex int
	var setErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		setErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		if setErr == nil {
			ifindex, setErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BINDTOIFINDEX)
		}
	})
	if ctrlErr != nil {
		return 0, ctrlErr
	}
	return ifindex, wrapBindError(setErr)
}

// wrapBindError converts the raw SetsockoptString error returned by
// bindToDevice into the daemon-facing form. EPERM is the only
// branch with a meaningful transform (the CAP_NET_RAW hint); the
// rest of the function exists to make that branch testable without
// the runtime needing actual CAP_NET_RAW (or its absence).
func wrapBindError(setErr error) error {
	if setErr == nil {
		return nil
	}
	// EPERM here means CAP_NET_RAW wasn't granted — surface it
	// explicitly so the operator can fix the systemd unit rather
	// than chasing a vague "permission denied".
	if errors.Is(setErr, os.ErrPermission) {
		return fmt.Errorf("SO_BINDTODEVICE: %w (need CAP_NET_RAW)", setErr)
	}
	return setErr
}
