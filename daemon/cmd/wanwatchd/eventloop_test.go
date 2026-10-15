package main

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
)

// startEventLoop runs eventLoop for a harness daemon on its own
// goroutine with fresh unbuffered channels. It returns the challenge
// channel, the cancel of the loop's context, and a channel closed when
// eventLoop returns. Call it inside a synctest bubble, so a loop that
// never returns or never acknowledges fails the test as a deadlock
// instead of hanging it.
func startEventLoop(tb testing.TB) (chan<- watchdogChallenge, context.CancelFunc, <-chan struct{}) {
	tb.Helper()
	h := newHarness(tb, baseConfig())
	ctx, cancel := context.WithCancel(tb.Context())
	challenges := make(chan watchdogChallenge)
	done := make(chan struct{})
	go func() {
		defer close(done)
		eventLoop(ctx, h.d,
			make(chan probe.ProbeResult), make(chan rtnl.LinkEvent), make(chan rtnl.RouteEvent),
			challenges)
	}()
	return challenges, cancel, done
}

func TestEventLoopReturnsOnCancel(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		_, cancel, done := startEventLoop(t)

		synctest.Wait()
		select {
		case <-done:
			t.Fatal("eventLoop returned before its context was cancelled")
		default:
		}

		cancel()
		<-done
	})
}

func TestEventLoopAcknowledgesWatchdogChallenge(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		challenges, cancel, done := startEventLoop(t)

		ack := make(chan struct{})
		challenges <- watchdogChallenge{ack: ack}
		<-ack

		cancel()
		<-done
	})
}
