package rtnl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

// mkUpdate builds a LinkUpdate shaped like what netlink emits, so
// handleUpdate can be exercised without a real netlink socket.
func mkUpdate(name string, flags uint32, oper netlink.LinkOperState) netlink.LinkUpdate {
	return netlink.LinkUpdate{
		IfInfomsg: nl.IfInfomsg{
			IfInfomsg: unix.IfInfomsg{Flags: flags},
		},
		Link: &netlink.Device{
			LinkAttrs: netlink.LinkAttrs{
				Name:      name,
				OperState: oper,
			},
		},
	}
}

func TestCarrierFromFlagsUp(t *testing.T) {
	t.Parallel()
	got := carrierFromFlags(unix.IFF_LOWER_UP)
	if got != CarrierUp {
		t.Errorf("carrierFromFlags(IFF_LOWER_UP) = %v, want CarrierUp", got)
	}
}

func TestCarrierFromFlagsDown(t *testing.T) {
	t.Parallel()
	// IFF_UP without IFF_LOWER_UP is admin-up with cable unplugged
	// — must read as down so the daemon can fast-track failover.
	got := carrierFromFlags(unix.IFF_UP)
	if got != CarrierDown {
		t.Errorf("carrierFromFlags(IFF_UP only) = %v, want CarrierDown", got)
	}
}

func TestHandleUpdateEmitsFirstSighting(t *testing.T) {
	t.Parallel()
	s := &Subscriber{}
	state := map[string]LinkState{}
	upd := mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp)

	ev, ok := s.handleUpdate(state, upd)
	if !ok {
		t.Fatal("first sighting did not emit")
	}
	if ev.Name != "eth0" || ev.Carrier != CarrierUp || ev.Operstate != OperstateUp {
		t.Errorf("event = %+v, want eth0/up/up", ev)
	}
	if state["eth0"].Carrier != CarrierUp {
		t.Errorf("state not recorded: %+v", state["eth0"])
	}
	if ev.Time.IsZero() {
		t.Error("Time = zero, want stamped at emit")
	}
}

func TestHandleUpdateSuppressesDuplicate(t *testing.T) {
	t.Parallel()
	s := &Subscriber{}
	state := map[string]LinkState{}
	upd := mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp)

	if _, ok := s.handleUpdate(state, upd); !ok {
		t.Fatal("first call: expected emit")
	}
	if _, ok := s.handleUpdate(state, upd); ok {
		t.Error("second call with identical update: expected no emit")
	}
}

func TestHandleUpdateEmitsOnChange(t *testing.T) {
	t.Parallel()
	s := &Subscriber{}
	state := map[string]LinkState{}
	s.handleUpdate(state, mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp))

	ev, ok := s.handleUpdate(state, mkUpdate("eth0", 0, netlink.OperDown))
	if !ok {
		t.Fatal("change-of-state did not emit")
	}
	if ev.Carrier != CarrierDown || ev.Operstate != OperstateDown {
		t.Errorf("event = %+v, want carrier=down/operstate=down", ev)
	}
}

func TestHandleUpdateFiltersByInterfaceSet(t *testing.T) {
	t.Parallel()
	s := &Subscriber{Interfaces: map[string]struct{}{"eth0": {}}}
	state := map[string]LinkState{}

	if _, ok := s.handleUpdate(state, mkUpdate("wwan0", unix.IFF_LOWER_UP, netlink.OperUp)); ok {
		t.Error("wwan0 emitted despite not being in watched set")
	}
	if _, recorded := state["wwan0"]; recorded {
		t.Error("wwan0 leaked into state map despite filter")
	}
	if _, ok := s.handleUpdate(state, mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp)); !ok {
		t.Error("eth0 not emitted despite being in watched set")
	}
}

func TestHandleUpdateNilInterfaceSetMatchesAll(t *testing.T) {
	t.Parallel()
	s := &Subscriber{}
	state := map[string]LinkState{}

	if _, ok := s.handleUpdate(state, mkUpdate("anything", unix.IFF_LOWER_UP, netlink.OperUp)); !ok {
		t.Error("nil Interfaces set did not match every interface")
	}
}

func TestRunLoopForwardsEvent(t *testing.T) {
	t.Parallel()
	updates := make(chan netlink.LinkUpdate, 1)
	out := make(chan LinkEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates <- mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp)

	errCh := make(chan error, 1)
	go func() { errCh <- (&Subscriber{}).runLoop(ctx, updates, out) }()

	select {
	case ev := <-out:
		if ev.Name != "eth0" || ev.Carrier != CarrierUp {
			t.Errorf("event = %+v, want eth0/up", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runLoop to forward event")
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRunLoopReturnsOnUpdatesClosed(t *testing.T) {
	t.Parallel()
	// A closed subscription channel must surface as a non-nil
	// error so the caller can decide whether to retry.
	updates := make(chan netlink.LinkUpdate)
	out := make(chan LinkEvent, 1)
	close(updates)

	err := (&Subscriber{}).runLoop(context.Background(), updates, out)
	if err == nil {
		t.Fatal("runLoop on closed channel returned nil, want error")
	}
}

func TestRunLoopCancelsBetweenUpdates(t *testing.T) {
	t.Parallel()
	updates := make(chan netlink.LinkUpdate)
	out := make(chan LinkEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := (&Subscriber{}).runLoop(ctx, updates, out)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
