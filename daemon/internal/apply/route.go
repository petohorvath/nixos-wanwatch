package apply

import (
	"fmt"
	"net"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// DefaultRoute describes a default-route write into `Table` (in the
// `Family` RIB) out of the interface at `IfIndex`.
//
// Two modes:
//
//   - Normal: Gateway is non-nil; the kernel ARPs/ND-resolves the
//     next-hop and forwards via it.
//   - PointToPoint: PointToPoint = true, Gateway = nil; the kernel
//     installs a `scope link` route, which works for PPP /
//     WireGuard / GRE / tun / any link without a broadcast next-hop.
//
// Exactly one of (Gateway != nil) and PointToPoint must hold; the
// validator rejects both-or-neither.
type DefaultRoute struct {
	Family       probe.Family
	Table        int
	Gateway      net.IP
	IfIndex      int
	PointToPoint bool
}

// WriteDefault installs the default route described by `d`. Uses
// RouteReplace so a pre-existing default in the same table is
// overwritten atomically — the operation is therefore idempotent.
func WriteDefault(d DefaultRoute) error {
	if err := validateDefaultRoute(d); err != nil {
		return err
	}
	if err := netlink.RouteReplace(buildRoute(d)); err != nil {
		return fmt.Errorf("apply: route replace %s table=%d dev=%d ptp=%v gw=%s: %w",
			d.Family, d.Table, d.IfIndex, d.PointToPoint, d.Gateway, err)
	}
	return nil
}

// buildRoute is the pure DefaultRoute → netlink.Route conversion.
//
// For a normal default route the kernel treats `Dst == nil` as
// "default for this family"; with `Gw` set, the library accepts
// it.
//
// For point-to-point links there is no `Gw` to set — `Dst == nil`
// + `Gw == nil` would leave the netlink message with no addresses
// at all, which `vishvananda/netlink` rejects ("either Dst.IP,
// Src.IP or Gw must be set"). Set `Dst` to the family's all-zero
// CIDR explicitly; the kernel still installs it as the default
// route for the family, scoped to the interface link.
func buildRoute(d DefaultRoute) *netlink.Route {
	r := &netlink.Route{
		Family:    int(d.Family),
		Table:     d.Table,
		LinkIndex: d.IfIndex,
	}
	if d.PointToPoint {
		r.Scope = unix.RT_SCOPE_LINK
		r.Dst = defaultDestination(d.Family)
	} else {
		r.Gw = d.Gateway
	}
	return r
}

// defaultDestination returns the all-zero CIDR for `f`. The kernel
// treats `0.0.0.0/0` / `::/0` as the default-route destination.
func defaultDestination(f probe.Family) *net.IPNet {
	if f == probe.FamilyV6 {
		return &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	}
	return &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
}

func validateDefaultRoute(d DefaultRoute) error {
	if d.Table <= 0 {
		return fmt.Errorf("apply: invalid table %d (must be > 0)", d.Table)
	}
	if d.IfIndex <= 0 {
		return fmt.Errorf("apply: invalid ifindex %d", d.IfIndex)
	}
	if d.PointToPoint {
		if d.Gateway != nil {
			return fmt.Errorf("apply: pointToPoint route must have nil Gateway; got %s", d.Gateway)
		}
		if !validFamily(d.Family) {
			return fmt.Errorf("apply: invalid family %d (want AF_INET or AF_INET6)", int(d.Family))
		}
		return nil
	}
	return validateFamilyIP(d.Family, d.Gateway, "gateway")
}
