package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"testing/synctest"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
)

// queue leaves v waiting in ch, as an event buffered in a subscriber
// channel at shutdown: its own goroutine sends it and gives up once
// serve has returned. synctest.Wait proves the send is pending.
func queue[T any](h *harness, ch chan<- T, v T) {
	go func() {
		select {
		case ch <- v:
		case <-h.done:
		}
	}()
}

// busyWithRetry leaves the event loop inside a handler until the
// daemon context is cancelled. backup is active and primary's switch is
// pending on a failed ifindex lookup; primary's next partial Window
// retries it, and that lookup fails again and holds. The handler then
// returns without another effect or State write, so whatever follows
// Stop belongs to the queued events. Call it inside a synctest bubble.
func busyWithRetry(h *harness) {
	h.tb.Helper()
	cachedGateways(h)
	h.Link(carrier("wwan0", true))
	h.kernel.fail(opIfIndex, "eth0")
	h.Link(carrier("eth0", true))
	h.Trace()

	h.kernel.hold(opIfIndex, "eth0", h.ctx.Done())
	send(h, h.probes, probe.ProbeResult{Wan: "primary", Family: probe.FamilyV4})
	synctest.Wait() // the retry's lookup has failed and holds
	// The queued events reach Apply for primary if they run.
	h.kernel.heal(opIfIndex, "eth0")
}

// applyRouteErrors sums apply_route_errors_total over Group home.
func applyRouteErrors(tb testing.TB, h *harness) float64 {
	tb.Helper()
	return readCounter(tb, h.metrics.ApplyRouteErrors.WithLabelValues("home", "v4")) +
		readCounter(tb, h.metrics.ApplyRouteErrors.WithLabelValues("home", "v6"))
}

// readStateFile returns state.json byte for byte, so any rewrite shows.
func readStateFile(tb testing.TB, h *harness) []byte {
	tb.Helper()
	data, err := os.ReadFile(h.statePath)
	if err != nil {
		tb.Fatalf("read State: %v", err)
	}
	return data
}

// TestStopDropsQueuedEvents pins the stop sequence: once the daemon
// context is cancelled, the event loop dispatches no queued event, so
// shutdown applies nothing, publishes no State and fails no route
// write. Each queued event would otherwise drive Apply with the
// cancelled context: the route event retries primary's pending switch,
// the others move the Decision to backup.
func TestStopDropsQueuedEvents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		queue func(*harness)
	}{
		{"probe result", func(h *harness) {
			queue(h, h.probes, lossy("primary", probe.FamilyV4))
		}},
		{"link event", func(h *harness) {
			queue(h, h.links, carrier("eth0", false))
		}},
		{"route event", func(h *harness) {
			queue(h, h.routes, gateway("eth0", probe.FamilyV6, "2001:db8::2"))
		}},
		{"one of each", func(h *harness) {
			queue(h, h.probes, lossy("primary", probe.FamilyV4))
			queue(h, h.links, carrier("eth0", false))
			queue(h, h.routes, gateway("eth0", probe.FamilyV6, "2001:db8::2"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				h := startHarness(t, baseConfig())
				busyWithRetry(h)
				tc.queue(h)
				synctest.Wait() // every queued send is pending

				assertTrace(t, "busy retry", h.Trace(), []string{"ifindex eth0 ✗"})
				stateBefore := readStateFile(t, h)
				errorsBefore := applyRouteErrors(t, h)

				h.Stop()

				assertTrace(t, "Stop", h.Trace(), []string{"hooks closed"})
				if got := readStateFile(t, h); !bytes.Equal(got, stateBefore) {
					t.Errorf("State rewritten during shutdown:\nbefore: %s\nafter:  %s", stateBefore, got)
				}
				if got := applyRouteErrors(t, h); got != errorsBefore {
					t.Errorf("apply_route_errors_total{home} = %v across Stop, want %v", got, errorsBefore)
				}
			})
		})
	}
}

// TestStopEndsInFlightHandler pins shutdown inside a handler: once the
// daemon context is cancelled, the handler that is already running
// applies nothing more, to its own Group or the next, counts no route
// error, and publishes no State. primary's carrier moves both Groups
// onto it; the first Group's ifindex lookup holds until cancellation.
// The hold is on the lookup, not the route write: a write still blocked
// at cancellation fails on the cancelled context and counts its error
// before any guard can run.
func TestStopEndsInFlightHandler(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		cfg := baseConfig()
		cfg.Groups["guest"] = selector.Group{
			Name: "guest", Strategy: "primary-backup", Table: 200, Mark: 0x200,
			Members: cfg.Groups["home"].Members,
		}
		h := startHarness(t, cfg)
		cachedGateways(h)
		h.Trace()
		stateBefore := readStateFile(t, h)
		errorsBefore := applyRouteErrorsIn(t, h, "home", "guest")

		h.kernel.hold(opIfIndex, "eth0", h.ctx.Done())
		send(h, h.links, carrier("eth0", true))
		synctest.Wait() // the first Group's lookup holds

		h.Stop()

		assertTrace(t, "Stop", h.Trace(), []string{"hooks closed"})
		if got := readStateFile(t, h); !bytes.Equal(got, stateBefore) {
			t.Errorf("State rewritten during shutdown:\nbefore: %s\nafter:  %s", stateBefore, got)
		}
		if got := applyRouteErrorsIn(t, h, "home", "guest"); got != errorsBefore {
			t.Errorf("apply_route_errors_total = %v across Stop, want %v", got, errorsBefore)
		}
	})
}

// TestHandlersStopOnceCancelled: a handler that finds the daemon
// context cancelled, as one still running at shutdown does for the
// Groups after the first, makes no Decision and publishes nothing.
// Group guest has only primary, so losing primary would commit its
// down without any Apply, the path a route-write guard alone misses;
// a changed Gateway would republish State.
func TestHandlersStopOnceCancelled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		handle func(*daemon, context.Context)
	}{
		{"link event", func(d *daemon, ctx context.Context) { d.handleLinkEvent(ctx, carrier("eth0", false)) }},
		{"probe result", func(d *daemon, ctx context.Context) { d.handleProbeResult(ctx, lossy("primary", probe.FamilyV4)) }},
		{"route event", func(d *daemon, ctx context.Context) {
			d.handleRouteEvent(ctx, gateway("eth0", probe.FamilyV4, "192.0.2.99"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig()
			cfg.Groups["guest"] = selector.Group{
				Name: "guest", Strategy: "primary-backup", Table: 200, Mark: 0x200,
				Members: map[string]selector.Member{"primary": {Wan: "primary", Priority: 1}},
			}
			h := newHarness(t, cfg)
			if err := h.d.bootstrap(h.ctx); err != nil {
				t.Fatal(err)
			}
			h.kernel.setAddrs("eth0", "192.0.2.10", "2001:db8::10")
			h.d.prime(h.ctx, startupSnapshot{
				links: []rtnl.LinkEvent{carrier("eth0", true), carrier("wwan0", true)},
				routes: []rtnl.RouteEvent{
					gateway("eth0", probe.FamilyV4, "192.0.2.1"),
					gateway("eth0", probe.FamilyV6, "2001:db8::1"),
					gateway("wwan0", probe.FamilyV4, "198.51.100.1"),
					gateway("wwan0", probe.FamilyV6, "2001:db8:1::1"),
				},
			})
			h.Trace()
			stateBefore := readStateFile(t, h)
			decisionsBefore := map[string]int{}
			for name, g := range h.d.groups {
				decisionsBefore[name] = g.Snapshot().DecisionsTotal
			}

			h.cancel()
			tc.handle(h.d, h.ctx)

			assertTrace(t, tc.name, h.Trace(), nil)
			if got := readStateFile(t, h); !bytes.Equal(got, stateBefore) {
				t.Errorf("State rewritten once cancelled:\nbefore: %s\nafter:  %s", stateBefore, got)
			}
			for name, g := range h.d.groups {
				if got := g.Snapshot().DecisionsTotal; got != decisionsBefore[name] {
					t.Errorf("Group %s decisionsTotal = %d once cancelled, want %d", name, got, decisionsBefore[name])
				}
			}
		})
	}
}

// applyRouteErrorsIn sums apply_route_errors_total over groups.
func applyRouteErrorsIn(tb testing.TB, h *harness, groups ...string) float64 {
	tb.Helper()
	var sum float64
	for _, g := range groups {
		sum += readCounter(tb, h.metrics.ApplyRouteErrors.WithLabelValues(g, "v4")) +
			readCounter(tb, h.metrics.ApplyRouteErrors.WithLabelValues(g, "v6"))
	}
	return sum
}

// TestPrimesNothingOnceCancelled: a daemon cancelled before priming
// primes nothing from its startup snapshots, as the event loop
// dispatches nothing once cancelled. Priming would otherwise drive
// Apply with the cancelled context and publish State during shutdown.
func TestPrimesNothingOnceCancelled(t *testing.T) {
	t.Parallel()
	h := newHarness(t, baseConfig())
	if err := h.d.bootstrap(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.Trace()
	stateBefore := readStateFile(t, h)

	h.cancel()
	h.d.prime(h.ctx, startupSnapshot{
		links:  []rtnl.LinkEvent{carrier("eth0", true)},
		routes: []rtnl.RouteEvent{gateway("eth0", probe.FamilyV4, "192.0.2.1")},
	})
	serve(h.ctx, h.d, h.probes, h.links, h.routes, h.challenges)

	assertTrace(t, "prime and serve", h.Trace(), []string{"hooks closed"})
	if got := readStateFile(t, h); !bytes.Equal(got, stateBefore) {
		t.Errorf("State rewritten during shutdown:\nbefore: %s\nafter:  %s", stateBefore, got)
	}
}

// TestSubsystemExitStopsServe: a subsystem that fails while the daemon
// runs ends the event loop, and the daemon exits non-zero so systemd
// restarts it, as run() does with each prober, subscriber, and the
// metrics server.
func TestSubsystemExitStopsServe(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, baseConfig())
		ctx, cancel := context.WithCancelCause(h.ctx)
		defer cancel(nil)
		exited := make(chan error)
		subsystemDone := superviseSubsystem(ctx, cancel, h.d.logger, "route subscriber", func() error { return <-exited })
		served := make(chan struct{})
		go func() {
			defer close(served)
			serve(ctx, h.d, h.probes, h.links, h.routes, h.challenges)
		}()

		errOverrun := errors.New("ENOBUFS")
		exited <- errOverrun
		synctest.Wait()

		select {
		case <-served:
		default:
			t.Fatal("serve still running after a subsystem failed")
		}
		if err := exitError(ctx, subsystemDone, h.d.logger); !errors.Is(err, errOverrun) {
			t.Errorf("exitError = %v, want %%w-wrapped %v", err, errOverrun)
		}
	})
}
