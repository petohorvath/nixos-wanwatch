package apply

import (
	"errors"
	"fmt"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// FwmarkRule describes "lookup `Table` when the packet carries
// `Mark` in its fwmark slot." Installed once per (group, family)
// at daemon start — see PLAN §6.1 step 2.
type FwmarkRule struct {
	Family Family
	Mark   int
	Table  int
}

// EnsureRule installs `r` if it isn't already present. RuleAdd
// returns EEXIST on a duplicate; swallow it so the operation is
// idempotent regardless of whether a previous daemon run left the
// rule behind.
func EnsureRule(r FwmarkRule) error {
	return ensureRuleVia(netlink.RuleAdd, r)
}

// ensureRuleVia is EnsureRule parameterized on the rule-adder so
// tests can drive the EEXIST-swallow branch without a netlink
// socket.
func ensureRuleVia(ruleAdd func(*netlink.Rule) error, r FwmarkRule) error {
	if err := validateRule(r); err != nil {
		return err
	}
	if err := ruleAdd(buildRule(r)); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil
		}
		return fmt.Errorf("apply: rule add fwmark=%d table=%d family=%s: %w",
			r.Mark, r.Table, r.Family, err)
	}
	return nil
}

func buildRule(r FwmarkRule) *netlink.Rule {
	rule := netlink.NewRule()
	rule.Family = int(r.Family)
	rule.Mark = uint32(r.Mark)
	// Explicit full mask: the netlink library only encodes
	// FRA_FWMASK when rule.Mask != nil, and recent kernels
	// (≥ 6.18 observed) reject or hide a rule that carries
	// FRA_FWMARK with no FRA_FWMASK — userspace `ip rule add
	// fwmark X table Y` defaults to mask 0xffffffff for the
	// same reason.
	mask := uint32(0xffffffff)
	rule.Mask = &mask
	rule.Table = r.Table
	return rule
}

func validateRule(r FwmarkRule) error {
	if r.Family != FamilyV4 && r.Family != FamilyV6 {
		return fmt.Errorf("apply: invalid family %d", int(r.Family))
	}
	if r.Mark <= 0 {
		return fmt.Errorf("apply: invalid mark %d (must be > 0)", r.Mark)
	}
	if r.Table <= 0 {
		return fmt.Errorf("apply: invalid table %d (must be > 0)", r.Table)
	}
	return nil
}
