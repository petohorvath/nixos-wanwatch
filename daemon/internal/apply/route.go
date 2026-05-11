package apply

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// DefaultRoute describes a default-route write: "send all traffic
// in `Table` (in the `Family` RIB) via `Gateway` out of the
// interface at `IfIndex`."
type DefaultRoute struct {
	Family  Family
	Table   int
	Gateway net.IP
	IfIndex int
}

// WriteDefault installs the default route described by `d`. Uses
// RouteReplace so a pre-existing default in the same table is
// overwritten atomically — the operation is therefore idempotent,
// matching PLAN §5.5: "rewrites on switch".
func WriteDefault(d DefaultRoute) error {
	if err := validateDefaultRoute(d); err != nil {
		return err
	}
	if err := netlink.RouteReplace(buildRoute(d)); err != nil {
		return fmt.Errorf("apply: route replace %s table=%d via %s dev=%d: %w",
			d.Family, d.Table, d.Gateway, d.IfIndex, err)
	}
	return nil
}

// buildRoute is the pure DefaultRoute → netlink.Route conversion.
// `Dst == nil` is netlink's convention for "default route".
func buildRoute(d DefaultRoute) *netlink.Route {
	return &netlink.Route{
		Family:    int(d.Family),
		Table:     d.Table,
		Dst:       nil,
		Gw:        d.Gateway,
		LinkIndex: d.IfIndex,
	}
}

func validateDefaultRoute(d DefaultRoute) error {
	if err := validateFamilyIP(d.Family, d.Gateway, "gateway"); err != nil {
		return err
	}
	if d.Table <= 0 {
		return fmt.Errorf("apply: invalid table %d (must be > 0)", d.Table)
	}
	if d.IfIndex <= 0 {
		return fmt.Errorf("apply: invalid ifindex %d", d.IfIndex)
	}
	return nil
}
