package apply

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

func TestBuildReplyDstFilterAccepts(t *testing.T) {
	t.Parallel()
	f, err := buildReplyDstFilter(net.ParseIP("192.0.2.1"))
	if err != nil {
		t.Fatalf("buildReplyDstFilter = %v, want nil", err)
	}
	if f == nil {
		t.Fatal("buildReplyDstFilter returned nil filter without error")
	}
}

func TestBuildReplyDstFilterRejectsNilIP(t *testing.T) {
	t.Parallel()
	if _, err := buildReplyDstFilter(nil); err == nil {
		t.Error("buildReplyDstFilter(nil) = nil error, want error")
	}
}

func TestValidateFlushAcceptsHappyPath(t *testing.T) {
	t.Parallel()
	if err := validateFlush(probe.FamilyV4, net.ParseIP("192.0.2.1")); err != nil {
		t.Errorf("validateFlush(v4) = %v, want nil", err)
	}
	if err := validateFlush(probe.FamilyV6, net.ParseIP("2001:db8::1")); err != nil {
		t.Errorf("validateFlush(v6) = %v, want nil", err)
	}
}

func TestValidateFlushRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		family  probe.Family
		ip      net.IP
		wantSub string
	}{
		{"bad family", probe.Family(99), net.ParseIP("192.0.2.1"), "invalid family"},
		{"nil ip", probe.FamilyV4, nil, "ip is nil"},
		{"v6 ip family=v4", probe.FamilyV4, net.ParseIP("2001:db8::1"), "is not v4"},
		{"v4 ip family=v6", probe.FamilyV6, net.ParseIP("192.0.2.1"), "is v4 but family=v6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateFlush(tc.family, tc.ip)
			if err == nil {
				t.Fatalf("validateFlush = nil, want %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// dumpOutcome is one dump-and-delete attempt's result.
type dumpOutcome struct {
	n   uint
	err error
}

// fakeOps records the kernel requests a flush sends. The filtered
// delete returns filteredErr; dump attempts return dumps in order.
type fakeOps struct {
	filteredErr  error
	dumps        []dumpOutcome
	onFiltered   func()
	onDump       func()
	filteredReqs int
	filters      []*netlink.ConntrackFilter
	families     []netlink.InetFamily
}

func (f *fakeOps) ops(tb testing.TB) conntrackOps {
	tb.Helper()
	return conntrackOps{
		filteredDelete: func(*nl.NetlinkRequest) error {
			f.filteredReqs++
			if f.onFiltered != nil {
				f.onFiltered()
			}
			return f.filteredErr
		},
		dumpDelete: func(family netlink.InetFamily, filter *netlink.ConntrackFilter) (uint, error) {
			attempt := len(f.filters)
			if attempt >= len(f.dumps) {
				tb.Fatalf("unexpected dump attempt %d", attempt+1)
			}
			f.filters = append(f.filters, filter)
			f.families = append(f.families, family)
			if f.onDump != nil {
				f.onDump()
			}
			return f.dumps[attempt].n, f.dumps[attempt].err
		},
	}
}

func TestFlushBySourceVia(t *testing.T) {
	t.Parallel()
	interrupted := netlink.ErrDumpInterrupted
	cases := []struct {
		name        string
		filteredErr error
		dumps       []dumpOutcome
		want        FlushResult
		wantErr     error
		wantErrSub  string
	}{
		{name: "kernel deletes by filter"},
		{
			name:        "EINVAL falls back to a dump",
			filteredErr: unix.EINVAL,
			dumps:       []dumpOutcome{{n: 5}},
			want:        FlushResult{Dumped: true, Entries: 5},
		},
		{
			name:        "EOPNOTSUPP falls back to a dump",
			filteredErr: unix.EOPNOTSUPP,
			dumps:       []dumpOutcome{{n: 5}},
			want:        FlushResult{Dumped: true, Entries: 5},
		},
		{
			name:        "annotated EINVAL falls back to a dump",
			filteredErr: fmt.Errorf("%w: missing tuple attribute", unix.EINVAL),
			dumps:       []dumpOutcome{{n: 5}},
			want:        FlushResult{Dumped: true, Entries: 5},
		},
		{
			name:        "other kernel error fails without a dump",
			filteredErr: unix.EPERM,
			wantErr:     unix.EPERM,
		},
		{
			name:        "interrupted dump is retried",
			filteredErr: unix.EINVAL,
			dumps:       []dumpOutcome{{n: 3, err: interrupted}, {n: 2}},
			want:        FlushResult{Dumped: true, Entries: 5},
		},
		{
			name:        "second interrupted dump fails",
			filteredErr: unix.EINVAL,
			dumps:       []dumpOutcome{{n: 3, err: interrupted}, {n: 2, err: interrupted}},
			want:        FlushResult{Dumped: true, Entries: 5},
			wantErr:     interrupted,
			wantErrSub:  "dump interrupted on all 2 attempts",
		},
		{
			name:        "other dump error is not retried",
			filteredErr: unix.EINVAL,
			dumps:       []dumpOutcome{{n: 3, err: unix.ENOMEM}},
			want:        FlushResult{Dumped: true, Entries: 3},
			wantErr:     unix.ENOMEM,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeOps{filteredErr: tc.filteredErr, dumps: tc.dumps}
			got, err := flushBySourceVia(t.Context(), f.ops(t), probe.FamilyV4, net.ParseIP("192.0.2.1"))
			if tc.wantErr == nil && err != nil {
				t.Fatalf("flushBySourceVia = %v, want nil", err)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("flushBySourceVia = %v, want %v", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), "apply: conntrack flush family=v4 ip=192.0.2.1: "+tc.wantErrSub) {
					t.Errorf("err = %q, want apply context prefix and %q", err, tc.wantErrSub)
				}
			}
			if got != tc.want {
				t.Errorf("result = %+v, want %+v", got, tc.want)
			}
			if f.filteredReqs != 1 {
				t.Errorf("filtered deletes = %d, want 1", f.filteredReqs)
			}
			if len(f.filters) != len(tc.dumps) {
				t.Errorf("dump attempts = %d, want %d", len(f.filters), len(tc.dumps))
			}
		})
	}
}

// TestFlushBySourceViaRetriesCombinedDumpError: the library's
// dump-and-delete joins an interrupted dump with its per-entry delete
// failures into one error. The interruption still marks the attempt
// for a retry, which dumps the table again and so retries the failed
// deletes too. Per-entry failures without an interruption are final.
func TestFlushBySourceViaRetriesCombinedDumpError(t *testing.T) {
	t.Parallel()
	// The library's message for failed per-entry deletes.
	deleteFailed := errors.New("failed to delete 1 conntrack flows with 1 filters")
	combined := errors.Join(netlink.ErrDumpInterrupted, deleteFailed)
	cases := []struct {
		name       string
		dumps      []dumpOutcome
		want       FlushResult
		wantErrs   []error
		wantErrSub string
	}{
		{
			name:  "combined error then a clean dump",
			dumps: []dumpOutcome{{n: 3, err: combined}, {n: 1}},
			want:  FlushResult{Dumped: true, Entries: 4},
		},
		{
			name:       "combined error on every attempt",
			dumps:      []dumpOutcome{{n: 3, err: combined}, {n: 1, err: combined}},
			want:       FlushResult{Dumped: true, Entries: 4},
			wantErrs:   []error{netlink.ErrDumpInterrupted, deleteFailed},
			wantErrSub: "dump interrupted on all 2 attempts",
		},
		{
			name:     "delete failures alone are not retried",
			dumps:    []dumpOutcome{{n: 3, err: deleteFailed}},
			want:     FlushResult{Dumped: true, Entries: 3},
			wantErrs: []error{deleteFailed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeOps{filteredErr: unix.EINVAL, dumps: tc.dumps}
			got, err := flushBySourceVia(t.Context(), f.ops(t), probe.FamilyV4, net.ParseIP("192.0.2.1"))
			if len(tc.wantErrs) == 0 && err != nil {
				t.Fatalf("flushBySourceVia = %v, want nil", err)
			}
			for _, want := range tc.wantErrs {
				if !errors.Is(err, want) {
					t.Errorf("flushBySourceVia = %v, want %v in its chain", err, want)
				}
			}
			if len(tc.wantErrs) > 0 && !strings.Contains(err.Error(), "apply: conntrack flush family=v4 ip=192.0.2.1: "+tc.wantErrSub) {
				t.Errorf("err = %q, want apply context prefix and %q", err, tc.wantErrSub)
			}
			if got != tc.want {
				t.Errorf("result = %+v, want %+v", got, tc.want)
			}
			if len(f.filters) != len(tc.dumps) {
				t.Errorf("dump attempts = %d, want %d", len(f.filters), len(tc.dumps))
			}
		})
	}
}

func TestFlushBySourceViaContextCancelled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// cancelAt is where the stubs cancel: "entry" cancels before
		// the flush, "filtered" during the filtered delete, "dump"
		// during the first, interrupted dump.
		cancelAt         string
		wantFilteredReqs int
		wantDumps        int
		want             FlushResult
	}{
		{name: "at entry", cancelAt: "entry"},
		{
			name:             "before the fallback dump",
			cancelAt:         "filtered",
			wantFilteredReqs: 1,
			want:             FlushResult{Dumped: true},
		},
		{
			name:             "before a dump retry",
			cancelAt:         "dump",
			wantFilteredReqs: 1,
			wantDumps:        1,
			want:             FlushResult{Dumped: true, Entries: 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			f := &fakeOps{
				filteredErr: unix.EINVAL,
				dumps:       []dumpOutcome{{n: 3, err: netlink.ErrDumpInterrupted}},
			}
			switch tc.cancelAt {
			case "entry":
				cancel()
			case "filtered":
				f.onFiltered = cancel
			case "dump":
				f.onDump = cancel
			}
			defer cancel()

			got, err := flushBySourceVia(ctx, f.ops(t), probe.FamilyV4, net.ParseIP("192.0.2.1"))
			if !errors.Is(err, context.Canceled) {
				t.Errorf("flushBySourceVia = %v, want context.Canceled", err)
			}
			if got != tc.want {
				t.Errorf("result = %+v, want %+v", got, tc.want)
			}
			if f.filteredReqs != tc.wantFilteredReqs {
				t.Errorf("filtered deletes = %d, want %d", f.filteredReqs, tc.wantFilteredReqs)
			}
			if len(f.filters) != tc.wantDumps {
				t.Errorf("dump attempts = %d, want %d", len(f.filters), tc.wantDumps)
			}
		})
	}
}

func TestFlushBySourceViaSkipsKernelOnValidationFailure(t *testing.T) {
	t.Parallel()
	f := &fakeOps{}
	_, err := flushBySourceVia(t.Context(), f.ops(t), probe.FamilyV4, nil)
	if err == nil {
		t.Error("flushBySourceVia(nil ip) = nil error, want non-nil")
	}
	if f.filteredReqs != 0 || len(f.filters) != 0 {
		t.Errorf("kernel requests = %d filtered, %d dumps; want none", f.filteredReqs, len(f.filters))
	}
}

// holdsCapNetAdmin reports whether the test process has CAP_NET_ADMIN
// in its effective set, as root or through file or ambient capabilities.
func holdsCapNetAdmin(tb testing.TB) bool {
	tb.Helper()
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		tb.Fatalf("capget: %v", err)
	}
	return data[unix.CAP_NET_ADMIN/32].Effective&(1<<(unix.CAP_NET_ADMIN%32)) != 0
}

// TestFlushBySourceReportsKernelRefusal sends both kernel paths to the
// real kernel. Without CAP_NET_ADMIN, nfnetlink refuses the IPv4
// filtered delete and the IPv6 dump, and the refusal must reach the
// caller as a flush error. With it the requests would really flush, so
// the test skips.
func TestFlushBySourceReportsKernelRefusal(t *testing.T) {
	t.Parallel()
	if holdsCapNetAdmin(t) {
		t.Skip("CAP_NET_ADMIN held: the flush would mutate the conntrack table")
	}
	cases := []struct {
		family probe.Family
		ip     string
	}{
		{probe.FamilyV4, "192.0.2.1"},
		{probe.FamilyV6, "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.family.String(), func(t *testing.T) {
			t.Parallel()
			_, err := FlushBySource(t.Context(), tc.family, net.ParseIP(tc.ip))
			if err == nil {
				t.Fatal("FlushBySource without CAP_NET_ADMIN = nil, want the kernel's refusal")
			}
			if !strings.Contains(err.Error(), "apply: conntrack flush family="+tc.family.String()) {
				t.Errorf("err = %q, want apply context prefix", err)
			}
		})
	}
}

func TestFlushBySourcePropagatesValidationError(t *testing.T) {
	t.Parallel()
	// nil ip → fail fast before any netlink call. This branch is
	// the only one we can drive without root + a live conntrack
	// table; the netlink-bound path is VM-tier per AGENTS.md.
	res, err := FlushBySource(t.Context(), probe.FamilyV4, nil)
	if err == nil {
		t.Fatal("FlushBySource(nil ip) = nil err, want validation error")
	}
	if res != (FlushResult{}) {
		t.Errorf("result = %+v, want zero on validation error", res)
	}
}

// TestFlushBySourceViaDumpMatchesFlowsLeavingFromIP runs the filter the
// fallback dump hands to the library against representative flows of
// the vacated WAN's address and of another WAN's.
func TestFlushBySourceViaDumpMatchesFlowsLeavingFromIP(t *testing.T) {
	t.Parallel()
	tcp := func(src, dst string) netlink.IPTuple {
		return netlink.IPTuple{Protocol: unix.IPPROTO_TCP, SrcIP: net.ParseIP(src), DstIP: net.ParseIP(dst)}
	}
	cases := []struct {
		name    string
		family  probe.Family
		wan     string
		forward netlink.IPTuple
		reverse netlink.IPTuple
		want    bool
	}{
		{
			"v4 masqueraded LAN flow", probe.FamilyV4, "198.51.100.1",
			tcp("10.0.0.5", "203.0.113.9"), tcp("203.0.113.9", "198.51.100.1"), true,
		},
		{
			"v4 locally-originated flow", probe.FamilyV4, "198.51.100.1",
			tcp("198.51.100.1", "203.0.113.9"), tcp("203.0.113.9", "198.51.100.1"), true,
		},
		{
			"v4 inbound connection to the WAN", probe.FamilyV4, "198.51.100.1",
			tcp("203.0.113.9", "198.51.100.1"), tcp("198.51.100.1", "203.0.113.9"), false,
		},
		{
			"v4 flow masqueraded to another WAN", probe.FamilyV4, "198.51.100.1",
			tcp("10.0.0.5", "203.0.113.9"), tcp("203.0.113.9", "192.0.2.1"), false,
		},
		{
			"v6 locally-originated flow", probe.FamilyV6, "2001:db8::1",
			tcp("2001:db8::1", "2001:db8:ffff::9"), tcp("2001:db8:ffff::9", "2001:db8::1"), true,
		},
		{
			"v6 inbound connection to the WAN", probe.FamilyV6, "2001:db8::1",
			tcp("2001:db8:ffff::9", "2001:db8::1"), tcp("2001:db8::1", "2001:db8:ffff::9"), false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeOps{filteredErr: unix.EINVAL, dumps: []dumpOutcome{{}}}
			if _, err := flushBySourceVia(t.Context(), f.ops(t), tc.family, net.ParseIP(tc.wan)); err != nil {
				t.Fatalf("flushBySourceVia = %v, want nil", err)
			}
			if len(f.filters) != 1 {
				t.Fatalf("dump attempts = %d, want 1", len(f.filters))
			}
			if want := netlink.InetFamily(tc.family); f.families[0] != want {
				t.Errorf("dump family = %v, want %v", f.families[0], want)
			}
			flow := &netlink.ConntrackFlow{Forward: tc.forward, Reverse: tc.reverse}
			if got := f.filters[0].MatchConntrackFlow(flow); got != tc.want {
				t.Errorf("filter matches flow = %v, want %v", got, tc.want)
			}
		})
	}
}

// parseAttrs splits b into netlink attributes keyed by their full type,
// flags included.
func parseAttrs(tb testing.TB, b []byte) map[uint16][]byte {
	tb.Helper()
	attrs, err := nl.ParseRouteAttr(b)
	if err != nil {
		tb.Fatalf("parse attributes: %v", err)
	}
	out := make(map[uint16][]byte, len(attrs))
	for _, a := range attrs {
		out[a.Attr.Type] = a.Value
	}
	if len(out) != len(attrs) {
		tb.Fatalf("attributes repeat a type: %v", attrs)
	}
	return out
}

// TestFilteredDeleteRequest decodes the request the kernel receives:
// an IPv4 conntrack delete with a CTA_FILTER selecting the reply
// destination and a reply tuple carrying only that address.
func TestFilteredDeleteRequest(t *testing.T) {
	t.Parallel()
	const nested = unix.NLA_F_NESTED
	b := filteredDeleteRequest(net.ParseIP("198.51.100.1")).Serialize()

	// struct nlmsghdr: u32 len, u16 type, u16 flags, …
	msgType, flags := nl.NativeEndian().Uint16(b[4:6]), nl.NativeEndian().Uint16(b[6:8])
	if want := uint16(int(netlink.ConntrackTable)<<8 | nl.IPCTNL_MSG_CT_DELETE); msgType != want {
		t.Errorf("message type = %#x, want %#x", msgType, want)
	}
	if want := uint16(unix.NLM_F_REQUEST | unix.NLM_F_ACK); flags != want {
		t.Errorf("flags = %#x, want %#x (no dump, no create)", flags, want)
	}
	nfgen := b[unix.SizeofNlMsghdr:]
	if nfgen[0] != unix.AF_INET || nfgen[1] != nl.NFNETLINK_V0 {
		t.Errorf("nfgenmsg family=%d version=%d, want %d and %d", nfgen[0], nfgen[1], unix.AF_INET, nl.NFNETLINK_V0)
	}

	top := parseAttrs(t, nfgen[4:])
	if len(top) != 2 {
		t.Fatalf("top-level attributes = %d, want CTA_FILTER and CTA_TUPLE_REPLY", len(top))
	}
	filter := parseAttrs(t, top[nested|ctaFilter])
	if len(filter) != 1 {
		t.Fatalf("CTA_FILTER attributes = %d, want only CTA_FILTER_REPLY_FLAGS", len(filter))
	}
	if got := nl.NativeEndian().Uint32(filter[ctaFilterReplyFlags]); got != ctaFilterFlagCtaIPDest {
		t.Errorf("reply flags = %#x, want CTA_FILTER_FLAG_CTA_IP_DST", got)
	}
	tuple := parseAttrs(t, top[nested|nl.CTA_TUPLE_REPLY])
	addrs := parseAttrs(t, tuple[nested|nl.CTA_TUPLE_IP])
	if len(tuple) != 1 || len(addrs) != 1 {
		t.Fatalf("reply tuple carries %d attributes and %d addresses, want only the destination", len(tuple), len(addrs))
	}
	if got, want := addrs[nl.CTA_IP_V4_DST], []byte{198, 51, 100, 1}; !bytes.Equal(got, want) {
		t.Errorf("reply destination = %v, want %v", got, want)
	}
}

// TestFlushBySourceViaDumpsIPv6 pins IPv6 to the dump path: the
// kernel's IPv6 filter match is inverted, so a filtered delete would
// remove every other IPv6 entry.
func TestFlushBySourceViaDumpsIPv6(t *testing.T) {
	t.Parallel()
	f := &fakeOps{dumps: []dumpOutcome{{n: 4}}}
	got, err := flushBySourceVia(t.Context(), f.ops(t), probe.FamilyV6, net.ParseIP("2001:db8::1"))
	if err != nil {
		t.Fatalf("flushBySourceVia(v6) = %v, want nil", err)
	}
	if want := (FlushResult{Dumped: true, Entries: 4}); got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
	if f.filteredReqs != 0 {
		t.Errorf("filtered deletes = %d, want 0 for IPv6", f.filteredReqs)
	}
}

func TestFlushResultLogValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  FlushResult
		want string
	}{
		{"kernel delete", FlushResult{}, "flush.method=kernel"},
		{"dump", FlushResult{Dumped: true, Entries: 7}, "flush.method=dump flush.entries=7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			dropBuiltins := func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.MessageKey {
					return slog.Attr{}
				}
				return a
			}
			slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: dropBuiltins})).Info("", "flush", tc.res)
			if got := strings.TrimSpace(buf.String()); got != tc.want {
				t.Errorf("logged %q, want %q", got, tc.want)
			}
		})
	}
}
