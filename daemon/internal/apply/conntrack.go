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
		toInetFamily(family),
		origFilter,
		replyFilter,
	)
	if err != nil {
		return n, fmt.Errorf("apply: conntrack flush family=%s ip=%s: %w", family, ip, err)
	}
	return n, nil
}

// buildSourceFilter constructs a ConntrackFilter that matches a
// single IP at the given tuple position. The "any of"-semantics of
// ConntrackDeleteFilters means each filter is OR-combined at the
// kernel level.
func buildSourceFilter(tp netlink.ConntrackFilterType, ip net.IP) (*netlink.ConntrackFilter, error) {
	f := &netlink.ConntrackFilter{}
	if err := f.AddIP(tp, ip); err != nil {
		return nil, fmt.Errorf("apply: conntrack filter add ip %s: %w", ip, err)
	}
	return f, nil
}

// toInetFamily maps the apply Family enum (AF_INET / AF_INET6) to
// netlink.InetFamily (uint8). Defined locally so the apply API
// stays free of vishvananda's type names.
func toInetFamily(f Family) netlink.InetFamily {
	return netlink.InetFamily(f)
}

func validateFlush(family Family, ip net.IP) error {
	if family != FamilyV4 && family != FamilyV6 {
		return fmt.Errorf("apply: invalid family %d", int(family))
	}
	if ip == nil {
		return fmt.Errorf("apply: ip is nil")
	}
	isV4 := ip.To4() != nil
	if family == FamilyV4 && !isV4 {
		return fmt.Errorf("apply: ip %s is not v4 but family=v4", ip)
	}
	if family == FamilyV6 && isV4 {
		return fmt.Errorf("apply: ip %s is v4 but family=v6", ip)
	}
	return nil
}
