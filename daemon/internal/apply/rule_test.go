package apply

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func validFwmarkRule() FwmarkRule {
	return FwmarkRule{
		Family: FamilyV4,
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
	v6.Family = FamilyV6
	if err := validateRule(v6); err != nil {
		t.Errorf("validateRule(v6) = %v, want nil", err)
	}
}

func TestEnsureRuleSwallowsEEXIST(t *testing.T) {
	t.Parallel()
	// A leftover rule from a previous daemon run must not fail
	// EnsureRule. Locks the contract against a future vendored-
	// netlink update that drops the %w wrapping on syscall errors.
	ruleAdd := func(*netlink.Rule) error { return unix.EEXIST }
	if err := ensureRuleVia(ruleAdd, validFwmarkRule()); err != nil {
		t.Errorf("ensureRuleVia(EEXIST) = %v, want nil (idempotent)", err)
	}
}

func TestEnsureRuleWrapsOtherErrors(t *testing.T) {
	t.Parallel()
	// Non-EEXIST errors must propagate, wrapped — callers
	// distinguish via errors.Is on the underlying syscall errno.
	ruleAdd := func(*netlink.Rule) error { return fmt.Errorf("kernel: %w", unix.EPERM) }
	err := ensureRuleVia(ruleAdd, validFwmarkRule())
	if err == nil {
		t.Fatal("ensureRuleVia(EPERM) = nil, want wrapped error")
	}
	if !errors.Is(err, unix.EPERM) {
		t.Errorf("errors.Is(err, EPERM) = false; want true so callers can match")
	}
}

func TestValidateRuleRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*FwmarkRule)
		wantSub string
	}{
		{"invalid family", func(r *FwmarkRule) { r.Family = Family(99) }, "invalid family"},
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
