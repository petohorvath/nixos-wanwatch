package apply

import (
	"fmt"
	"net"

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
	Family       Family
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
// `Dst == nil` is netlink's convention for "default route".
// For point-to-point links, `Scope = link` and no Gw is set — the
// kernel forwards out the interface without resolving a next-hop.
func buildRoute(d DefaultRoute) *netlink.Route {
	r := &netlink.Route{
		Family:    int(d.Family),
		Table:     d.Table,
		Dst:       nil,
		LinkIndex: d.IfIndex,
	}
	if d.PointToPoint {
		r.Scope = unix.RT_SCOPE_LINK
	} else {
		r.Gw = d.Gateway
	}
	return r
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
		if d.Family != FamilyV4 && d.Family != FamilyV6 {
			return fmt.Errorf("apply: invalid family %d (want AF_INET or AF_INET6)", int(d.Family))
		}
		return nil
	}
	return validateFamilyIP(d.Family, d.Gateway, "gateway")
}
