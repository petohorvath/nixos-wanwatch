package probe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestFamilyString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		f    Family
		want string
	}{
		{FamilyV4, "v4"},
		{FamilyV6, "v6"},
		{Family(42), "Family(42)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.f.String(); got != tc.want {
				t.Errorf("Family(%d).String() = %q, want %q", int(tc.f), got, tc.want)
			}
		})
	}
}

func TestEchoRequestBytesV4Layout(t *testing.T) {
	t.Parallel()
	const (
		ident uint16 = 0x1234
		seq   uint16 = 0x5678
	)
	payload := []byte("ping")
	b := EchoRequestBytes(FamilyV4, ident, seq, payload)

	if got, want := len(b), 8+len(payload); got != want {
		t.Fatalf("len(b) = %d, want %d", got, want)
	}
	if got, want := b[0], byte(8); got != want {
		t.Errorf("type byte = %d, want %d (echo request)", got, want)
	}
	if got, want := b[1], byte(0); got != want {
		t.Errorf("code byte = %d, want %d", got, want)
	}
	if got, want := binary.BigEndian.Uint16(b[4:6]), ident; got != want {
		t.Errorf("identifier = %#x, want %#x", got, want)
	}
	if got, want := binary.BigEndian.Uint16(b[6:8]), seq; got != want {
		t.Errorf("sequence = %#x, want %#x", got, want)
	}
	if !bytes.Equal(b[8:], payload) {
		t.Errorf("payload = %q, want %q", b[8:], payload)
	}
}

// TestEchoRequestBytesV4ChecksumValid: the checksum field must be set
// to the value that makes the whole packet's Internet-checksum zero.
// Confirms by re-running the checksum over the full packet (header
// + payload) and checking the result is zero.
func TestEchoRequestBytesV4ChecksumValid(t *testing.T) {
	t.Parallel()
	b := EchoRequestBytes(FamilyV4, 0xBEEF, 0x0001, []byte("wanwatch"))
	if got := internetChecksum(b); got != 0 {
		t.Errorf("checksum over signed packet = %#x, want 0 (any non-zero means the inserted checksum is wrong)", got)
	}
}

// TestEchoRequestBytesV6KernelChecksum: ICMPv6 leaves the checksum
// field zero — the kernel fills it on send (Linux auto-enables
// IPV6_CHECKSUM for raw ICMPv6 sockets), using the IPv6
// pseudo-header that userspace can't see.
func TestEchoRequestBytesV6KernelChecksum(t *testing.T) {
	t.Parallel()
	b := EchoRequestBytes(FamilyV6, 0xBEEF, 0x0001, []byte("wanwatch"))
	if got, want := b[0], byte(128); got != want {
		t.Errorf("v6 type byte = %d, want %d (echo request)", got, want)
	}
	if got := binary.BigEndian.Uint16(b[2:4]); got != 0 {
		t.Errorf("v6 checksum field = %#x, want 0 (kernel fills on send)", got)
	}
}

func TestEchoRequestBytesEmptyPayload(t *testing.T) {
	t.Parallel()
	b := EchoRequestBytes(FamilyV4, 1, 2, nil)
	if got, want := len(b), 8; got != want {
		t.Errorf("len(b) with nil payload = %d, want %d", got, want)
	}
	if got := internetChecksum(b); got != 0 {
		t.Errorf("checksum invariant broken on empty-payload packet: got %#x, want 0", got)
	}
}

func TestParseEchoReplyV4(t *testing.T) {
	t.Parallel()
	// Hand-craft an echo reply (type=0).
	b := make([]byte, 12)
	b[0] = 0 // echo reply
	b[1] = 0
	// Checksum left zero; ParseEchoReply doesn't verify it.
	binary.BigEndian.PutUint16(b[4:6], 0x1234)
	binary.BigEndian.PutUint16(b[6:8], 0x5678)
	copy(b[8:], []byte("data"))

	ident, seq, err := ParseEchoReply(FamilyV4, b)
	if err != nil {
		t.Fatalf("ParseEchoReply error: %v", err)
	}
	if ident != 0x1234 {
		t.Errorf("ident = %#x, want 0x1234", ident)
	}
	if seq != 0x5678 {
		t.Errorf("seq = %#x, want 0x5678", seq)
	}
}

func TestParseEchoReplyV6(t *testing.T) {
	t.Parallel()
	b := make([]byte, 8)
	b[0] = 129 // ICMPv6 echo reply
	binary.BigEndian.PutUint16(b[4:6], 0xABCD)
	binary.BigEndian.PutUint16(b[6:8], 0x0001)

	ident, seq, err := ParseEchoReply(FamilyV6, b)
	if err != nil {
		t.Fatalf("ParseEchoReply error: %v", err)
	}
	if ident != 0xABCD || seq != 0x0001 {
		t.Errorf("(ident, seq) = (%#x, %#x), want (0xABCD, 0x0001)", ident, seq)
	}
}

func TestParseEchoReplyRejectsShort(t *testing.T) {
	t.Parallel()
	_, _, err := ParseEchoReply(FamilyV4, []byte{0, 0, 0})
	if !errors.Is(err, ErrReplyTooShort) {
		t.Errorf("err = %v, want ErrReplyTooShort", err)
	}
}

func TestParseEchoReplyRejectsRequestTypeAsReply(t *testing.T) {
	t.Parallel()
	// A v4 echo *request* (type 8) is not a reply.
	b := make([]byte, 8)
	b[0] = 8
	_, _, err := ParseEchoReply(FamilyV4, b)
	if !errors.Is(err, ErrUnexpectedType) {
		t.Errorf("err = %v, want ErrUnexpectedType", err)
	}
}

func TestParseEchoReplyRejectsV6ReplyAsV4(t *testing.T) {
	t.Parallel()
	// v6 reply type (129) is not a v4 reply.
	b := make([]byte, 8)
	b[0] = 129
	_, _, err := ParseEchoReply(FamilyV4, b)
	if !errors.Is(err, ErrUnexpectedType) {
		t.Errorf("err = %v, want ErrUnexpectedType", err)
	}
}

func TestParseEchoReplyRejectsV4ReplyAsV6(t *testing.T) {
	t.Parallel()
	// v4 reply type (0) is not a v6 reply.
	b := make([]byte, 8)
	b[0] = 0
	_, _, err := ParseEchoReply(FamilyV6, b)
	if !errors.Is(err, ErrUnexpectedType) {
		t.Errorf("err = %v, want ErrUnexpectedType", err)
	}
}

// TestInternetChecksumKnownVector: the canonical RFC 1071 example.
// Input bytes 00 01 f2 03 f4 f5 f6 f7; expected one's-complement sum
// is 0xddf2 → checksum 0x220d.
func TestInternetChecksumKnownVector(t *testing.T) {
	t.Parallel()
	in := []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7}
	if got, want := internetChecksum(in), uint16(0x220d); got != want {
		t.Errorf("internetChecksum = %#x, want %#x", got, want)
	}
}

// FuzzParseEchoReply runs the parser over arbitrary byte payloads
// and asserts it never panics. The invariant for the return shape
// is narrow: either a clean parse (err==nil, ident/seq decoded
// from bytes 4–8 verbatim) or one of the two named sentinels
// (ErrReplyTooShort, ErrUnexpectedType) — wrapped.
//
// `go test -run none -fuzz=FuzzParseEchoReply ./internal/probe`
// runs a real fuzz campaign; CI's `go test ./...` runs the seed
// corpus only.
func FuzzParseEchoReply(f *testing.F) {
	// Seed: valid v4 reply, valid v6 reply, truncated, request-as-
	// reply, garbage type byte, all-zero, all-FF.
	f.Add(uint8(FamilyV4), []byte{0, 0, 0, 0, 0x12, 0x34, 0x56, 0x78})
	f.Add(uint8(FamilyV6), []byte{129, 0, 0, 0, 0xAB, 0xCD, 0x00, 0x01})
	f.Add(uint8(FamilyV4), []byte{0, 0})                   // too short
	f.Add(uint8(FamilyV4), []byte{8, 0, 0, 0, 0, 0, 0, 0}) // request as reply
	f.Add(uint8(FamilyV4), []byte{})                       // empty
	f.Add(uint8(FamilyV6), []byte(strings.Repeat("\xff", 64)))

	f.Fuzz(func(t *testing.T, familyByte uint8, data []byte) {
		// Limit the family input to the two values we accept; for
		// anything else, ParseEchoReply behaviour is undefined and
		// not part of the fuzzed contract.
		var family Family
		switch familyByte & 1 {
		case 0:
			family = FamilyV4
		default:
			family = FamilyV6
		}

		ident, seq, err := ParseEchoReply(family, data)
		if err == nil {
			// Accept path: ident/seq must equal the bytes at the
			// fixed offsets, otherwise the decoder is reading from
			// the wrong place.
			wantIdent := uint16(data[4])<<8 | uint16(data[5])
			wantSeq := uint16(data[6])<<8 | uint16(data[7])
			if ident != wantIdent || seq != wantSeq {
				t.Errorf("decode mismatch: (ident, seq) = (%#x, %#x); bytes say (%#x, %#x)",
					ident, seq, wantIdent, wantSeq)
			}
			return
		}
		// Reject path: must be one of the two sentinels wrapped.
		if !errors.Is(err, ErrReplyTooShort) && !errors.Is(err, ErrUnexpectedType) {
			t.Errorf("Parse rejected with un-classified error: %v", err)
		}
	})
}

// TestInternetChecksumOddLength: a single trailing byte must be
// treated as if padded with a zero (i.e. the high byte is the
// real one, the low byte is zero).
func TestInternetChecksumOddLength(t *testing.T) {
	t.Parallel()
	// Single byte 0xAB → interpreted as 0xAB00 → checksum = ~0xAB00 = 0x54FF.
	if got, want := internetChecksum([]byte{0xAB}), uint16(0x54FF); got != want {
		t.Errorf("odd-length checksum = %#x, want %#x", got, want)
	}
}

// TestEchoRoundTrip: construct an echo *request*, flip its type to the
// matching *reply*, and parse it. The identifier and sequence should
// round-trip cleanly.
func TestEchoRoundTrip(t *testing.T) {
	t.Parallel()
	const (
		ident uint16 = 0xCAFE
		seq   uint16 = 0x0042
	)
	for _, family := range []Family{FamilyV4, FamilyV6} {
		t.Run(family.String(), func(t *testing.T) {
			t.Parallel()
			req := EchoRequestBytes(family, ident, seq, []byte("rt"))
			// Flip request → reply: v4 (8→0), v6 (128→129).
			reply := make([]byte, len(req))
			copy(reply, req)
			switch family {
			case FamilyV4:
				reply[0] = icmpv4EchoReply
			case FamilyV6:
				reply[0] = icmpv6EchoReply
			}
			gotIdent, gotSeq, err := ParseEchoReply(family, reply)
			if err != nil {
				t.Fatalf("ParseEchoReply error: %v", err)
			}
			if gotIdent != ident || gotSeq != seq {
				t.Errorf("(ident, seq) = (%#x, %#x), want (%#x, %#x)", gotIdent, gotSeq, ident, seq)
			}
		})
	}
}
