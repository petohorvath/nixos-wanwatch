package apply

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func validFwmarkRule() FwmarkRule {
	return FwmarkRule{
		Family: probe.FamilyV4,
		Mark:   0x100,
		Table:  100,
	}
}

func TestBuildRulePopulatesNetlinkStruct(t *testing.T) {
	t.Parallel()
	r := validFwmarkRule()
	got := buildRule(r)
	if got.Family != int(r.Family) {
		t.Errorf("Family = %d, want %d", got.Family, r.Family)
	}
	if got.Mark != uint32(r.Mark) {
		t.Errorf("Mark = %d, want %d", got.Mark, r.Mark)
	}
	if got.Table != r.Table {
		t.Errorf("Table = %d, want %d", got.Table, r.Table)
	}
}

func TestValidateRuleAcceptsHappyPath(t *testing.T) {
	t.Parallel()
	if err := validateRule(validFwmarkRule()); err != nil {
		t.Errorf("validateRule(happy) = %v, want nil", err)
	}
	v6 := validFwmarkRule()
	v6.Family = probe.FamilyV6
	if err := validateRule(v6); err != nil {
		t.Errorf("validateRule(v6) = %v, want nil", err)
	}
}

// fakeRuleOps stands in for the kernel's rule list: list returns
// rules, add and del record their argument and return the scripted
// error. onList runs inside list, so a test can cancel ctx between
// the list and the first mutation.
type fakeRuleOps struct {
	rules   []netlink.Rule
	listErr error
	addErr  error
	delErr  error
	onList  func()

	listed  []int // the families list was called with
	added   []netlink.Rule
	deleted []netlink.Rule
}

func (f *fakeRuleOps) ops() ruleOps {
	return ruleOps{
		list: func(family int) ([]netlink.Rule, error) {
			f.listed = append(f.listed, family)
			if f.onList != nil {
				f.onList()
			}
			return f.rules, f.listErr
		},
		add: func(r *netlink.Rule) error {
			f.added = append(f.added, *r)
			return f.addErr
		},
		del: func(r *netlink.Rule) error {
			f.deleted = append(f.deleted, *r)
			return f.delErr
		},
	}
}

func (f *fakeRuleOps) calls() int {
	return len(f.listed) + len(f.added) + len(f.deleted)
}

// listedRule is validFwmarkRule as netlink.RuleList returns it after
// `ip rule add fwmark 0x100 table 100` gave it preference pref. It
// starts from netlink.NewRule, as RuleList does, so the attributes the
// kernel did not send keep RuleList's "unset" values.
func listedRule(pref int) netlink.Rule {
	mask := uint32(0xffffffff)
	r := *netlink.NewRule()
	r.Priority = pref
	r.Family = unix.AF_INET
	r.Mark = 0x100
	r.Mask = &mask
	r.Table = 100
	r.Protocol = unix.RTPROT_STATIC
	return r
}

func withRule(pref int, mutate func(*netlink.Rule)) netlink.Rule {
	r := listedRule(pref)
	mutate(&r)
	return r
}

func preferences(rules []netlink.Rule) []int {
	prefs := make([]int, len(rules))
	for i, r := range rules {
		prefs[i] = r.Priority
	}
	return prefs
}

// TestEnsureRuleInstallsOnlyWhenMissing covers decisions 1-3 of
// issue #70: EnsureRule adds the rule only when no rule with the
// same Family, mark, mask, and table exists, whatever its preference
// or protocol, and deletes all but the lowest-preference copy.
func TestEnsureRuleInstallsOnlyWhenMissing(t *testing.T) {
	t.Parallel()
	narrow := uint32(0xff)
	subnet := &net.IPNet{IP: net.IPv4(192, 0, 2, 0).To4(), Mask: net.CIDRMask(24, 32)}
	ports := netlink.NewRulePortRange(443, 443)
	uids := netlink.NewRuleUIDRange(1000, 1000)
	for _, tc := range []struct {
		name        string
		rules       []netlink.Rule
		wantAdd     bool
		wantDeleted []int // the preferences deleted, in order
	}{
		{name: "no rules", wantAdd: true},
		{name: "one matching rule", rules: []netlink.Rule{listedRule(32765)}},
		{
			name:  "match with another protocol",
			rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Protocol = unix.RTPROT_BOOT })},
		},
		{name: "match with another preference", rules: []netlink.Rule{listedRule(100)}},
		{
			name:    "another mark",
			rules:   []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Mark = 0x200 })},
			wantAdd: true,
		},
		{
			name:    "another mask",
			rules:   []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Mask = &narrow })},
			wantAdd: true,
		},
		{
			name:    "no mask",
			rules:   []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Mask = nil })},
			wantAdd: true,
		},
		{
			name:    "another table",
			rules:   []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Table = 200 })},
			wantAdd: true,
		},
		{
			name:    "another Family",
			rules:   []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Family = unix.AF_INET6 })},
			wantAdd: true,
		},
		// A rule with any other selector routes only part of the
		// mark's traffic to the table, so it is not a copy.
		{name: "partial: source", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Src = subnet })}, wantAdd: true},
		{name: "partial: destination", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Dst = subnet })}, wantAdd: true},
		{name: "partial: iif", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.IifName = "eth0" })}, wantAdd: true},
		{name: "partial: oif", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.OifName = "eth0" })}, wantAdd: true},
		{name: "partial: not", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Invert = true })}, wantAdd: true},
		{name: "partial: tos", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Tos = 0x10 })}, wantAdd: true},
		{name: "partial: tun_id", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.TunID = 7 })}, wantAdd: true},
		{name: "partial: ipproto", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.IPProto = unix.IPPROTO_TCP })}, wantAdd: true},
		{name: "partial: sport", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Sport = ports })}, wantAdd: true},
		{name: "partial: dport", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Dport = ports })}, wantAdd: true},
		{name: "partial: uidrange", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.UIDRange = uids })}, wantAdd: true},
		// suppress_prefixlength 0 is the common setting, so the
		// unset value is RuleList's -1, not 0.
		{name: "partial: suppress_prefixlength", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.SuppressPrefixlen = 0 })}, wantAdd: true},
		{name: "partial: suppress_ifgroup", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.SuppressIfgroup = 0 })}, wantAdd: true},
		{name: "goto", rules: []netlink.Rule{withRule(32765, func(r *netlink.Rule) { r.Goto = 32766 })}, wantAdd: true},
		{
			// The user's partial rule has the lower preference, but
			// it is neither the kept copy nor a duplicate to delete.
			name: "partial user rule beside the full rule",
			rules: []netlink.Rule{
				withRule(100, func(r *netlink.Rule) { r.IifName = "eth0" }),
				listedRule(32765),
			},
		},
		{
			name: "partial user rule beside two full rules",
			rules: []netlink.Rule{
				listedRule(32765),
				withRule(100, func(r *netlink.Rule) { r.Src = subnet }),
				listedRule(32764),
			},
			wantDeleted: []int{32765},
		},
		{
			name: "three matching rules",
			rules: []netlink.Rule{
				listedRule(32765),
				withRule(32762, func(r *netlink.Rule) { r.Table = 200 }),
				listedRule(32763),
				withRule(32764, func(r *netlink.Rule) { r.Protocol = unix.RTPROT_BOOT }),
			},
			wantDeleted: []int{32764, 32765},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeRuleOps{rules: tc.rules}
			removed, err := ensureRuleVia(t.Context(), f.ops(), validFwmarkRule())
			if err != nil {
				t.Fatalf("ensureRuleVia = %v, want nil", err)
			}
			if want := []int{unix.AF_INET}; !slices.Equal(f.listed, want) {
				t.Errorf("listed families = %v, want %v", f.listed, want)
			}
			switch {
			case tc.wantAdd && len(f.added) != 1:
				t.Errorf("adds = %d, want 1", len(f.added))
			case tc.wantAdd:
				got := f.added[0]
				if got.Family != unix.AF_INET || got.Mark != 0x100 || got.Mask == nil ||
					*got.Mask != 0xffffffff || got.Table != 100 || got.Protocol != unix.RTPROT_STATIC {
					t.Errorf("added %v, want fwmark 0x100/0xffffffff table 100 proto static", got)
				}
				if got.Priority != -1 {
					t.Errorf("added preference %d, want none (-1), so the kernel assigns it", got.Priority)
				}
			case len(f.added) != 0:
				t.Errorf("adds = %d, want 0", len(f.added))
			}
			if got := preferences(f.deleted); !slices.Equal(got, tc.wantDeleted) {
				t.Errorf("deleted preferences = %v, want %v", got, tc.wantDeleted)
			}
			if removed != len(tc.wantDeleted) {
				t.Errorf("removed = %d, want %d", removed, len(tc.wantDeleted))
			}
		})
	}
}

// TestEnsureRuleIsIdempotent: a second EnsureRule for the same rule
// finds the first one's rule and changes nothing.
func TestEnsureRuleIsIdempotent(t *testing.T) {
	t.Parallel()
	f := &fakeRuleOps{}
	ops := f.ops()
	add := ops.add
	ops.add = func(r *netlink.Rule) error {
		installed := *r
		installed.Priority = 32765
		f.rules = append(f.rules, installed)
		return add(r)
	}
	for i := range 2 {
		if _, err := ensureRuleVia(t.Context(), ops, validFwmarkRule()); err != nil {
			t.Fatalf("ensureRuleVia #%d = %v, want nil", i+1, err)
		}
	}
	if len(f.added) != 1 || len(f.deleted) != 0 {
		t.Errorf("adds = %d, deletes = %d; want 1 and 0", len(f.added), len(f.deleted))
	}
}

func TestEnsureRuleErrors(t *testing.T) {
	t.Parallel()
	errKernel := fmt.Errorf("kernel: %w", unix.EPERM)
	for _, tc := range []struct {
		name    string
		f       *fakeRuleOps
		wantErr error // nil: ensureRuleVia succeeds
	}{
		{name: "list fails", f: &fakeRuleOps{listErr: errKernel}, wantErr: unix.EPERM},
		{name: "add fails", f: &fakeRuleOps{addErr: errKernel}, wantErr: unix.EPERM},
		// A rule that appeared between the list and the add does
		// the job.
		{name: "add finds the rule", f: &fakeRuleOps{addErr: unix.EEXIST}},
		{
			name:    "delete fails",
			f:       &fakeRuleOps{rules: []netlink.Rule{listedRule(32765), listedRule(32764)}, delErr: errKernel},
			wantErr: unix.EPERM,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ensureRuleVia(t.Context(), tc.f.ops(), validFwmarkRule())
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ensureRuleVia = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ensureRuleVia = %v, want it to wrap %v", err, tc.wantErr)
			}
			for _, sub := range []string{"fwmark=256", "table=100", "family=v4"} {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("err = %q, want it to name %q", err, sub)
				}
			}
		})
	}
}

// TestEnsureRuleHonoursCancellation: a cancelled ctx stops EnsureRule
// before it lists, and a ctx cancelled during the list stops it
// before it mutates.
func TestEnsureRuleHonoursCancellation(t *testing.T) {
	t.Parallel()
	t.Run("at entry", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		f := &fakeRuleOps{}
		if _, err := ensureRuleVia(ctx, f.ops(), validFwmarkRule()); !errors.Is(err, context.Canceled) {
			t.Errorf("ensureRuleVia = %v, want context.Canceled", err)
		}
		if n := f.calls(); n != 0 {
			t.Errorf("kernel calls = %d, want 0", n)
		}
	})
	t.Run("after the list", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		f := &fakeRuleOps{rules: []netlink.Rule{listedRule(32765), listedRule(32764)}, onList: cancel}
		if _, err := ensureRuleVia(ctx, f.ops(), validFwmarkRule()); !errors.Is(err, context.Canceled) {
			t.Errorf("ensureRuleVia = %v, want context.Canceled", err)
		}
		if len(f.added) != 0 || len(f.deleted) != 0 {
			t.Errorf("adds = %d, deletes = %d after cancellation; want 0 and 0", len(f.added), len(f.deleted))
		}
	})
	t.Run("public EnsureRule", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := EnsureRule(ctx, validFwmarkRule()); !errors.Is(err, context.Canceled) {
			t.Errorf("EnsureRule = %v, want context.Canceled", err)
		}
	})
}

// TestEnsureRuleSkipsKernelOnValidationFailure: validation happens
// before any netlink call, so a bad rule never reaches the kernel.
func TestEnsureRuleSkipsKernelOnValidationFailure(t *testing.T) {
	t.Parallel()
	f := &fakeRuleOps{}
	r := validFwmarkRule()
	r.Mark = 0 // validator rejects
	if _, err := ensureRuleVia(t.Context(), f.ops(), r); err == nil {
		t.Error("ensureRuleVia(invalid) = nil err, want error")
	}
	if n := f.calls(); n != 0 {
		t.Errorf("kernel calls = %d, want 0", n)
	}
}

func TestValidateRuleRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*FwmarkRule)
		wantSub string
	}{
		{"invalid family", func(r *FwmarkRule) { r.Family = probe.Family(99) }, "invalid family"},
		{"mark zero", func(r *FwmarkRule) { r.Mark = 0 }, "invalid mark"},
		{"mark negative", func(r *FwmarkRule) { r.Mark = -1 }, "invalid mark"},
		{"table zero", func(r *FwmarkRule) { r.Table = 0 }, "invalid table"},
		{"table negative", func(r *FwmarkRule) { r.Table = -5 }, "invalid table"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := validFwmarkRule()
			tc.mutate(&r)
			err := validateRule(r)
			if err == nil {
				t.Fatalf("validateRule = nil, want error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}
