// Package apply mutates kernel routing state via vishvananda/netlink
// — no shellouts to `ip`. Three orthogonal concerns:
//
//   - route.go     — RTM_NEWROUTE per (family, table) every Decision
//   - rule.go      — RTM_NEWRULE per (group, family) once at startup
//   - conntrack.go — flush entries on the just-vacated interface
//     after a switch (best-effort, family-agnostic)
//
// Every operation is family-parameterized; the orchestrator iterates
// over both families per Decision. The Family type lives in
// `internal/probe` (its values match AF_INET / AF_INET6 so they pass
// through to netlink unchanged).
package apply

import (
	"fmt"
	"net"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// validateFamilyIP rejects family/IP mismatches — a v6 address
// under family=v4 (or vice versa) would be coerced into adjacent
// kernel state rather than failing loudly. `label` names the field
// in the error message (e.g. "gateway", "ip") so the caller's
// context survives.
func validateFamilyIP(family probe.Family, ip net.IP, label string) error {
	if !validFamily(family) {
		return fmt.Errorf("apply: invalid family %d (want AF_INET or AF_INET6)", int(family))
	}
	if ip == nil {
		return fmt.Errorf("apply: %s is nil", label)
	}
	isV4 := ip.To4() != nil
	if family == probe.FamilyV4 && !isV4 {
		return fmt.Errorf("apply: %s %s is not v4 but family=v4", label, ip)
	}
	if family == probe.FamilyV6 && isV4 {
		return fmt.Errorf("apply: %s %s is v4 but family=v6", label, ip)
	}
	return nil
}

// validFamily reports whether `family` is one of the two supported
// IP families. Centralized so route.go / rule.go don't repeat the
// same two-arm check.
func validFamily(family probe.Family) bool {
	return family == probe.FamilyV4 || family == probe.FamilyV6
}
