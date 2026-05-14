package rtnl

import (
	"context"
	"errors"
	"strings"
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
		Header:    unix.NlMsghdr{Type: unix.RTM_NEWLINK},
		IfInfomsg: nl.IfInfomsg{IfInfomsg: unix.IfInfomsg{Flags: flags}},
		Link: &netlink.Device{
			LinkAttrs: netlink.LinkAttrs{Name: name, OperState: oper},
		},
	}
}

// mkDelete builds a RTM_DELLINK update for `name`.
func mkDelete(name string) netlink.LinkUpdate {
	return netlink.LinkUpdate{
		Header: unix.NlMsghdr{Type: unix.RTM_DELLINK},
		Link: &netlink.Device{
			LinkAttrs: netlink.LinkAttrs{Name: name},
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
	s := &LinkSubscriber{}
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
	s := &LinkSubscriber{}
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
	s := &LinkSubscriber{}
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
	s := &LinkSubscriber{Interfaces: map[string]struct{}{"eth0": {}}}
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

func TestHandleUpdateDeleteEmitsDownNotpresent(t *testing.T) {
	t.Parallel()
	// A previously up interface that gets RTM_DELLINK must surface
	// as carrier=down/operstate=notpresent so the selector drops it.
	s := &LinkSubscriber{}
	state := map[string]LinkState{}
	s.handleUpdate(state, mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp))

	ev, ok := s.handleUpdate(state, mkDelete("eth0"))
	if !ok {
		t.Fatal("RTM_DELLINK on up iface did not emit")
	}
	if ev.Carrier != CarrierDown || ev.Operstate != OperstateNotPresent {
		t.Errorf("event = %+v, want carrier=down/operstate=notpresent", ev)
	}
	if _, still := state["eth0"]; still {
		t.Error("state map still contains eth0 after RTM_DELLINK")
	}
}

func TestHandleUpdateDeleteBoundsStateOnNoChange(t *testing.T) {
	t.Parallel()
	// If the prior state already matches down/notpresent (unusual
	// but possible), RTM_DELLINK still has to evict the map entry
	// so transient veth/dummy churn doesn't grow the map.
	s := &LinkSubscriber{}
	state := map[string]LinkState{
		"veth0": {Name: "veth0", Carrier: CarrierDown, Operstate: OperstateNotPresent},
	}
	if _, ok := s.handleUpdate(state, mkDelete("veth0")); ok {
		t.Error("RTM_DELLINK on already-down iface emitted; want suppressed")
	}
	if _, still := state["veth0"]; still {
		t.Error("state map still contains veth0 — map is unbounded")
	}
}

func TestHandleUpdateNilInterfaceSetMatchesAll(t *testing.T) {
	t.Parallel()
	s := &LinkSubscriber{}
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
	go func() { errCh <- (&LinkSubscriber{}).runLoop(ctx, updates, out) }()

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

	err := (&LinkSubscriber{}).runLoop(context.Background(), updates, out)
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

	err := (&LinkSubscriber{}).runLoop(ctx, updates, out)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestLinkSubscriberRunViaWrapsSubscribeError: a netlink subscribe
// failure must be wrapped with the `rtnl: LinkSubscribe:` prefix
// so logs name the layer responsible.
func TestLinkSubscriberRunViaWrapsSubscribeError(t *testing.T) {
	t.Parallel()
	want := errors.New("netlink: subscribe denied")
	subscribe := func(chan<- netlink.LinkUpdate, <-chan struct{}, netlink.LinkSubscribeOptions) error {
		return want
	}
	err := (&LinkSubscriber{}).runVia(context.Background(), subscribe, make(chan LinkEvent, 1))
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want subscribe-error chained via %%w", err)
	}
	if !strings.Contains(err.Error(), "rtnl: LinkSubscribe") {
		t.Errorf("err = %q, want 'rtnl: LinkSubscribe' prefix", err.Error())
	}
}

// TestLinkSubscriberRunViaWiresSubscribeToRunLoop: a successful
// subscribe hands the `updates` channel through to runLoop — a
// LinkUpdate written by the stub must surface as a LinkEvent on
// `out`, proving the wire-up isn't dropped on the floor.
func TestLinkSubscriberRunViaWiresSubscribeToRunLoop(t *testing.T) {
	t.Parallel()
	// `subscribe` writes synthetic updates to the channel runVia
	// hands it, then signals readiness via `subscribed`. Writing
	// from the runVia goroutine itself avoids racing the producer
	// against an external sender that doesn't know when ch is
	// non-nil.
	subscribed := make(chan struct{})
	subscribe := func(ch chan<- netlink.LinkUpdate, _ <-chan struct{}, _ netlink.LinkSubscribeOptions) error {
		ch <- mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp)
		close(subscribed)
		return nil
	}
	out := make(chan LinkEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- (&LinkSubscriber{}).runVia(ctx, subscribe, out)
	}()

	<-subscribed
	select {
	case ev := <-out:
		if ev.Name != "eth0" || ev.Carrier != CarrierUp {
			t.Errorf("event = %+v, want eth0/up", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no LinkEvent within 1s; subscribe→runLoop wiring broken")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("runVia returned %v, want context.Canceled after cancel", err)
	}
}
