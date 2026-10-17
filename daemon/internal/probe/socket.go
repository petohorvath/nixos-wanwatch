package probe

import (
	"errors"
	"fmt"
	"log/slog"
	"net"

	"golang.org/x/sys/unix"
)

// dialFn opens an ICMP socket for family bound to iface and returns
// the ifindex the socket is bound to. dialICMP is the production
// dialFn; tests substitute a scripted one.
type dialFn func(family Family, iface string) (pingConn, int, error)

// socket owns a Pinger's ICMP socket across the lifetime of its WAN
// interface. SO_BINDTODEVICE resolves the interface name to an
// ifindex once, at bind time. A PPP, WireGuard, LTE, or VLAN
// interface that is recreated gets a new ifindex and leaves the
// socket bound to the old one, so every send fails: with ENODEV on
// v4 and ENETUNREACH on v6. ENETUNREACH also means a merely down
// interface, so socket tells the two apart by ifindex, not by errno,
// and re-dials only a socket whose ifindex is gone.
//
// Only the Pinger's Run goroutine uses a socket.
type socket struct {
	wan     string
	family  Family
	iface   string
	dial    dialFn
	ifindex func(name string) (int, error)
	logger  *slog.Logger

	conn  pingConn // nil while the interface is absent or the socket is stale
	bound int      // the ifindex conn is bound to
	lost  bool     // a socket was lost or never opened; its reopening is logged
}

// newSocket returns the Pinger's socket, not yet open.
func (p *Pinger) newSocket(dial dialFn, ifindex func(string) (int, error)) *socket {
	return &socket{
		wan:     p.Wan,
		family:  p.Family,
		iface:   p.Interface,
		dial:    dial,
		ifindex: ifindex,
		logger:  p.Logger,
	}
}

// open dials the socket when none is open. An absent interface
// (ENODEV) is not an error: open leaves conn nil, and the next cycle
// retries. Any other dial error, such as EPERM without CAP_NET_RAW,
// does not heal by retrying and is returned.
func (s *socket) open() error {
	if s.conn != nil {
		return nil
	}
	conn, bound, err := s.dial(s.family, s.iface)
	if errors.Is(err, unix.ENODEV) {
		if !s.lost {
			s.logger.Warn("probe interface absent; retrying each interval",
				"wan", s.wan, "family", s.family, "interface", s.iface)
			s.lost = true
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("probe: open icmp %s on %s: %w", s.family, s.iface, err)
	}
	if s.lost {
		s.logger.Info("probe socket reopened",
			"wan", s.wan, "family", s.family, "interface", s.iface, "ifindex", bound)
		s.lost = false
	}
	s.conn, s.bound = conn, bound
	return nil
}

// recheck runs after a send fails. It closes the socket when the
// interface is gone or its name now has another ifindex, so the next
// open re-dials. A failed lookup also closes it: re-dialing an intact
// interface is harmless, and keeping a stale socket is not.
func (s *socket) recheck() {
	if s.conn == nil {
		return
	}
	if ifindex, err := s.ifindex(s.iface); err == nil && ifindex == s.bound {
		return
	}
	s.logger.Warn("probe interface recreated or gone; reopening socket",
		"wan", s.wan, "family", s.family, "interface", s.iface, "ifindex", s.bound)
	s.close()
	s.lost = true
}

// close closes the socket if one is open.
func (s *socket) close() {
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// interfaceIndex is the production ifindex lookup for recheck.
func interfaceIndex(name string) (int, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return 0, err
	}
	return ifi.Index, nil
}
