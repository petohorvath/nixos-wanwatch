package apply

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

// ctnetlink filter attributes, absent from vishvananda/netlink. Values
// from include/uapi/linux/netfilter/nfnetlink_conntrack.h.
const (
	ctaFilter              = 25     // CTA_FILTER
	ctaFilterReplyFlags    = 2      // CTA_FILTER_REPLY_FLAGS
	ctaFilterFlagCtaIPDest = 1 << 1 // CTA_FILTER_FLAG_CTA_IP_DST
)

// dumpAttempts bounds the dump-and-delete fallback. The kernel
// interrupts a dump when the table changes under it, and each retry
// dumps the whole table again.
const dumpAttempts = 2

// FlushResult reports how FlushBySource removed the entries.
type FlushResult struct {
	// Dumped is set when the table was dumped and each matching entry
	// deleted with its own request: always for IPv6, and for IPv4 on a
	// kernel without filtered deletes (before Linux 6.12).
	Dumped bool
	// Entries counts the entries deleted on the dump path. The
	// kernel's filtered delete reports no count.
	Entries uint
}

// LogValue names the path the flush took, with the entry count only
// where one exists. A dump stalls the caller for as long as it runs,
// so a log that shows one explains a slow switch.
func (r FlushResult) LogValue() slog.Value {
	if !r.Dumped {
		return slog.GroupValue(slog.String("method", "kernel"))
	}
	return slog.GroupValue(slog.String("method", "dump"), slog.Uint64("entries", uint64(r.Entries)))
}

// FlushBySource removes the conntrack entries whose reply direction is
// addressed to `ip`, the vacated WAN's address. Those are the flows
// that leave with `ip` as their source:
//
//   - locally-originated traffic (original src == reply dst == WAN IP)
//   - forwarded traffic masqueraded to the WAN (reply dst == WAN IP)
//
// Matching the reply destination covers both with one filter. Inbound
// connections to the WAN address (reply src == WAN IP) are left alone:
// a flush cannot move them to another WAN, and it would reset them
// while the vacated WAN still works.
//
// For IPv4 the kernel deletes the matching entries itself, in one
// request (Linux 6.12 and later). Earlier kernels reject that request,
// and the flush falls back to dumping the table and deleting each
// match, which takes seconds on a full table and stalls the caller
// meanwhile. IPv6 always takes the dump path: up to at least Linux
// 7.3, the kernel's IPv6 filter match is inverted (fixed upstream by
// cd37ab22a74e), so a filtered delete would remove every other IPv6
// entry instead. Without NAT66 an IPv6 flush matches only the router's
// own flows, so the dump deletes few entries.
//
// The flush is best-effort (docs/wan-monitoring.md) — the caller logs
// failures but does not fail the Decision. ctx is checked at entry and
// before each dump; a request in flight, or a dump's per-entry
// deletes, run to completion.
func FlushBySource(ctx context.Context, family probe.Family, ip net.IP) (FlushResult, error) {
	return flushBySourceVia(ctx, conntrackOps{
		filteredDelete: executeFilteredDelete,
		dumpDelete:     dumpDelete,
	}, family, ip)
}

// conntrackOps are the kernel requests behind FlushBySource, extracted
// so tests can substitute stubs.
type conntrackOps struct {
	// filteredDelete sends a request built by filteredDeleteRequest.
	filteredDelete func(*nl.NetlinkRequest) error
	// dumpDelete dumps the table and deletes the entries filter
	// matches, returning how many it deleted.
	dumpDelete func(netlink.InetFamily, *netlink.ConntrackFilter) (uint, error)
}

// flushBySourceVia is FlushBySource parameterized on the kernel
// requests so tests can exercise the fallback, retry, validation, and
// error-wrapping branches without netlink + root.
func flushBySourceVia(ctx context.Context, ops conntrackOps, family probe.Family, ip net.IP) (FlushResult, error) {
	if err := ctx.Err(); err != nil {
		return FlushResult{}, err
	}
	if err := validateFlush(family, ip); err != nil {
		return FlushResult{}, err
	}
	if family == probe.FamilyV6 {
		return dumpFlush(ctx, ops.dumpDelete, family, ip)
	}
	err := ops.filteredDelete(filteredDeleteRequest(ip))
	if err == nil {
		return FlushResult{}, nil
	}
	// Before 6.12 the kernel parses the filter's partial tuple as one
	// entry's and rejects it: EINVAL, or EOPNOTSUPP from the flush path.
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
		return FlushResult{}, fmt.Errorf("apply: conntrack flush family=%s ip=%s: %w", family, ip, err)
	}
	return dumpFlush(ctx, ops.dumpDelete, family, ip)
}

// dumpFlush is the fallback for kernels without filtered deletes. A
// dump the kernel interrupts still deletes the matches it returned, so
// a retry only has to catch the rest. The count includes every
// attempt's deletions, also on error.
func dumpFlush(
	ctx context.Context,
	del func(netlink.InetFamily, *netlink.ConntrackFilter) (uint, error),
	family probe.Family,
	ip net.IP,
) (FlushResult, error) {
	filter, err := buildReplyDstFilter(ip)
	if err != nil {
		return FlushResult{}, err
	}
	res := FlushResult{Dumped: true}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		//nolint:gosec // family is probe.FamilyV4 (=AF_INET=2) or FamilyV6 (=AF_INET6=10); both fit uint8
		n, err := del(netlink.InetFamily(family), filter)
		res.Entries += n
		if err == nil {
			return res, nil
		}
		if !errors.Is(err, netlink.ErrDumpInterrupted) {
			return res, fmt.Errorf("apply: conntrack flush family=%s ip=%s: %w", family, ip, err)
		}
		if attempt == dumpAttempts {
			return res, fmt.Errorf("apply: conntrack flush family=%s ip=%s: dump interrupted on all %d attempts: %w",
				family, ip, dumpAttempts, err)
		}
	}
}

// filteredDeleteRequest builds a ctnetlink delete carrying a
// CTA_FILTER that selects the reply destination: the kernel then
// deletes every IPv4 entry whose reply tuple is addressed to `ip`.
func filteredDeleteRequest(ip net.IP) *nl.NetlinkRequest {
	req := nl.NewNetlinkRequest(int(netlink.ConntrackTable)<<8|nl.IPCTNL_MSG_CT_DELETE, unix.NLM_F_ACK)
	req.AddData(&nl.Nfgenmsg{NfgenFamily: unix.AF_INET, Version: nl.NFNETLINK_V0})

	filter := nl.NewRtAttr(unix.NLA_F_NESTED|ctaFilter, nil)
	filter.AddRtAttr(ctaFilterReplyFlags, nl.Uint32Attr(ctaFilterFlagCtaIPDest))

	tuple := nl.NewRtAttr(unix.NLA_F_NESTED|nl.CTA_TUPLE_REPLY, nil)
	addrs := tuple.AddRtAttr(unix.NLA_F_NESTED|nl.CTA_TUPLE_IP, nil)
	addrs.AddRtAttr(nl.CTA_IP_V4_DST, ip.To4())

	req.AddData(filter)
	req.AddData(tuple)
	return req
}

func executeFilteredDelete(req *nl.NetlinkRequest) error {
	_, err := req.Execute(unix.NETLINK_NETFILTER, 0)
	return err
}

// dumpDelete runs the library's dump-and-delete on one socket per
// dump; the package-level call opens a socket per request.
func dumpDelete(family netlink.InetFamily, filter *netlink.ConntrackFilter) (uint, error) {
	h, err := netlink.NewHandle(unix.NETLINK_NETFILTER)
	if err != nil {
		return 0, err
	}
	defer h.Close()
	return h.ConntrackDeleteFilters(netlink.ConntrackTable, family, filter)
}

// buildReplyDstFilter constructs a ConntrackFilter matching the
// entries whose reply destination is `ip`.
func buildReplyDstFilter(ip net.IP) (*netlink.ConntrackFilter, error) {
	f := &netlink.ConntrackFilter{}
	if err := f.AddIP(netlink.ConntrackReplyDstIP, ip); err != nil {
		return nil, fmt.Errorf("apply: conntrack filter add ip %s: %w", ip, err)
	}
	return f, nil
}

func validateFlush(family probe.Family, ip net.IP) error {
	return validateFamilyIP(family, ip, "ip")
}
