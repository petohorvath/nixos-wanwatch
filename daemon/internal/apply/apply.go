// Package apply mutates kernel routing state via vishvananda/netlink
// — no shellouts to `ip`. Three orthogonal concerns:
//
//   - route.go     — RTM_NEWROUTE per (family, table) every Decision
//   - rule.go      — RTM_NEWRULE per (group, family) once at startup
//   - conntrack.go — flush entries on the just-vacated interface
//     after a switch (best-effort, family-agnostic)
//
// Every operation is family-parameterized; the orchestrator iterates
// over both families per Decision.
package apply

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// Family is the IP family an operation acts on, aliased to
// AF_INET/AF_INET6 so values can be passed to netlink without
// conversion.
type Family int

const (
	FamilyV4 Family = unix.AF_INET
	FamilyV6 Family = unix.AF_INET6
)

// String returns "v4" / "v6" — matches the textual names used in
// the daemon-config, state.json, and metrics labels.
func (f Family) String() string {
	switch f {
	case FamilyV4:
		return "v4"
	case FamilyV6:
		return "v6"
	default:
		return fmt.Sprintf("Family(%d)", int(f))
	}
}

// FamilyFromString maps "v4" / "v6" → Family. Returns (0, false)
// on an unknown name so the orchestrator can decide whether to
// surface the error or skip the family.
func FamilyFromString(s string) (Family, bool) {
	switch s {
	case "v4":
		return FamilyV4, true
	case "v6":
		return FamilyV6, true
	}
	return 0, false
}

// validateFamilyIP rejects family/IP mismatches — a v6 address
// under family=v4 (or vice versa) would be coerced into adjacent
// kernel state rather than failing loudly. `label` names the field
// in the error message (e.g. "gateway", "ip") so the caller's
// context survives.
func validateFamilyIP(family Family, ip net.IP, label string) error {
	if family != FamilyV4 && family != FamilyV6 {
		return fmt.Errorf("apply: invalid family %d (want AF_INET or AF_INET6)", int(family))
	}
	if ip == nil {
		return fmt.Errorf("apply: %s is nil", label)
	}
	isV4 := ip.To4() != nil
	if family == FamilyV4 && !isV4 {
		return fmt.Errorf("apply: %s %s is not v4 but family=v4", label, ip)
	}
	if family == FamilyV6 && isV4 {
		return fmt.Errorf("apply: %s %s is v4 but family=v6", label, ip)
	}
	return nil
}
