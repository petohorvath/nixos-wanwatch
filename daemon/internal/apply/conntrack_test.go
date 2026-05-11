package apply

import (
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestToInetFamilyAliasesAFConstants(t *testing.T) {
	t.Parallel()
	if got := toInetFamily(FamilyV4); netlink.InetFamily(FamilyV4) != got {
		t.Errorf("toInetFamily(v4) = %d, want %d", got, FamilyV4)
	}
	if got := toInetFamily(FamilyV6); netlink.InetFamily(FamilyV6) != got {
		t.Errorf("toInetFamily(v6) = %d, want %d", got, FamilyV6)
	}
}

func TestBuildSourceFilterAccepts(t *testing.T) {
	t.Parallel()
	f, err := buildSourceFilter(netlink.ConntrackOrigSrcIP, net.ParseIP("192.0.2.1"))
	if err != nil {
		t.Fatalf("buildSourceFilter = %v, want nil", err)
	}
	if f == nil {
		t.Fatal("buildSourceFilter returned nil filter without error")
	}
}

func TestBuildSourceFilterRejectsNilIP(t *testing.T) {
	t.Parallel()
	if _, err := buildSourceFilter(netlink.ConntrackOrigSrcIP, nil); err == nil {
		t.Error("buildSourceFilter(nil) = nil error, want error")
	}
}

func TestValidateFlushAcceptsHappyPath(t *testing.T) {
	t.Parallel()
	if err := validateFlush(FamilyV4, net.ParseIP("192.0.2.1")); err != nil {
		t.Errorf("validateFlush(v4) = %v, want nil", err)
	}
	if err := validateFlush(FamilyV6, net.ParseIP("2001:db8::1")); err != nil {
		t.Errorf("validateFlush(v6) = %v, want nil", err)
	}
}

func TestValidateFlushRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		family  Family
		ip      net.IP
		wantSub string
	}{
		{"bad family", Family(99), net.ParseIP("192.0.2.1"), "invalid family"},
		{"nil ip", FamilyV4, nil, "ip is nil"},
		{"v6 ip family=v4", FamilyV4, net.ParseIP("2001:db8::1"), "is not v4"},
		{"v4 ip family=v6", FamilyV6, net.ParseIP("192.0.2.1"), "is v4 but family=v6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateFlush(tc.family, tc.ip)
			if err == nil {
				t.Fatalf("validateFlush = nil, want %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestFlushBySourcePropagatesValidationError(t *testing.T) {
	t.Parallel()
	// nil ip → fail fast before any netlink call. This branch is
	// the only one we can drive without root + a live conntrack
	// table; the netlink-bound path is VM-tier per PLAN §9.4.
	n, err := FlushBySource(FamilyV4, nil)
	if err == nil {
		t.Fatal("FlushBySource(nil ip) = nil err, want validation error")
	}
	if n != 0 {
		t.Errorf("count = %d, want 0 on validation error", n)
	}
}
