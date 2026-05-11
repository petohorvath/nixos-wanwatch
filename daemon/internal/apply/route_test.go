package apply

import (
	"net"
	"strings"
	"testing"
)

// validRoute is the canonical happy-path input — tests mutate
// individual fields to exercise each validation branch.
func validRoute() DefaultRoute {
	return DefaultRoute{
		Family:  FamilyV4,
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
	v6.Family = FamilyV6
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
			mutate:  func(d *DefaultRoute) { d.Family = Family(99) },
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
				d.Family = FamilyV4
				d.Gateway = net.ParseIP("2001:db8::1")
			},
			wantSub: "not v4",
		},
		{
			name: "v4 gateway with family=v6",
			mutate: func(d *DefaultRoute) {
				d.Family = FamilyV6
				d.Gateway = net.ParseIP("192.0.2.1")
			},
			wantSub: "is v4 but family=v6",
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
