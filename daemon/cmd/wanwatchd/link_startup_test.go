package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// TestLinkStartupSelectsFromCurrentCarrier drives the link
// subscriber's startup into the daemon. Carrier changes queued while
// the snapshot is taken must reach Selection, and queued history the
// snapshot already includes must not flap it.
func TestLinkStartupSelectsFromCurrentCarrier(t *testing.T) {
	t.Parallel()
	up := func(name string) netlink.Link {
		return &netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: name, RawFlags: unix.IFF_LOWER_UP, OperState: netlink.OperUp}}
	}
	down := func(name string) netlink.Link {
		return &netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: name, OperState: netlink.OperDown}}
	}
	for _, tc := range []struct {
		name     string
		snapshot []netlink.Link
		current  []netlink.Link
		queued   []netlink.Link
		want     []string // Selection after each LinkEvent
	}{
		{
			// vm-recovery: the backup gains carrier just after the dump,
			// then the primary loses carrier.
			name:     "backup gains carrier during snapshot",
			snapshot: []netlink.Link{up("eth0"), down("wwan0")},
			current:  []netlink.Link{down("eth0"), up("wwan0")},
			queued:   []netlink.Link{up("wwan0"), down("eth0")},
			want:     []string{"primary", "primary", "primary", "backup"},
		},
		{
			name:     "primary flap before snapshot",
			snapshot: []netlink.Link{up("eth0"), up("wwan0")},
			current:  []netlink.Link{up("eth0"), up("wwan0")},
			queued:   []netlink.Link{down("eth0"), up("eth0")},
			want:     []string{"primary", "primary"},
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
			listLinks := func() ([]netlink.Link, error) {
				for _, link := range tc.queued {
					upd := netlink.LinkUpdate{Header: unix.NlMsghdr{Type: unix.RTM_NEWLINK}, Link: link}
					upd.Flags = link.Attrs().RawFlags
					updates <- upd
				}
				close(updates)
				return tc.snapshot, nil
			}
			getLink := func(name string) (netlink.Link, error) {
				for _, link := range tc.current {
					if link.Attrs().Name == name {
						return link, nil
					}
				}
				return nil, netlink.LinkNotFoundError{}
			}
			cfg := testCfgWithGroup()
			s := &rtnl.LinkSubscriber{Interfaces: watchedInterfaces(cfg)}
			events := make(chan rtnl.LinkEvent, 8)
			exited, err := s.StartWith(ctx, subscribe, listLinks, getLink, events)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-exited:
			case <-ctx.Done():
				t.Fatal("subscriber did not finish processing startup history")
			}

			d := testDaemon(t, cfg)
			var got []string
			for len(events) > 0 {
				d.handleLinkEvent(t.Context(), <-events)
				active := "<none>"
				if a := readPublishedState(t, d).Groups["home"].Active; a != nil {
					active = *a
				}
				got = append(got, active)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Selections = %v, want %v", got, tc.want)
			}
		})
	}
}
