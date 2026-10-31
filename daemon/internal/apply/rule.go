package apply

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// FwmarkRule describes "lookup `Table` when the packet carries
// `Mark` in its fwmark slot." Ensured once per (group, family)
// at daemon start — see docs/nftzones-integration.md step 2.
type FwmarkRule struct {
	Family probe.Family
	Mark   int
	Table  int
}

// EnsureRule makes `r` the only rule of its Family that routes all of
// r.Mark (mask 0xffffffff) to r.Table, and returns how many copies it
// removed. It lists the Family's rules and adds `r` only when none
// matches. A rule matches only when it has no selector besides the
// fwmark — see matchesRule — so a narrower rule, such as one that also
// matches an iif, is never kept in place of `r` and never deleted.
// Preference and protocol are ignored, so a full rule the user or
// systemd-networkd installed counts as a match. The kernel assigns the
// preference of a rule EnsureRule adds, so RuleAdd's EEXIST check never
// sees an existing copy: listing first is what keeps a daemon restart
// from adding another one. When more than one rule matches, EnsureRule
// keeps the lowest-preference one and deletes the rest. The copies are
// identical, so their removal never changes routing. ctx is checked at
// entry and again before the first mutation — see WriteDefault for the
// cancellation contract.
func EnsureRule(ctx context.Context, r FwmarkRule) (int, error) {
	return ensureRuleVia(ctx, ruleOps{
		list: netlink.RuleList,
		add:  netlink.RuleAdd,
		del:  netlink.RuleDel,
	}, r)
}

// ruleOps is the netlink seam behind EnsureRule, so tests can run
// every branch without a netlink socket.
type ruleOps struct {
	list func(family int) ([]netlink.Rule, error)
	add  func(*netlink.Rule) error
	del  func(*netlink.Rule) error
}

// fullMask is the fwmark mask of every rule EnsureRule installs.
const fullMask = uint32(0xffffffff)

func ensureRuleVia(ctx context.Context, ops ruleOps, r FwmarkRule) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validateRule(r); err != nil {
		return 0, err
	}
	wrap := func(op string, err error) error {
		return fmt.Errorf("apply: rule %s fwmark=%d table=%d family=%s: %w",
			op, r.Mark, r.Table, r.Family, err)
	}
	rules, err := ops.list(int(r.Family))
	if err != nil {
		return 0, wrap("list", err)
	}
	matches := slices.DeleteFunc(slices.Clone(rules), func(k netlink.Rule) bool {
		return !matchesRule(k, r)
	})
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(matches) == 0 {
		if err := ops.add(buildRule(r)); err != nil && !errors.Is(err, unix.EEXIST) {
			return 0, wrap("add", err)
		}
		return 0, nil
	}
	slices.SortStableFunc(matches, func(a, b netlink.Rule) int {
		return cmp.Compare(a.Priority, b.Priority)
	})
	removed := 0
	for i := range matches[1:] {
		if err := ops.del(&matches[i+1]); err != nil {
			return removed, wrap("delete", err)
		}
		removed++
	}
	return removed, nil
}

// matchesRule reports whether the listed rule k routes all of r.Mark
// to r.Table in r.Family: it matches the mark under the full mask, it
// looks up r.Table, and it has no other selector that narrows the
// traffic or the lookup — no source or destination prefix, iif or oif,
// `not`, TOS, tunnel ID, IP protocol, port range, or UID range, no
// goto, and no suppress_prefixlength or suppress_ifgroup. Its
// preference and protocol are ignored. k must come from
// netlink.RuleList, which marks an absent goto or suppressor as -1.
func matchesRule(k netlink.Rule, r FwmarkRule) bool {
	//nolint:gosec // r.Mark is validated positive by validateRule + bounded by the Nix allocator
	return k.Family == int(r.Family) && k.Mark == uint32(r.Mark) &&
		k.Mask != nil && *k.Mask == fullMask && k.Table == r.Table &&
		!hasOtherSelector(k)
}

// hasOtherSelector reports whether the listed rule k carries a selector
// or lookup modifier besides its fwmark and table.
func hasOtherSelector(k netlink.Rule) bool {
	return k.Src != nil || k.Dst != nil || k.IifName != "" || k.OifName != "" ||
		k.Invert || k.Tos != 0 || k.TunID != 0 || k.IPProto != 0 ||
		k.Sport != nil || k.Dport != nil || k.UIDRange != nil ||
		k.Goto != -1 || k.SuppressPrefixlen != -1 || k.SuppressIfgroup != -1
}

func buildRule(r FwmarkRule) *netlink.Rule {
	rule := netlink.NewRule()
	rule.Family = int(r.Family)
	//nolint:gosec // r.Mark is validated positive by validateRule + bounded by the Nix allocator
	rule.Mark = uint32(r.Mark)
	// Explicit full mask: the netlink library only encodes
	// FRA_FWMASK when rule.Mask != nil, and recent kernels
	// (≥ 6.18 observed) reject or hide a rule that carries
	// FRA_FWMARK with no FRA_FWMASK — userspace `ip rule add
	// fwmark X table Y` defaults to mask 0xffffffff for the
	// same reason.
	mask := fullMask
	rule.Mask = &mask
	rule.Table = r.Table
	// FRA_PROTOCOL = RTPROT_STATIC stamps the rule as "managed by
	// some daemon, not networkd's job to clean up". Without it,
	// systemd-networkd with `ManageForeignRoutingPolicyRules=yes`
	// (its default) considers our rule foreign and deletes it
	// during its startup-time reconciliation pass.
	rule.Protocol = unix.RTPROT_STATIC
	return rule
}

func validateRule(r FwmarkRule) error {
	if !validFamily(r.Family) {
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
