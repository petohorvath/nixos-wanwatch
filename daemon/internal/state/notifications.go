package state

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
)

// HookNotifier delivers best-effort notifications in submission order.
// Notify never waits for scripts to finish: a full queue drops the newest
// event. Cancellation stops in-flight scripts and discards queued events.
// Call Close to finish delivery before releasing the notifier.
type HookNotifier struct {
	parent  context.Context
	run     func(context.Context, HookContext) []hookResult
	metrics *metrics.Registry
	logger  *slog.Logger

	mu     sync.Mutex // serializes submission with Close, never script execution
	jobs   chan HookContext
	done   chan struct{}
	closed bool
}

const (
	hookQueueCapacity = 32
	maxHooksPerEvent  = 8
)

// NewHookNotifier binds delivery to the daemon's lifetime context. A zero
// timeout uses DefaultHookTimeout. Construction performs no I/O and starts
// no goroutines; the worker starts on the first accepted notification.
func NewHookNotifier(parent context.Context, dir string, timeout time.Duration, mreg *metrics.Registry, logger *slog.Logger) *HookNotifier {
	runner := &hookRunner{Dir: dir, Timeout: timeout, MaxHooks: maxHooksPerEvent}
	return newHookNotifier(parent, runner.run, mreg, logger)
}

// Execution is an internal seam: production runs scripts, delivery tests
// substitute a deterministic function without building a daemon.
func newHookNotifier(parent context.Context, run func(context.Context, HookContext) []hookResult, mreg *metrics.Registry, logger *slog.Logger) *HookNotifier {
	return &HookNotifier{parent: parent, run: run, metrics: mreg, logger: logger}
}

// Notify captures a copy of the Decision data and reports whether it was
// accepted. A zero Timestamp is stamped at submission, before queueing.
// Calls from the event loop preserve Decision order. Notify and Close may
// run concurrently; after cancellation or Close, Notify returns false.
func (n *HookNotifier) Notify(hookCtx HookContext) bool {
	n.mu.Lock()
	if n.closed || n.parent.Err() != nil {
		n.mu.Unlock()
		return false
	}
	if n.jobs == nil {
		n.jobs = make(chan HookContext, hookQueueCapacity)
		n.done = make(chan struct{})
		go n.deliver()
	}
	hookCtx.Families = slices.Clone(hookCtx.Families)
	if hookCtx.Timestamp.IsZero() {
		hookCtx.Timestamp = time.Now().UTC()
	}
	select {
	case n.jobs <- hookCtx:
		n.mu.Unlock()
		return true
	default:
		n.mu.Unlock()
		n.logger.Warn("hook event dropped: queue full", "event", string(hookCtx.Event), "capacity", hookQueueCapacity)
		return false
	}
}

// Close stops accepting notifications and waits for accepted events to
// finish. If the lifetime context is cancelled, pending events are discarded
// and running scripts are killed instead. Repeated calls are safe, including
// when no notification was submitted.
func (n *HookNotifier) Close() {
	n.mu.Lock()
	if !n.closed {
		n.closed = true
		if n.jobs != nil {
			close(n.jobs)
		}
	}
	done := n.done
	n.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (n *HookNotifier) deliver() {
	defer close(n.done)
	for {
		select {
		case <-n.parent.Done():
			return
		case hookCtx, ok := <-n.jobs:
			if !ok || n.parent.Err() != nil {
				return
			}
			n.execute(hookCtx)
		}
	}
}

func (n *HookNotifier) execute(hookCtx HookContext) {
	for _, r := range n.run(n.parent, hookCtx) {
		if r.Skipped {
			n.logger.Warn("hook skipped: per-event limit reached",
				"event", string(hookCtx.Event), "hook", r.Path, "limit", maxHooksPerEvent)
			continue
		}
		result := "ok"
		switch {
		case r.TimedOut:
			result = "timeout"
		case r.ExitCode != 0:
			result = "nonzero"
		}
		n.metrics.HookInvocations.WithLabelValues(string(hookCtx.Event), result).Inc()
		if result != "ok" {
			n.logger.Warn("hook failed",
				"event", string(hookCtx.Event), "hook", r.Path, "result", result,
				"exitCode", r.ExitCode, "err", r.Err, "output", r.Output)
		}
	}
}
