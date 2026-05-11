package probe

import (
	"context"
	"errors"
	"fmt"
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
	Interface  string // bound via SO_BINDTODEVICE per PLAN §8
	Targets    []string
	Ident      uint16
	Interval   time.Duration
	Timeout    time.Duration
	WindowSize int
}

// Run opens the ICMP socket, binds it to the WAN interface, and
// blocks until ctx is cancelled or a fatal error occurs. ProbeResult
// values are sent on `out` after each cycle.
func (p *Pinger) Run(ctx context.Context, out chan<- ProbeResult) error {
	conn, err := dialICMP(p.Family, p.Interface)
	if err != nil {
		return fmt.Errorf("probe: open icmp %s on %s: %w", p.Family, p.Interface, err)
	}
	defer conn.Close()
	return p.runWithConn(ctx, conn, out)
}

// runWithConn is the cycle loop, extracted so tests can drive it
// with a fake pingConn instead of opening a real socket.
func (p *Pinger) runWithConn(ctx context.Context, conn pingConn, out chan<- ProbeResult) error {
	addrs, err := p.resolveTargets()
	if err != nil {
		return err
	}
	windows := make(map[string]*WindowStats, len(p.Targets))
	for _, t := range p.Targets {
		windows[t] = NewWindow(p.WindowSize)
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
			seq = p.cycle(conn, addrs, windows, buf, seq)
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
// timeout fires, then pushes a Sample (with measured RTT, or Lost)
// into each target's window. Returns the next sequence to use.
func (p *Pinger) cycle(conn pingConn, addrs map[string]net.Addr, windows map[string]*WindowStats, buf []byte, seq uint16) uint16 {
	type pending struct {
		target string
		sent   time.Time
	}
	inflight := make(map[uint16]pending, len(p.Targets))
	for _, target := range p.Targets {
		req := EchoRequestBytes(p.Family, p.Ident, seq, nil)
		if _, err := conn.WriteTo(req, addrs[target]); err == nil {
			inflight[seq] = pending{target: target, sent: time.Now()}
		} else {
			// Send failure → record loss directly; don't wait for
			// a reply that can't come.
			windows[target].Push(Sample{Lost: true})
		}
		seq++
	}

	deadline := time.Now().Add(p.Timeout)
	_ = conn.SetReadDeadline(deadline)
	for len(inflight) > 0 {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
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
		rttMicros := uint64(time.Since(entry.sent).Microseconds())
		windows[entry.target].Push(Sample{RTTMicros: rttMicros})
	}
	for _, entry := range inflight {
		windows[entry.target].Push(Sample{Lost: true})
	}
	return seq
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

// dialICMP opens a raw ICMP socket for `family` and binds it to
// `iface` via SO_BINDTODEVICE. Requires CAP_NET_RAW — the NixOS
// module hands the daemon that capability per PLAN §8.
//
// We use net.ListenPacket rather than x/net/icmp because we own
// the wire format (EchoRequestBytes / ParseEchoReply in icmp.go)
// and net.IPConn exposes SyscallConn, which x/net/icmp's wrapper
// doesn't surface — and SyscallConn is required for the
// SO_BINDTODEVICE setsockopt that prevents probe-traffic leakage.
func dialICMP(family Family, iface string) (pingConn, error) {
	network := "ip4:icmp"
	if family == FamilyV6 {
		network = "ip6:ipv6-icmp"
	}
	pc, err := net.ListenPacket(network, "")
	if err != nil {
		return nil, err
	}
	ipConn, ok := pc.(*net.IPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("probe: net.ListenPacket(%s) returned %T, want *net.IPConn", network, pc)
	}
	if err := bindToDevice(ipConn, iface); err != nil {
		_ = ipConn.Close()
		return nil, err
	}
	return ipConn, nil
}

// bindToDevice applies SO_BINDTODEVICE so the socket egresses out
// of the WAN under test rather than whichever interface the kernel
// would route to by default. Without this, a probe from `backup`
// could leak via `primary` and report `primary`'s health.
func bindToDevice(conn *net.IPConn, iface string) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		setErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	if setErr != nil {
		// EPERM here means CAP_NET_RAW wasn't granted — surface
		// it explicitly so the operator can fix the systemd unit
		// rather than chasing a vague "permission denied".
		if errors.Is(setErr, os.ErrPermission) {
			return fmt.Errorf("SO_BINDTODEVICE: %w (need CAP_NET_RAW)", setErr)
		}
		return setErr
	}
	return nil
}
