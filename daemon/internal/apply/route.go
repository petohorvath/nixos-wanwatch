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

// buildRoute is the pure conversion from DefaultRoute to the
// netlink.Route struct the kernel consumes. Split out so the wire
// format can be table-tested without a netlink socket.
func buildRoute(d DefaultRoute) *netlink.Route {
	return &netlink.Route{
		Family:    int(d.Family),
		Table:     d.Table,
		Dst:       nil,
		Gw:        d.Gateway,
		LinkIndex: d.IfIndex,
	}
}

// validateDefaultRoute rejects inputs the kernel would silently
// misinterpret — a v6 gateway under family=v4 would be coerced into
// some adjacent kernel state rather than failing loudly. Catching
// at the boundary keeps the error message attributable.
func validateDefaultRoute(d DefaultRoute) error {
	if d.Family != FamilyV4 && d.Family != FamilyV6 {
		return fmt.Errorf("apply: invalid family %d (want AF_INET or AF_INET6)", int(d.Family))
	}
	if d.Table <= 0 {
		return fmt.Errorf("apply: invalid table %d (must be > 0)", d.Table)
	}
	if d.Gateway == nil {
		return fmt.Errorf("apply: gateway is nil")
	}
	if d.IfIndex <= 0 {
		return fmt.Errorf("apply: invalid ifindex %d", d.IfIndex)
	}
	isV4 := d.Gateway.To4() != nil
	if d.Family == FamilyV4 && !isV4 {
		return fmt.Errorf("apply: gateway %s is not v4 but family=v4", d.Gateway)
	}
	if d.Family == FamilyV6 && isV4 {
		return fmt.Errorf("apply: gateway %s is v4 but family=v6", d.Gateway)
	}
	return nil
}
