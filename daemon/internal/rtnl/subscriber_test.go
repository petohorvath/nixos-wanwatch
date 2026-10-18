package rtnl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
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

// mkLink builds a netlink.Link shaped like what netlink.LinkList
// returns — the IFF_* flags live in LinkAttrs.RawFlags, not a
// message header, so readSnapshot must read them from there.
func mkLink(name string, rawFlags uint32, oper netlink.LinkOperState) netlink.Link {
	return &netlink.Device{
		LinkAttrs: netlink.LinkAttrs{Name: name, OperState: oper, RawFlags: rawFlags},
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

func TestHandleUpdateDeleteSuppressesUnseen(t *testing.T) {
	t.Parallel()
	// A link that stays deleted reads as RTM_DELLINK on every later
	// notification. Only the first delete after a sighting may emit,
	// and a link never sighted has nothing to withdraw.
	s := &LinkSubscriber{}
	state := map[string]LinkState{}
	s.handleUpdate(state, mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp))
	if _, ok := s.handleUpdate(state, mkDelete("eth0")); !ok {
		t.Fatal("first RTM_DELLINK after a sighting did not emit")
	}
	if _, ok := s.handleUpdate(state, mkDelete("eth0")); ok {
		t.Error("repeated RTM_DELLINK emitted; want suppressed")
	}
	if _, ok := s.handleUpdate(state, mkDelete("eth1")); ok {
		t.Error("RTM_DELLINK on never-sighted iface emitted; want suppressed")
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
	getFn := func(string) (netlink.Link, error) {
		return mkLink("eth0", unix.IFF_LOWER_UP, netlink.OperUp), nil
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- (&LinkSubscriber{}).runLoop(ctx, updates, getFn, map[string]LinkState{}, out)
	}()

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
	// A closed subscription channel must surface as the
	// errSubscriptionClosed sentinel so StartWith can translate it
	// into the ErrorCallback's captured cause.
	updates := make(chan netlink.LinkUpdate)
	out := make(chan LinkEvent, 1)
	close(updates)

	err := (&LinkSubscriber{}).runLoop(context.Background(), updates, unexpectedLinkByName(t), map[string]LinkState{}, out)
	if !errors.Is(err, errSubscriptionClosed) {
		t.Fatalf("runLoop on closed channel = %v, want errSubscriptionClosed", err)
	}
}

func TestRunLoopCancelsBetweenUpdates(t *testing.T) {
	t.Parallel()
	updates := make(chan netlink.LinkUpdate)
	out := make(chan LinkEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := (&LinkSubscriber{}).runLoop(ctx, updates, unexpectedLinkByName(t), map[string]LinkState{}, out)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestRunLoopSkipsUnwatchedWithoutReading: an update on an
// interface outside the watched set must not cost a link read.
func TestRunLoopSkipsUnwatchedWithoutReading(t *testing.T) {
	t.Parallel()
	updates := make(chan netlink.LinkUpdate, 1)
	updates <- mkUpdate("wwan0", unix.IFF_LOWER_UP, netlink.OperUp)
	close(updates)
	s := &LinkSubscriber{Interfaces: map[string]struct{}{"eth0": {}}}
	out := make(chan LinkEvent, 1)

	err := s.runLoop(context.Background(), updates, unexpectedLinkByName(t), map[string]LinkState{}, out)
	if !errors.Is(err, errSubscriptionClosed) {
		t.Fatalf("runLoop = %v, want errSubscriptionClosed", err)
	}
	if len(out) != 0 {
		t.Errorf("unwatched update emitted %+v", <-out)
	}
}

// TestLinkSubscriberStartWrapsSubscribeError: a netlink subscribe
// failure must be wrapped with the `rtnl: LinkSubscribe:` prefix
// so logs name the layer responsible, and no snapshot is taken.
func TestLinkSubscriberStartWrapsSubscribeError(t *testing.T) {
	t.Parallel()
	want := errors.New("netlink: subscribe denied")
	subscribe := func(chan<- netlink.LinkUpdate, <-chan struct{}, netlink.LinkSubscribeOptions) error {
		return want
	}
	listFn := func() ([]netlink.Link, error) {
		t.Error("LinkList called after subscribe failed")
		return nil, nil
	}
	_, exited, err := (&LinkSubscriber{}).StartWith(context.Background(), subscribe, listFn, unexpectedLinkByName(t), make(chan LinkEvent, 1))
	if !errors.Is(err, want) || exited != nil {
		t.Fatalf("Start = %v, %v; want no worker and subscribe-error chained via %%w", exited, err)
	}
	if !strings.Contains(err.Error(), "rtnl: LinkSubscribe") {
		t.Errorf("err = %q, want 'rtnl: LinkSubscribe' prefix", err.Error())
	}
}

// TestLinkSubscriberStartWiresSubscribeToRunLoop: a successful
// subscribe hands the `updates` channel through to runLoop — a
// LinkUpdate written by the stub must surface as a LinkEvent on
// `out`, proving the wire-up isn't dropped on the floor.
func TestLinkSubscriberStartWiresSubscribeToRunLoop(t *testing.T) {
	t.Parallel()
	subscribe := func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, opts netlink.LinkSubscribeOptions) error {
		if opts.ListExisting {
			t.Error("ListExisting enabled; the snapshot comes from LinkList")
		}
		ch <- mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp)
		closeLinkUpdatesOnStop(ch, done)
		return nil
	}
	getFn := func(string) (netlink.Link, error) {
		return mkLink("eth0", unix.IFF_LOWER_UP, netlink.OperUp), nil
	}
	out := make(chan LinkEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, exited, err := (&LinkSubscriber{}).StartWith(ctx, subscribe, emptyLinkList, getFn, out)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-out:
		if ev.Name != "eth0" || ev.Carrier != CarrierUp {
			t.Errorf("event = %+v, want eth0/up", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no LinkEvent within 1s; subscribe→runLoop wiring broken")
	}

	cancel()
	if err := <-exited; !errors.Is(err, context.Canceled) {
		t.Errorf("subscriber returned %v, want context.Canceled after cancel", err)
	}
	if _, ok := <-exited; ok {
		t.Error("terminal error channel did not close")
	}
}

// TestLinkSubscriberStartSurfacesErrorCallbackCause: when the
// netlink receive goroutine dies it reports the cause through
// ErrorCallback and closes the update channel. The subscriber must
// surface that concrete cause, %w-chained — not a bare "channel
// closed".
func TestLinkSubscriberStartSurfacesErrorCallbackCause(t *testing.T) {
	t.Parallel()
	want := errors.New("netlink: receive failed: ENOBUFS")
	subscribe := func(ch chan<- netlink.LinkUpdate, _ <-chan struct{}, opts netlink.LinkSubscribeOptions) error {
		// Mimic linkSubscribeAt on a fatal Receive error: report the
		// cause via the callback, then close the update channel.
		opts.ErrorCallback(want)
		close(ch)
		return nil
	}
	_, exited, err := (&LinkSubscriber{}).StartWith(context.Background(), subscribe, emptyLinkList, unexpectedLinkByName(t), make(chan LinkEvent, 1))
	if err != nil {
		t.Fatal(err)
	}
	err = <-exited
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want ErrorCallback cause chained via %%w", err)
	}
	if !strings.Contains(err.Error(), "subscription ended") {
		t.Errorf("err = %q, want 'subscription ended' context", err.Error())
	}
}

// TestLinkSubscriberReadSnapshotWrapsListError: a netlink.LinkList
// failure must be wrapped with the `rtnl: LinkList:` prefix so logs
// name the layer responsible.
func TestLinkSubscriberReadSnapshotWrapsListError(t *testing.T) {
	t.Parallel()
	want := errors.New("netlink: link list denied")
	listFn := func() ([]netlink.Link, error) { return nil, want }

	_, err := (&LinkSubscriber{}).readSnapshot(listFn, map[string]LinkState{})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want list-error chained via %%w", err)
	}
	if !strings.Contains(err.Error(), "rtnl: LinkList") {
		t.Errorf("err = %q, want 'rtnl: LinkList' prefix", err.Error())
	}
}

// TestLinkSubscriberReadSnapshotEmitsWatchedLinks: priming must return one
// LinkEvent per watched interface LinkList returns — carrier and
// operstate decoded from RawFlags — and drop interfaces outside the
// watched set.
func TestLinkSubscriberReadSnapshotEmitsWatchedLinks(t *testing.T) {
	t.Parallel()
	listFn := func() ([]netlink.Link, error) {
		return []netlink.Link{
			mkLink("eth0", unix.IFF_LOWER_UP, netlink.OperUp),
			mkLink("lo", unix.IFF_LOWER_UP, netlink.OperUp),
		}, nil
	}
	s := &LinkSubscriber{Interfaces: map[string]struct{}{"eth0": {}}}
	snapshot, err := s.readSnapshot(listFn, map[string]LinkState{})
	if err != nil {
		t.Fatalf("readSnapshot = %v, want nil", err)
	}
	if got := len(snapshot); got != 1 {
		t.Fatalf("len(snapshot) = %d, want 1 (eth0 only; lo filtered)", got)
	}
	ev := snapshot[0]
	if ev.Name != "eth0" || ev.Carrier != CarrierUp || ev.Operstate != OperstateUp {
		t.Errorf("event = %+v, want eth0/up/up", ev)
	}
}

func emptyLinkList() ([]netlink.Link, error) { return nil, nil }

// unexpectedLinkByName fails the test if the subscriber reads a link.
func unexpectedLinkByName(t testing.TB) linkByNameFn {
	t.Helper()
	return func(name string) (netlink.Link, error) {
		t.Errorf("unexpected LinkByName(%q)", name)
		return nil, errors.New("unexpected LinkByName")
	}
}

func TestTranslateSubClose(t *testing.T) {
	t.Parallel()
	cause := errors.New("netlink: ENOBUFS")
	withCause := func() *atomic.Pointer[error] {
		var p atomic.Pointer[error]
		p.Store(&cause)
		return &p
	}

	// Sentinel + captured cause → cause surfaced, %w-chained.
	got := translateSubClose(errSubscriptionClosed, withCause(), "link")
	if !errors.Is(got, cause) || !strings.Contains(got.Error(), "link subscription ended") {
		t.Errorf("with cause: got %v, want wrapped %v", got, cause)
	}

	// Sentinel + no captured cause → sentinel passes through.
	if got := translateSubClose(errSubscriptionClosed, &atomic.Pointer[error]{}, "link"); !errors.Is(got, errSubscriptionClosed) {
		t.Errorf("no cause: got %v, want errSubscriptionClosed", got)
	}

	// Non-sentinel error → passes through untouched.
	other := errors.New("some other error")
	if got := translateSubClose(other, withCause(), "route"); !errors.Is(got, other) {
		t.Errorf("non-sentinel: got %v, want %v", got, other)
	}
}

// TestLinkSubscriberStartCapturesCarrierDuringSnapshot: a carrier
// change that lands just after the link dump must still reach out,
// after the snapshot that Start returns. Only a subscription opened
// before the dump catches it.
func TestLinkSubscriberStartCapturesCarrierDuringSnapshot(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var updates chan<- netlink.LinkUpdate
	subscribe := func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, _ netlink.LinkSubscribeOptions) error {
		updates = ch
		closeLinkUpdatesOnStop(ch, done)
		return nil
	}
	current := mkLink("wan1", 0, netlink.OperDown)
	listFn := func() ([]netlink.Link, error) {
		snapshot := []netlink.Link{current}
		// wan1 gains carrier just after the dump reads it as down.
		current = mkLink("wan1", unix.IFF_LOWER_UP, netlink.OperUp)
		if updates != nil {
			updates <- mkUpdate("wan1", unix.IFF_LOWER_UP, netlink.OperUp)
		}
		return snapshot, nil
	}
	getFn := func(string) (netlink.Link, error) { return current, nil }
	s := &LinkSubscriber{Interfaces: map[string]struct{}{"wan1": {}}}
	out := make(chan LinkEvent, 1)
	snapshot, exited, err := s.StartWith(ctx, subscribe, listFn, getFn, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("subscriber did not stop after cancellation")
		}
	})
	if len(snapshot) != 1 || snapshot[0].Name != "wan1" || snapshot[0].Carrier != CarrierDown {
		t.Errorf("snapshot = %+v, want wan1/down", snapshot)
	}
	select {
	case ev := <-out:
		if ev.Name != "wan1" || ev.Carrier != CarrierUp {
			t.Errorf("event = %+v, want wan1/up", ev)
		}
	case <-ctx.Done():
		t.Fatal("no wan1/up event: carrier change during subscriber startup was missed")
	}
}

// Model the library's channel closure when its subscription socket closes.
func closeLinkUpdatesOnStop(ch chan<- netlink.LinkUpdate, done <-chan struct{}) {
	go func() {
		<-done
		close(ch)
	}()
}

// TestLinkSubscriberStartReconcilesQueuedChanges: notifications
// queued before the snapshot describe history it may already
// include. Each must be read as the link's current state and
// deduplicated against the snapshot that Start returns, so stale
// history never reaches out as a transient flap, while a change the
// snapshot missed still does, in notification order across
// interfaces.
func TestLinkSubscriberStartReconcilesQueuedChanges(t *testing.T) {
	t.Parallel()
	up := func(name string) netlink.Link { return mkLink(name, unix.IFF_LOWER_UP, netlink.OperUp) }
	down := func(name string) netlink.Link { return mkLink(name, 0, netlink.OperDown) }
	for _, tc := range []struct {
		name         string
		snapshot     []netlink.Link
		current      []netlink.Link // links that exist after the snapshot
		queued       []netlink.LinkUpdate
		wantSnapshot []string // the snapshot Start returns
		wantLive     []string // the events queued on out afterwards
	}{
		{
			name:         "flap before snapshot",
			snapshot:     []netlink.Link{up("wan0")},
			current:      []netlink.Link{up("wan0")},
			queued:       []netlink.LinkUpdate{mkUpdate("wan0", 0, netlink.OperDown), mkUpdate("wan0", unix.IFF_LOWER_UP, netlink.OperUp)},
			wantSnapshot: []string{"wan0 up/up"},
		},
		{
			name:         "carrier lost after snapshot",
			snapshot:     []netlink.Link{up("wan0")},
			current:      []netlink.Link{down("wan0")},
			queued:       []netlink.LinkUpdate{mkUpdate("wan0", 0, netlink.OperDown)},
			wantSnapshot: []string{"wan0 up/up"},
			wantLive:     []string{"wan0 down/down"},
		},
		{
			name:         "carrier gained after snapshot",
			snapshot:     []netlink.Link{down("wan0")},
			current:      []netlink.Link{up("wan0")},
			queued:       []netlink.LinkUpdate{mkUpdate("wan0", 0, netlink.OperDown), mkUpdate("wan0", unix.IFF_LOWER_UP, netlink.OperUp)},
			wantSnapshot: []string{"wan0 down/down"},
			wantLive:     []string{"wan0 up/up"},
		},
		{
			name:         "re-created before snapshot",
			snapshot:     []netlink.Link{up("wan0")},
			current:      []netlink.Link{up("wan0")},
			queued:       []netlink.LinkUpdate{mkDelete("wan0"), mkUpdate("wan0", unix.IFF_LOWER_UP, netlink.OperUp)},
			wantSnapshot: []string{"wan0 up/up"},
		},
		{
			name:         "deleted after snapshot",
			snapshot:     []netlink.Link{up("wan0")},
			queued:       []netlink.LinkUpdate{mkUpdate("wan0", 0, netlink.OperDown), mkDelete("wan0")},
			wantSnapshot: []string{"wan0 up/up"},
			wantLive:     []string{"wan0 down/notpresent"},
		},
		{
			// vm-recovery: wan1 gains carrier just after the dump, then
			// wan0 loses carrier.
			name:         "carrier moves to another link after snapshot",
			snapshot:     []netlink.Link{up("wan0"), down("wan1")},
			current:      []netlink.Link{down("wan0"), up("wan1")},
			queued:       []netlink.LinkUpdate{mkUpdate("wan1", unix.IFF_LOWER_UP, netlink.OperUp), mkUpdate("wan0", 0, netlink.OperDown)},
			wantSnapshot: []string{"wan0 up/up", "wan1 down/down"},
			wantLive:     []string{"wan1 up/up", "wan0 down/down"},
		},
		{
			name:         "flap on one link before snapshot",
			snapshot:     []netlink.Link{up("wan0"), up("wan1")},
			current:      []netlink.Link{up("wan0"), up("wan1")},
			queued:       []netlink.LinkUpdate{mkUpdate("wan0", 0, netlink.OperDown), mkUpdate("wan0", unix.IFF_LOWER_UP, netlink.OperUp)},
			wantSnapshot: []string{"wan0 up/up", "wan1 up/up"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var updates chan<- netlink.LinkUpdate
			subscribe := func(ch chan<- netlink.LinkUpdate, _ <-chan struct{}, _ netlink.LinkSubscribeOptions) error {
				updates = ch
				return nil
			}
			listFn := func() ([]netlink.Link, error) {
				for _, upd := range tc.queued {
					updates <- upd
				}
				close(updates)
				return tc.snapshot, nil
			}
			getFn := func(name string) (netlink.Link, error) {
				for _, link := range tc.current {
					if link.Attrs().Name == name {
						return link, nil
					}
				}
				return nil, netlink.LinkNotFoundError{}
			}
			s := &LinkSubscriber{Interfaces: map[string]struct{}{"wan0": {}, "wan1": {}}}
			out := make(chan LinkEvent, len(tc.queued))
			snapshot, exited, err := s.StartWith(ctx, subscribe, listFn, getFn, out)
			if err != nil {
				t.Fatal(err)
			}
			if got := linkEventStrings(snapshot); !slices.Equal(got, tc.wantSnapshot) {
				t.Errorf("snapshot = %v, want %v", got, tc.wantSnapshot)
			}
			select {
			case err := <-exited:
				if !errors.Is(err, errSubscriptionClosed) {
					t.Fatalf("subscriber = %v, want subscription closed", err)
				}
			case <-ctx.Done():
				t.Fatal("subscriber did not finish draining queued changes")
			}
			var live []LinkEvent
			for len(out) > 0 {
				live = append(live, <-out)
			}
			if got := linkEventStrings(live); !slices.Equal(got, tc.wantLive) {
				t.Errorf("live events = %v, want %v", got, tc.wantLive)
			}
		})
	}
}

// linkEventStrings renders events as "<name> <carrier>/<operstate>".
func linkEventStrings(events []LinkEvent) []string {
	var out []string
	for _, ev := range events {
		out = append(out, ev.Name+" "+ev.Carrier.String()+"/"+ev.Operstate.String())
	}
	return out
}

// TestLinkSubscriberStartReturnsSnapshotLargerThanOut: Start returns
// the snapshot instead of queuing it, so a dump with more watched
// links than out can hold cannot block startup before a consumer
// reads out.
func TestLinkSubscriberStartReturnsSnapshotLargerThanOut(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	subscribe := func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, _ netlink.LinkSubscribeOptions) error {
		closeLinkUpdatesOnStop(ch, done)
		return nil
	}
	var links []netlink.Link
	for i := range 100 {
		links = append(links, mkLink(fmt.Sprintf("wan%d", i), unix.IFF_LOWER_UP, netlink.OperUp))
	}
	listFn := func() ([]netlink.Link, error) { return links, nil }
	started := make(chan []LinkEvent, 1)
	go func() {
		snapshot, _, err := (&LinkSubscriber{}).StartWith(ctx, subscribe, listFn, unexpectedLinkByName(t), make(chan LinkEvent))
		if err != nil {
			t.Error(err)
		}
		started <- snapshot
	}()
	select {
	case snapshot := <-started:
		if len(snapshot) != len(links) {
			t.Errorf("len(snapshot) = %d, want %d", len(snapshot), len(links))
		}
	case <-ctx.Done():
		t.Fatal("Start blocked on a snapshot larger than out")
	}
}

func TestLinkSubscriberStartClosesSubscriptionOnSnapshotFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("link snapshot failed")
	var stopped <-chan struct{}
	subscribe := func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, _ netlink.LinkSubscribeOptions) error {
		stopped = done
		closeLinkUpdatesOnStop(ch, done)
		return nil
	}
	listFn := func() ([]netlink.Link, error) { return nil, want }
	_, exited, err := (&LinkSubscriber{}).StartWith(context.Background(), subscribe, listFn, unexpectedLinkByName(t), make(chan LinkEvent, 1))
	if !errors.Is(err, want) || exited != nil {
		t.Fatalf("Start = %v, %v; want no worker and wrapped snapshot failure", exited, err)
	}
	select {
	case <-stopped:
	default:
		t.Error("subscription left open after snapshot failure")
	}
}

func TestLinkSubscriberStartCancelsBlockedDelivery(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"snapshot", "live"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			link := mkLink("eth0", unix.IFF_LOWER_UP, netlink.OperUp)
			var stopped <-chan struct{}
			subscribe := func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, _ netlink.LinkSubscribeOptions) error {
				stopped = done
				ch <- mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp)
				closeLinkUpdatesOnStop(ch, done)
				return nil
			}
			listFn := func() ([]netlink.Link, error) {
				if phase == "snapshot" {
					cancel()
					return []netlink.Link{link}, nil
				}
				return nil, nil
			}
			getFn := func(string) (netlink.Link, error) { return link, nil }
			out := make(chan LinkEvent) // No consumer: every event send blocks.
			_, exited, err := (&LinkSubscriber{}).StartWith(ctx, subscribe, listFn, getFn, out)
			if phase == "snapshot" {
				if !errors.Is(err, context.Canceled) || exited != nil {
					t.Fatalf("Start = %v, %v; want no worker and context.Canceled", exited, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				select {
				case err := <-exited:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("subscriber = %v, want context.Canceled", err)
					}
				case <-time.After(time.Second):
					t.Fatal("subscriber did not cancel while delivery was blocked")
				}
			}
			select {
			case <-stopped:
			default:
				t.Error("subscription left open after cancellation")
			}
			select {
			case <-out:
				t.Error("caller-owned event channel closed")
			default:
			}
		})
	}
}

func TestLinkSubscriberStartSnapshotFailureUnblocksReceiver(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"list error", "cancellation"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := errors.New("link snapshot failed")
			if failure == "cancellation" {
				want = context.Canceled
			}
			finished := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			subscribe := func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, _ netlink.LinkSubscribeOptions) error {
				for range cap(ch) {
					ch <- mkUpdate("eth0", 0, netlink.OperDown)
				}
				go func() {
					defer close(finished)
					defer close(ch)
					// The vendored receiver cannot check done while sending
					// a batch of messages to a full update channel.
					select {
					case ch <- mkUpdate("eth0", 0, netlink.OperDown):
					case <-release: // Release the fake receiver if this test fails.
					}
					<-done
				}()
				return nil
			}
			listFn := func() ([]netlink.Link, error) {
				if failure == "cancellation" {
					cancel()
					return []netlink.Link{mkLink("eth0", unix.IFF_LOWER_UP, netlink.OperUp)}, nil
				}
				return nil, want
			}
			_, _, err := (&LinkSubscriber{}).StartWith(ctx, subscribe, listFn, unexpectedLinkByName(t), make(chan LinkEvent))
			if !errors.Is(err, want) {
				t.Fatalf("Start = %v, want %v", err, want)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("netlink receiver stayed blocked after snapshot failure")
			}
		})
	}
}

func TestLinkSubscriberReadFailureUnblocksReceiver(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	want := errors.New("link read failed")
	finished := make(chan struct{})
	subscribe := func(ch chan<- netlink.LinkUpdate, done <-chan struct{}, _ netlink.LinkSubscribeOptions) error {
		for range cap(ch) {
			ch <- mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp)
		}
		go func() {
			defer close(finished)
			defer close(ch)
			// Two sends keep the receiver blocked after runLoop takes
			// the first update and the current-link read fails.
			for range 2 {
				select {
				case ch <- mkUpdate("eth0", unix.IFF_LOWER_UP, netlink.OperUp):
				case <-ctx.Done():
					return
				}
			}
			<-done
		}()
		return nil
	}
	getFn := func(string) (netlink.Link, error) { return nil, want }
	out := make(chan LinkEvent, 1)
	_, exited, err := (&LinkSubscriber{}).StartWith(ctx, subscribe, emptyLinkList, getFn, out)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if !errors.Is(err, want) || !strings.Contains(err.Error(), "rtnl: LinkByName eth0") {
			t.Errorf("subscriber = %v; want wrapped %v", err, want)
		}
	case <-ctx.Done():
		t.Fatal("subscriber stayed blocked after link read failure")
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("netlink receiver stayed blocked after link read failure")
	}
	if len(out) != 0 {
		t.Error("failed link read emitted a LinkEvent")
	}
}
