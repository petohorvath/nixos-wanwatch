package apply

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// validRoute is the canonical happy-path input — tests mutate
// individual fields to exercise each validation branch.
func validRoute() DefaultRoute {
	return DefaultRoute{
		Family:  probe.FamilyV4,
		Table:   100,
		Gateway: net.ParseIP("192.0.2.1"),
		IfIndex: 3,
	}
}

func TestBuildRoutePopulatesNetlinkStruct(t *testing.T) {
	t.Parallel()
	d := validRoute()
	got := buildRoute(d)
	if got.Family != int(d.Family) {
		t.Errorf("Family = %d, want %d", got.Family, d.Family)
	}
	if got.Table != d.Table {
		t.Errorf("Table = %d, want %d", got.Table, d.Table)
	}
	if !got.Gw.Equal(d.Gateway) {
		t.Errorf("Gw = %v, want %v", got.Gw, d.Gateway)
	}
	if got.LinkIndex != d.IfIndex {
		t.Errorf("LinkIndex = %d, want %d", got.LinkIndex, d.IfIndex)
	}
	// Default-route convention: Dst == nil. The kernel treats a
	// route with no destination as the default for its family.
	if got.Dst != nil {
		t.Errorf("Dst = %v, want nil (default route)", got.Dst)
	}
}

func TestValidateDefaultRouteAcceptsHappyPath(t *testing.T) {
	t.Parallel()
	if err := validateDefaultRoute(validRoute()); err != nil {
		t.Errorf("validateDefaultRoute(happy) = %v, want nil", err)
	}
	v6 := validRoute()
	v6.Family = probe.FamilyV6
	v6.Gateway = net.ParseIP("2001:db8::1")
	if err := validateDefaultRoute(v6); err != nil {
		t.Errorf("validateDefaultRoute(v6) = %v, want nil", err)
	}
}

func TestValidateDefaultRouteRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*DefaultRoute)
		wantSub string
	}{
		{
			name:    "invalid family",
			mutate:  func(d *DefaultRoute) { d.Family = probe.Family(99) },
			wantSub: "invalid family",
		},
		{
			name:    "table zero",
			mutate:  func(d *DefaultRoute) { d.Table = 0 },
			wantSub: "invalid table",
		},
		{
			name:    "table negative",
			mutate:  func(d *DefaultRoute) { d.Table = -1 },
			wantSub: "invalid table",
		},
		{
			name:    "nil gateway",
			mutate:  func(d *DefaultRoute) { d.Gateway = nil },
			wantSub: "gateway is nil",
		},
		{
			name:    "zero ifindex",
			mutate:  func(d *DefaultRoute) { d.IfIndex = 0 },
			wantSub: "invalid ifindex",
		},
		{
			name: "v6 gateway with family=v4",
			mutate: func(d *DefaultRoute) {
				d.Family = probe.FamilyV4
				d.Gateway = net.ParseIP("2001:db8::1")
			},
			wantSub: "not v4",
		},
		{
			name: "v4 gateway with family=v6",
			mutate: func(d *DefaultRoute) {
				d.Family = probe.FamilyV6
				d.Gateway = net.ParseIP("192.0.2.1")
			},
			wantSub: "is v4 but family=v6",
		},
		{
			name: "pointToPoint with gateway",
			mutate: func(d *DefaultRoute) {
				d.PointToPoint = true
				// Gateway still set from validRoute() — should reject.
			},
			wantSub: "pointToPoint route must have nil Gateway",
		},
		{
			// PointToPoint reaches its own family-check branch
			// (separate from the gateway path) — pin that the
			// pointToPoint-true codepath rejects an invalid family
			// rather than passing it through to netlink.
			name: "pointToPoint with invalid family",
			mutate: func(d *DefaultRoute) {
				d.PointToPoint = true
				d.Gateway = nil
				d.Family = probe.Family(99)
			},
			wantSub: "invalid family",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := validRoute()
			tc.mutate(&d)
			err := validateDefaultRoute(d)
			if err == nil {
				t.Fatalf("validateDefaultRoute = nil, want error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidateDefaultRouteAcceptsPointToPoint(t *testing.T) {
	t.Parallel()
	d := DefaultRoute{
		Family:       probe.FamilyV4,
		Table:        100,
		IfIndex:      3,
		PointToPoint: true,
	}
	if err := validateDefaultRoute(d); err != nil {
		t.Errorf("validateDefaultRoute(ptp) = %v, want nil", err)
	}
}

func TestBuildRouteEmitsScopeLinkForPointToPoint(t *testing.T) {
	t.Parallel()
	d := DefaultRoute{
		Family:       probe.FamilyV4,
		Table:        100,
		IfIndex:      3,
		PointToPoint: true,
	}
	got := buildRoute(d)
	if got.Gw != nil {
		t.Errorf("Gw = %v, want nil under pointToPoint", got.Gw)
	}
	if int(got.Scope) != unix.RT_SCOPE_LINK {
		t.Errorf("Scope = %d, want RT_SCOPE_LINK (%d)", got.Scope, unix.RT_SCOPE_LINK)
	}
	// vishvananda/netlink refuses to send RTM_NEWROUTE when none
	// of Dst/Gw/Src/MPLSDst is set. For scope-link we have no Gw,
	// so Dst must carry the family-zero CIDR explicitly.
	if got.Dst == nil {
		t.Fatal("Dst = nil under pointToPoint; want 0.0.0.0/0 so netlink accepts the message")
	}
	if !got.Dst.IP.Equal(net.IPv4zero) {
		t.Errorf("Dst.IP = %v, want IPv4 zero", got.Dst.IP)
	}
	ones, _ := got.Dst.Mask.Size()
	if ones != 0 {
		t.Errorf("Dst prefix = %d, want 0 (default route)", ones)
	}
}

func TestBuildRouteEmitsScopeLinkForPointToPointV6(t *testing.T) {
	t.Parallel()
	d := DefaultRoute{
		Family:       probe.FamilyV6,
		Table:        100,
		IfIndex:      3,
		PointToPoint: true,
	}
	got := buildRoute(d)
	if got.Dst == nil || !got.Dst.IP.Equal(net.IPv6zero) {
		t.Errorf("Dst = %v, want ::/0 under v6 pointToPoint", got.Dst)
	}
	ones, bits := got.Dst.Mask.Size()
	if ones != 0 || bits != 128 {
		t.Errorf("Dst mask = %d/%d, want 0/128", ones, bits)
	}
}

func TestWriteDefaultViaHappyPath(t *testing.T) {
	t.Parallel()
	var got *netlink.Route
	replace := func(r *netlink.Route) error {
		got = r
		return nil
	}

	d := validRoute()
	if err := writeDefaultVia(context.Background(), replace, d); err != nil {
		t.Fatalf("writeDefaultVia(happy) = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("replace stub was not called")
	}
	if got.Family != int(d.Family) || got.Table != d.Table {
		t.Errorf("netlink.Route passthrough mismatch: got %+v from %+v", got, d)
	}
}

func TestWriteDefaultViaWrapsReplaceError(t *testing.T) {
	t.Parallel()
	replace := func(*netlink.Route) error { return errors.New("boom") }

	err := writeDefaultVia(context.Background(), replace, validRoute())
	if err == nil {
		t.Fatal("writeDefaultVia(error) = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %q, want to contain underlying %q", err.Error(), "boom")
	}
	if !strings.Contains(err.Error(), "apply: route replace") {
		t.Errorf("err = %q, want to carry the apply context prefix", err.Error())
	}
}

func TestWriteDefaultViaContextCancelled(t *testing.T) {
	t.Parallel()
	// A pre-cancelled ctx must short-circuit before any validation
	// or netlink work. We assert by having the replace stub fail
	// loudly if it's ever called.
	replace := func(*netlink.Route) error {
		t.Fatal("replace stub called after ctx cancel")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := writeDefaultVia(ctx, replace, validRoute())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("writeDefaultVia(cancelled) = %v, want context.Canceled", err)
	}
}

func TestWriteDefaultViaSkipsReplaceOnValidationFailure(t *testing.T) {
	t.Parallel()
	replace := func(*netlink.Route) error {
		t.Fatal("replace stub called despite validation failure")
		return nil
	}
	d := validRoute()
	d.Table = 0 // validator rejects

	if err := writeDefaultVia(context.Background(), replace, d); err == nil {
		t.Error("writeDefaultVia(invalid) = nil error, want non-nil")
	}
}
