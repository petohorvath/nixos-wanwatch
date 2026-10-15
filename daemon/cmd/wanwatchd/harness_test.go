package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

// harness drives one daemon through the production bootstrap, event
// loop and stop sequence. Events go over unbuffered channels, and each
// is followed by a watchdog challenge whose ack proves the handler
// finished, so every step is synchronous without sleeps or polling.
//
// Kernel effects and Hook notifications land in one ordered trace;
// State is read back from state.json.
type harness struct {
	tb        testing.TB
	d         *daemon
	kernel    *fakeKernel
	metrics   *metrics.Registry
	recorder  *hookRecorder
	trace     *trace
	statePath string

	ctx    context.Context
	cancel context.CancelFunc

	probes     chan probe.ProbeResult
	links      chan rtnl.LinkEvent
	routes     chan rtnl.RouteEvent
	challenges chan watchdogChallenge
	done       chan struct{} // closed when serve returns; nil before Start
	stopOnce   sync.Once
}

// newHarness builds a daemon for cfg without starting it. It points
// State and Hooks at fresh temporary directories, so cfg must not be
// shared with another harness or changed afterwards.
func newHarness(tb testing.TB, cfg *config.Config) *harness {
	tb.Helper()
	cfg.Global.StatePath = filepath.Join(tb.TempDir(), "state.json")
	cfg.Global.HooksDir = tb.TempDir()
	tr := &trace{}
	h := &harness{
		tb:         tb,
		kernel:     newFakeKernel(tr, cfg),
		metrics:    metrics.New(),
		recorder:   &hookRecorder{trace: tr},
		trace:      tr,
		statePath:  cfg.Global.StatePath,
		probes:     make(chan probe.ProbeResult),
		links:      make(chan rtnl.LinkEvent),
		routes:     make(chan rtnl.RouteEvent),
		challenges: make(chan watchdogChallenge),
	}
	h.ctx, h.cancel = context.WithCancel(tb.Context())
	tb.Cleanup(h.cancel)
	h.d = newDaemon(cfg, h.kernel, h.recorder, h.metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h
}

// Start runs bootstrap, then serve on its own goroutine, as run()
// does once the probers and subscribers are wired. The test's cleanup
// stops it.
func (h *harness) Start() error {
	if err := h.d.bootstrap(h.ctx); err != nil {
		return err
	}
	h.done = make(chan struct{})
	go func() {
		defer close(h.done)
		serve(h.ctx, h.d, h.probes, h.links, h.routes, h.challenges)
	}()
	h.tb.Cleanup(h.Stop)
	return nil
}

// startHarness builds and starts a harness, failing the test if
// bootstrap fails.
func startHarness(tb testing.TB, cfg *config.Config) *harness {
	tb.Helper()
	h := newHarness(tb, cfg)
	if err := h.Start(); err != nil {
		tb.Fatalf("harness start: %v", err)
	}
	return h
}

// Probe delivers one ProbeResult and waits until its handler finished.
func (h *harness) Probe(r probe.ProbeResult) {
	h.tb.Helper()
	send(h, h.probes, r)
	h.barrier()
}

// Link delivers one LinkEvent and waits until its handler finished.
func (h *harness) Link(e rtnl.LinkEvent) {
	h.tb.Helper()
	send(h, h.links, e)
	h.barrier()
}

// Route delivers one RouteEvent and waits until its handler finished.
func (h *harness) Route(e rtnl.RouteEvent) {
	h.tb.Helper()
	send(h, h.routes, e)
	h.barrier()
}

// Stop cancels the daemon context and waits for serve to return,
// which closes Hook delivery. Repeated calls are safe.
func (h *harness) Stop() {
	h.stopOnce.Do(func() {
		h.cancel()
		if h.done != nil {
			<-h.done
		}
	})
}

// Trace returns the kernel effects and Hook notifications recorded
// since the previous call, in order.
func (h *harness) Trace() []string {
	return h.trace.take()
}

// State reads the published state.json.
func (h *harness) State() state.State {
	h.tb.Helper()
	data, err := os.ReadFile(h.statePath)
	if err != nil {
		h.tb.Fatalf("read State: %v", err)
	}
	var s state.State
	if err := json.Unmarshal(data, &s); err != nil {
		h.tb.Fatalf("decode State: %v", err)
	}
	return s
}

// hooks returns every Hook notification accepted so far, with full
// Decision data.
func (h *harness) hooks() []state.HookContext {
	return h.recorder.accepted()
}

// barrier sends a watchdog challenge and waits for its ack. The event
// loop handles one event at a time, so the ack proves the handler of
// every earlier event has returned.
func (h *harness) barrier() {
	h.tb.Helper()
	ack := make(chan struct{})
	send(h, h.challenges, watchdogChallenge{ack: ack})
	select {
	case <-ack:
	case <-h.done:
		h.tb.Fatal("harness: serve stopped before acknowledging the barrier")
	}
}

// send hands v to the event loop, failing the test if serve is not
// running to receive it.
func send[T any](h *harness, ch chan<- T, v T) {
	h.tb.Helper()
	if h.done == nil {
		h.tb.Fatal("harness: event sent before Start")
	}
	select {
	case ch <- v:
	case <-h.done:
		h.tb.Fatal("harness: event sent after serve stopped")
	}
}

// baseConfig is the starting point of every harness test: primary on
// eth0 and backup on wwan0, both dual-stack, in Group home. Change it
// with plain Go before newHarness.
func baseConfig() *config.Config {
	probeCfg := func(v4, v6 string) config.Probe {
		return config.Probe{
			Method:             "icmp",
			Targets:            config.Targets{V4: []string{v4}, V6: []string{v6}},
			IntervalMs:         1000,
			TimeoutMs:          1000,
			WindowSize:         5,
			Thresholds:         config.Thresholds{LossPctDown: 20, LossPctUp: 10, RttMsDown: 200, RttMsUp: 100},
			Hysteresis:         config.Hysteresis{ConsecutiveUp: 1, ConsecutiveDown: 1},
			FamilyHealthPolicy: "all",
		}
	}
	return &config.Config{
		Schema: config.SupportedSchema,
		Wans: map[string]config.Wan{
			"primary": {Name: "primary", Interface: "eth0", Probe: probeCfg("198.51.100.53", "2001:db8:53::1")},
			"backup":  {Name: "backup", Interface: "wwan0", Probe: probeCfg("203.0.113.53", "2001:db8:53::2")},
		},
		Groups: map[string]selector.Group{
			"home": {
				Name:     "home",
				Strategy: "primary-backup",
				Table:    100,
				Mark:     0x100,
				Members: map[string]selector.Member{
					"primary": {Wan: "primary", Priority: 1},
					"backup":  {Wan: "backup", Priority: 2},
				},
			},
		},
	}
}

// healthy is a full Window that passes baseConfig's thresholds.
func healthy(wan string, fam probe.Family) probe.ProbeResult {
	return probe.ProbeResult{Wan: wan, Family: fam, Stats: probe.FamilyStats{
		RTTMicros: 10_000, WindowFilled: true,
	}}
}

// lossy is a full Window in which every Sample was lost.
func lossy(wan string, fam probe.Family) probe.ProbeResult {
	return probe.ProbeResult{Wan: wan, Family: fam, Stats: probe.FamilyStats{
		LossRatio: 1, WindowFilled: true,
	}}
}

// carrier reports iface's carrier and operstate both up or both down.
func carrier(iface string, up bool) rtnl.LinkEvent {
	if up {
		return rtnl.LinkEvent{Name: iface, Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp}
	}
	return rtnl.LinkEvent{Name: iface, Carrier: rtnl.CarrierDown, Operstate: rtnl.OperstateDown}
}

// gateway reports a default route on iface via ip; an empty ip is a
// scope-link route.
func gateway(iface string, fam probe.Family, ip string) rtnl.RouteEvent {
	return rtnl.RouteEvent{Op: rtnl.RouteEventAdd, Iface: iface, Family: rtnl.RouteFamily(fam), Gateway: net.ParseIP(ip)}
}

// gatewayGone reports that iface no longer has a default route.
func gatewayGone(iface string, fam probe.Family) rtnl.RouteEvent {
	return rtnl.RouteEvent{Op: rtnl.RouteEventDel, Iface: iface, Family: rtnl.RouteFamily(fam)}
}

// TestHarnessRouteEventsReachState checks the Route path through the
// harness: State shows the Gateway the last RouteEvent left behind, and
// a Gateway change on a WAN no Group selected writes no route.
func TestHarnessRouteEventsReachState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		events []rtnl.RouteEvent
		want   string // primary's v4 Gateway in State, "" for none
	}{
		{"none learned", nil, ""},
		{"learned", []rtnl.RouteEvent{gateway("eth0", probe.FamilyV4, "192.0.2.1")}, "192.0.2.1"},
		{"replaced", []rtnl.RouteEvent{
			gateway("eth0", probe.FamilyV4, "192.0.2.1"),
			gateway("eth0", probe.FamilyV4, "192.0.2.2"),
		}, "192.0.2.2"},
		{"withdrawn", []rtnl.RouteEvent{
			gateway("eth0", probe.FamilyV4, "192.0.2.1"),
			gatewayGone("eth0", probe.FamilyV4),
		}, ""},
		{"other Family", []rtnl.RouteEvent{gateway("eth0", probe.FamilyV6, "2001:db8::1")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startHarness(t, baseConfig())
			h.Trace()
			for _, e := range tc.events {
				h.Route(e)
			}
			assertTrace(t, h.Trace(), nil)
			got := ""
			if gw := h.State().Wans["primary"].Gateways.V4; gw != nil {
				got = *gw
			}
			if got != tc.want {
				t.Errorf("primary v4 Gateway = %q, want %q", got, tc.want)
			}
		})
	}
}

// hookRecorder is the synchronous Hook sink of the harness. It records
// `hook <event> <group> <old>→<new>` in the trace and keeps the full
// Hook context. Like state.HookNotifier it rejects notifications after
// Close, which it records marked as failed.
type hookRecorder struct {
	trace *trace

	mu     sync.Mutex
	hooks  []state.HookContext
	closed bool
}

func (r *hookRecorder) Notify(c state.HookContext) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := fmt.Sprintf("hook %s %s %s→%s", c.Event, c.Group, c.WanOld, c.WanNew)
	if r.closed {
		r.trace.add(line + failedMark)
		return false
	}
	c.Families = slices.Clone(c.Families)
	r.hooks = append(r.hooks, c)
	r.trace.add(line)
	return true
}

func (r *hookRecorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.trace.add("hooks closed")
	}
}

func (r *hookRecorder) accepted() []state.HookContext {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.hooks)
}
