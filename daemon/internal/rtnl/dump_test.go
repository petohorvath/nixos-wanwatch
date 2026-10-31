package rtnl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// interruptedThen returns a dump that fails with ErrDumpInterrupted,
// returning partial as its partial result, for the first interrupts
// calls, then returns final. calls counts every call.
func interruptedThen[T any](interrupts int, partial, final T, calls *int) func() (T, error) {
	return func() (T, error) {
		*calls++
		if *calls <= interrupts {
			return partial, netlink.ErrDumpInterrupted
		}
		return final, nil
	}
}

func TestRetryDump(t *testing.T) {
	t.Parallel()
	other := errors.New("netlink: permission denied")
	cases := []struct {
		name      string
		dump      func(calls *int) func() (string, error)
		wantValue string
		wantErr   error
		wantCalls int
	}{
		{
			name:      "first attempt succeeds",
			dump:      func(calls *int) func() (string, error) { return interruptedThen(0, "partial", "full", calls) },
			wantValue: "full",
			wantCalls: 1,
		},
		{
			name:      "interrupted once",
			dump:      func(calls *int) func() (string, error) { return interruptedThen(1, "partial", "full", calls) },
			wantValue: "full",
			wantCalls: 2,
		},
		{
			name: "interrupted until the last attempt",
			dump: func(calls *int) func() (string, error) {
				return interruptedThen(dumpAttempts-1, "partial", "full", calls)
			},
			wantValue: "full",
			wantCalls: dumpAttempts,
		},
		{
			name: "interrupted on every attempt",
			dump: func(calls *int) func() (string, error) {
				return interruptedThen(dumpAttempts, "partial", "full", calls)
			},
			wantErr:   netlink.ErrDumpInterrupted,
			wantCalls: dumpAttempts,
		},
		{
			name: "another error is not retried",
			dump: func(calls *int) func() (string, error) {
				return func() (string, error) {
					*calls++
					return "", other
				}
			},
			wantErr:   other,
			wantCalls: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			got, err := retryDump(t.Context(), tc.dump(&calls))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.wantValue {
				t.Errorf("value = %q, want %q (an interrupted dump's partial result must not leak)", got, tc.wantValue)
			}
			if calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// TestRetryDumpNamesAttemptsWhenExhausted: the fatal error says how
// many times the dump was tried, so a restart under churn is diagnosable.
func TestRetryDumpNamesAttemptsWhenExhausted(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := retryDump(t.Context(), interruptedThen(dumpAttempts, 0, 1, &calls))
	if want := fmt.Sprintf("all %d attempts", dumpAttempts); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to mention %q", err, want)
	}
}

// TestRetryDumpStopsOnceCancelled: cancellation between attempts ends
// the retries with the context's error instead of dumping again.
func TestRetryDumpStopsOnceCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := retryDump(ctx, func() (int, error) {
		calls++
		cancel()
		return 0, netlink.ErrDumpInterrupted
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// TestLinkSubscriberReadSnapshotRetriesInterruptedDump: the snapshot
// comes from the first complete LinkList, and links seen only in an
// interrupted dump's partial result stay out of the snapshot and the
// state map.
func TestLinkSubscriberReadSnapshotRetriesInterruptedDump(t *testing.T) {
	t.Parallel()
	calls := 0
	listFn := interruptedThen(2,
		[]netlink.Link{mkLink("eth1", unix.IFF_LOWER_UP, netlink.OperUp)},
		[]netlink.Link{mkLink("eth0", unix.IFF_LOWER_UP, netlink.OperUp)},
		&calls)
	state := map[string]LinkState{}
	snapshot, err := (&LinkSubscriber{}).readSnapshot(t.Context(), listFn, state)
	if err != nil {
		t.Fatalf("readSnapshot = %v, want nil", err)
	}
	if calls != 3 {
		t.Errorf("LinkList calls = %d, want 3", calls)
	}
	if len(snapshot) != 1 || snapshot[0].Name != "eth0" {
		t.Errorf("snapshot = %+v, want eth0 only", snapshot)
	}
	if _, seen := state["eth1"]; seen {
		t.Error("state records eth1 from an interrupted dump")
	}
}

// TestRouteSubscriberReadSnapshotRetriesInterruptedDump: an
// interrupted dump of one Family re-reads that Family only, and the
// snapshot holds the complete read's defaults.
func TestRouteSubscriberReadSnapshotRetriesInterruptedDump(t *testing.T) {
	t.Parallel()
	v6Calls := 0
	partial := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET6, unix.RT_TABLE_MAIN, net.ParseIP("2001:db8::1"), 7, nil).Route
	final := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET6, unix.RT_TABLE_MAIN, net.ParseIP("2001:db8::2"), 7, nil).Route
	v6 := interruptedThen(1, []netlink.Route{partial}, []netlink.Route{final}, &v6Calls)
	v4Calls := 0
	listFn := func(_ netlink.Link, family int) ([]netlink.Route, error) {
		if family == unix.AF_INET6 {
			return v6()
		}
		v4Calls++
		return nil, nil
	}
	s := mkSub(nil, map[int]string{7: "eth0"})
	snapshot, err := s.readSnapshot(t.Context(), listFn)
	if err != nil {
		t.Fatalf("readSnapshot = %v, want nil", err)
	}
	if v4Calls != 1 || v6Calls != 2 {
		t.Errorf("RouteList calls = v4 %d, v6 %d; want v4 1, v6 2", v4Calls, v6Calls)
	}
	if len(snapshot) != 1 || !snapshot[0].Gateway.Equal(final.Gw) {
		t.Errorf("snapshot = %+v, want one default via %s", snapshot, final.Gw)
	}
}

// TestRouteSubscriberReconcileRetriesInterruptedDump: a live re-read
// that is interrupted reads again and reports the current default,
// not the partial result's Del.
func TestRouteSubscriberReconcileRetriesInterruptedDump(t *testing.T) {
	t.Parallel()
	calls := 0
	current := mkRouteUpdate(unix.RTM_NEWROUTE, unix.AF_INET, unix.RT_TABLE_MAIN, net.ParseIP("192.0.2.1"), 7, nil).Route
	routes := interruptedThen(1, nil, []netlink.Route{current}, &calls)
	listFn := func(netlink.Link, int) ([]netlink.Route, error) { return routes() }
	s := mkSub(nil, map[int]string{7: "eth0"})
	ev, err := s.reconcileVia(t.Context(), listFn, RouteEvent{Op: RouteEventDel, Iface: "eth0", Family: RouteFamilyV4}, nil)
	if err != nil {
		t.Fatalf("reconcileVia = %v, want nil", err)
	}
	if calls != 2 {
		t.Errorf("RouteList calls = %d, want 2", calls)
	}
	if ev.Op != RouteEventAdd || !ev.Gateway.Equal(current.Gw) {
		t.Errorf("event = %+v, want Add via %s", ev, current.Gw)
	}
}
