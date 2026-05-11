package apply

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// FlushBySource removes conntrack entries whose original-source or
// reply-source IP matches `ip` (the vacated WAN's address). Covers
// both:
//
//   - locally-originated traffic (original src == WAN IP)
//   - forwarded + SNATted traffic (reply src == WAN IP)
//
// PLAN §5.5 marks conntrack flush as best-effort — the caller
// (orchestrator) logs failures but does not fail the apply step.
// Returns the number of entries deleted.
func FlushBySource(family Family, ip net.IP) (uint, error) {
	if err := validateFlush(family, ip); err != nil {
		return 0, err
	}
	origFilter, err := buildSourceFilter(netlink.ConntrackOrigSrcIP, ip)
	if err != nil {
		return 0, err
	}
	replyFilter, err := buildSourceFilter(netlink.ConntrackReplySrcIP, ip)
	if err != nil {
		return 0, err
	}
	n, err := netlink.ConntrackDeleteFilters(
		netlink.ConntrackTable,
		netlink.InetFamily(family),
		origFilter,
		replyFilter,
	)
	if err != nil {
		return n, fmt.Errorf("apply: conntrack flush family=%s ip=%s: %w", family, ip, err)
	}
	return n, nil
}

// buildSourceFilter constructs a ConntrackFilter matching a single
// IP at the given tuple position. ConntrackDeleteFilters takes a
// variadic and OR-combines them in userspace iteration (the kernel
// has no filter attribute), so passing orig + reply as separate
// filters is the correct shape — a single filter with both fields
// set would AND them.
func buildSourceFilter(tp netlink.ConntrackFilterType, ip net.IP) (*netlink.ConntrackFilter, error) {
	f := &netlink.ConntrackFilter{}
	if err := f.AddIP(tp, ip); err != nil {
		return nil, fmt.Errorf("apply: conntrack filter add ip %s: %w", ip, err)
	}
	return f, nil
}

func validateFlush(family Family, ip net.IP) error {
	return validateFamilyIP(family, ip, "ip")
}
