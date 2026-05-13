package apply

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/vishvananda/netlink"
)

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
	if err := validateFlush(probe.FamilyV4, net.ParseIP("192.0.2.1")); err != nil {
		t.Errorf("validateFlush(v4) = %v, want nil", err)
	}
	if err := validateFlush(probe.FamilyV6, net.ParseIP("2001:db8::1")); err != nil {
		t.Errorf("validateFlush(v6) = %v, want nil", err)
	}
}

func TestValidateFlushRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		family  probe.Family
		ip      net.IP
		wantSub string
	}{
		{"bad family", probe.Family(99), net.ParseIP("192.0.2.1"), "invalid family"},
		{"nil ip", probe.FamilyV4, nil, "ip is nil"},
		{"v6 ip family=v4", probe.FamilyV4, net.ParseIP("2001:db8::1"), "is not v4"},
		{"v4 ip family=v6", probe.FamilyV6, net.ParseIP("192.0.2.1"), "is v4 but family=v6"},
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
	n, err := FlushBySource(t.Context(), probe.FamilyV4, nil)
	if err == nil {
		t.Fatal("FlushBySource(nil ip) = nil err, want validation error")
	}
	if n != 0 {
		t.Errorf("count = %d, want 0 on validation error", n)
	}
}

func TestFlushBySourceViaHappyPath(t *testing.T) {
	t.Parallel()
	var (
		gotTable   netlink.ConntrackTableType
		gotFamily  netlink.InetFamily
		gotFilters int
	)
	del := func(
		table netlink.ConntrackTableType,
		family netlink.InetFamily,
		filters ...netlink.CustomConntrackFilter,
	) (uint, error) {
		gotTable = table
		gotFamily = family
		gotFilters = len(filters)
		return 7, nil
	}

	n, err := flushBySourceVia(context.Background(), del, probe.FamilyV4, net.ParseIP("192.0.2.1"))
	if err != nil {
		t.Fatalf("flushBySourceVia(happy) = %v, want nil", err)
	}
	if n != 7 {
		t.Errorf("n = %d, want 7 (passthrough from stub)", n)
	}
	if gotTable != netlink.ConntrackTable {
		t.Errorf("table = %v, want ConntrackTable", gotTable)
	}
	if gotFamily != netlink.InetFamily(probe.FamilyV4) {
		t.Errorf("family = %v, want %v", gotFamily, netlink.InetFamily(probe.FamilyV4))
	}
	if gotFilters != 2 {
		t.Errorf("len(filters) = %d, want 2 (orig + reply)", gotFilters)
	}
}

func TestFlushBySourceViaWrapsDeleteError(t *testing.T) {
	t.Parallel()
	del := func(
		netlink.ConntrackTableType,
		netlink.InetFamily,
		...netlink.CustomConntrackFilter,
	) (uint, error) {
		return 3, errors.New("kernel: ENOMEM")
	}

	n, err := flushBySourceVia(context.Background(), del, probe.FamilyV4, net.ParseIP("192.0.2.1"))
	if err == nil {
		t.Fatal("flushBySourceVia(error) = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "ENOMEM") {
		t.Errorf("err = %q, want it to surface underlying ENOMEM", err.Error())
	}
	if !strings.Contains(err.Error(), "apply: conntrack flush") {
		t.Errorf("err = %q, want apply context prefix", err.Error())
	}
	// PLAN §5.5: conntrack flush returns the partial count even on
	// error so the caller can log "deleted N before failing".
	if n != 3 {
		t.Errorf("count on error = %d, want 3 (partial deletion preserved)", n)
	}
}

func TestFlushBySourceViaContextCancelled(t *testing.T) {
	t.Parallel()
	del := func(
		netlink.ConntrackTableType,
		netlink.InetFamily,
		...netlink.CustomConntrackFilter,
	) (uint, error) {
		t.Fatal("delete stub called after ctx cancel")
		return 0, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	n, err := flushBySourceVia(ctx, del, probe.FamilyV4, net.ParseIP("192.0.2.1"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("flushBySourceVia(cancelled) = %v, want context.Canceled", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0 on cancellation", n)
	}
}

func TestFlushBySourceViaSkipsDeleteOnValidationFailure(t *testing.T) {
	t.Parallel()
	del := func(
		netlink.ConntrackTableType,
		netlink.InetFamily,
		...netlink.CustomConntrackFilter,
	) (uint, error) {
		t.Fatal("delete stub called despite validation failure")
		return 0, nil
	}

	_, err := flushBySourceVia(context.Background(), del, probe.FamilyV4, nil)
	if err == nil {
		t.Error("flushBySourceVia(nil ip) = nil error, want non-nil")
	}
}
