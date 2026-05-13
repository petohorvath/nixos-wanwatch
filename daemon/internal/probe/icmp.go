// ICMP and ICMPv6 echo client building blocks.
//
// icmp.go covers the *pure* part of probing — message construction
// and parsing — so the wire format is unit-testable without raw
// sockets or netns setup. The socket-bound Pinger that drives this
// in production lives next to it (added in Pass 4, when the
// goroutine orchestration that calls it also lands).
//
// Wire format reminders:
//
//   ICMPv4 echo request: type=8,   code=0, checksum, identifier, sequence, payload
//   ICMPv4 echo reply:   type=0,   code=0, checksum, identifier, sequence, payload (echoed)
//   ICMPv6 echo request: type=128, code=0, checksum, identifier, sequence, payload
//   ICMPv6 echo reply:   type=129, code=0, checksum, identifier, sequence, payload
//
// ICMPv4 checksums are computed by this code (RFC 1071, the
// standard one's-complement sum). ICMPv6 checksums are computed
// by the kernel on transmit: Linux's `net/ipv6/raw.c` auto-enables
// `IPV6_CHECKSUM` at offset 2 for IPPROTO_ICMPV6 raw sockets
// (which is what `net.ListenPacket("ip6:ipv6-icmp", …)` opens),
// so we leave the checksum field zero in
// echoRequestBytes(FamilyV6, …) and rely on the kernel to fill it.
// The IPv6 pseudo-header is not visible to userspace, so we could
// not compute the checksum ourselves anyway.

package probe

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Family is the IP family the probe operates in. Values match the
// kernel's AF_INET / AF_INET6 constants so the same value flows
// through to netlink (in `internal/apply`) without conversion.
type Family int

const (
	// FamilyV4 selects ICMP over IPv4 (type 8 / 0).
	FamilyV4 Family = unix.AF_INET
	// FamilyV6 selects ICMPv6 (type 128 / 129).
	FamilyV6 Family = unix.AF_INET6
)

// AllFamilies enumerates every Family the daemon supports. Use it
// where code iterates "do this per family" so adding a new family
// only touches the enum, not the call sites.
var AllFamilies = []Family{FamilyV4, FamilyV6}

// String returns the family's display name — "v4" / "v6". Used in
// log messages, metrics labels, and hook env vars.
func (f Family) String() string {
	switch f {
	case FamilyV4:
		return "v4"
	case FamilyV6:
		return "v6"
	default:
		return fmt.Sprintf("Family(%d)", int(f))
	}
}

// ICMP message-type constants.
const (
	icmpv4EchoRequest = 8
	icmpv4EchoReply   = 0
	icmpv6EchoRequest = 128
	icmpv6EchoReply   = 129
)

// EchoRequestBytes builds an ICMP/ICMPv6 echo request, ready to be
// passed to a packet conn's WriteTo. ident is the 16-bit identifier
// (one allocation per (WAN, family) — see PLAN §8 internal/probe);
// seq is the per-socket monotonic sequence number, modulo 2^16.
// payload is appended after the 8-byte header — zero-length payload
// is fine but a few bytes help with debugging in tcpdump traces.
//
// For FamilyV4 the IPv4 checksum is computed and inserted here.
// For FamilyV6 the kernel computes the checksum on transmit, so
// the checksum field is left zero.
func EchoRequestBytes(family Family, ident, seq uint16, payload []byte) []byte {
	msgType := byte(icmpv4EchoRequest)
	if family == FamilyV6 {
		msgType = icmpv6EchoRequest
	}
	b := make([]byte, 8+len(payload))
	b[0] = msgType
	b[1] = 0 // code
	// b[2:4] is checksum; filled below for v4, kernel-filled for v6.
	binary.BigEndian.PutUint16(b[4:6], ident)
	binary.BigEndian.PutUint16(b[6:8], seq)
	copy(b[8:], payload)

	if family == FamilyV4 {
		binary.BigEndian.PutUint16(b[2:4], internetChecksum(b))
	}
	return b
}

// Sentinel errors returned by ParseEchoReply.
var (
	// ErrReplyTooShort is returned when a message is too short to
	// hold a valid echo header.
	ErrReplyTooShort = errors.New("icmp: reply too short")
	// ErrUnexpectedType is returned when a message's type byte does
	// not match the expected echo-reply type for its family.
	ErrUnexpectedType = errors.New("icmp: unexpected message type")
)

// ParseEchoReply parses an ICMP/ICMPv6 echo reply and returns the
// identifier and sequence number it carried. Returns ErrReplyTooShort
// or ErrUnexpectedType (wrapped) on a malformed or wrong-type message.
//
// The caller is responsible for matching ident against its allocated
// (WAN, family) identifier and seq against its outstanding-probe map.
// ParseEchoReply does not perform the match — it just decodes.
func ParseEchoReply(family Family, data []byte) (ident, seq uint16, err error) {
	if len(data) < 8 {
		return 0, 0, fmt.Errorf("%w: %d bytes (need 8)", ErrReplyTooShort, len(data))
	}
	expected := byte(icmpv4EchoReply)
	if family == FamilyV6 {
		expected = icmpv6EchoReply
	}
	if data[0] != expected {
		return 0, 0, fmt.Errorf("%w: got %d, want %d", ErrUnexpectedType, data[0], expected)
	}
	ident = binary.BigEndian.Uint16(data[4:6])
	seq = binary.BigEndian.Uint16(data[6:8])
	return ident, seq, nil
}

// internetChecksum computes the Internet checksum (RFC 1071) over b.
// Used for ICMPv4 echo-request packets. The two-byte checksum slot in
// b must be zero before calling this — the result is what to write
// into those bytes.
func internetChecksum(b []byte) uint16 {
	var sum uint32
	n := len(b)
	for i := 0; i+1 < n; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
	}
	if n%2 == 1 {
		sum += uint32(b[n-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}
