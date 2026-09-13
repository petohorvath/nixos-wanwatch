package state

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/metrics"
	dto "github.com/prometheus/client_model/go"
)

func testHookNotifier(ctx context.Context, t *testing.T, run func(context.Context, HookContext) []hookResult) (*HookNotifier, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	n := newHookNotifier(ctx, run, metrics.New(), slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(n.Close)
	return n, &logs
}

func awaitHook(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for hook delivery")
	}
}

// Bound the submission itself so a blocking implementation fails without
// leaving the test waiting on its own script gate.
func notifyHook(t *testing.T, n *HookNotifier, h HookContext) bool {
	t.Helper()
	accepted := make(chan bool, 1)
	go func() { accepted <- n.Notify(h) }()
	select {
	case ok := <-accepted:
		return ok
	case <-time.After(time.Second):
		t.Fatal("notification blocked behind script execution")
		return false
	}
}

func TestHookNotifierPreservesDecisionOrderAndData(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	var got []HookContext
	n, _ := testHookNotifier(t.Context(), t, func(ctx context.Context, h HookContext) []hookResult {
		if h.Event == EventUp {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		got = append(got, h)
		return nil
	})
	up := HookContext{Event: EventUp, Group: "home", Timestamp: time.Date(2026, 9, 13, 9, 0, 0, 123, time.UTC)}
	if !notifyHook(t, n, up) {
		t.Fatal("first notification rejected")
	}
	awaitHook(t, started)
	switchCtx := HookContext{
		Event: EventSwitch, Group: "home", WanOld: "primary", WanNew: "backup",
		IfaceOld: "eth0", IfaceNew: "wwan0", GatewayV4Old: "192.0.2.1", GatewayV4New: "198.51.100.1",
		GatewayV6Old: "2001:db8::1", GatewayV6New: "2001:db8:1::1", Families: []string{"v4", "v6"},
		Table: 100, Mark: 200, Timestamp: up.Timestamp.Add(time.Second),
	}
	wantSwitch := switchCtx
	wantSwitch.Families = slices.Clone(switchCtx.Families)
	if !notifyHook(t, n, switchCtx) {
		t.Fatal("queued notification rejected")
	}
	// Caller-owned data can change while the first script is still running.
	switchCtx.Group = "mutated"
	switchCtx.Families[0] = "mutated"
	switchCtx.Timestamp = time.Time{}
	before := time.Now().UTC()
	if !notifyHook(t, n, HookContext{Event: EventDown, Group: "home"}) {
		t.Fatal("down notification rejected")
	}
	after := time.Now().UTC()
	close(release)
	n.Close()
	if len(got) != 3 {
		t.Fatalf("delivered %d notifications, want 3", len(got))
	}
	if !reflect.DeepEqual(got[:2], []HookContext{up, wantSwitch}) || got[2].Event != EventDown {
		t.Errorf("notifications lost order or captured data: %+v", got)
	}
	if ts := got[2].Timestamp; ts.Before(before) || ts.After(after) {
		t.Errorf("fallback timestamp = %v, want submission time between %v and %v", ts, before, after)
	}
}

func TestHookNotifierDropsNewestWhenQueueIsFull(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	var got []string
	n, logs := testHookNotifier(t.Context(), t, func(ctx context.Context, h HookContext) []hookResult {
		if h.Group == "first" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		got = append(got, h.Group)
		return nil
	})
	notifyHook(t, n, HookContext{Event: EventUp, Group: "first"})
	awaitHook(t, started)
	want := []string{"first"}
	for i := range 32 {
		group := fmt.Sprintf("queued-%d", i)
		want = append(want, group)
		if !notifyHook(t, n, HookContext{Event: EventSwitch, Group: group}) {
			t.Fatalf("notification %d rejected before the queue was full", i)
		}
	}
	if notifyHook(t, n, HookContext{Event: EventDown, Group: "overflow"}) {
		t.Error("full queue accepted newest notification")
	}
	close(release)
	n.Close()
	if !slices.Equal(got, want) {
		t.Errorf("delivered = %v, want %v", got, want)
	}
	if !strings.Contains(logs.String(), `msg="hook event dropped: queue full" event=down capacity=32`) {
		t.Errorf("missing overflow diagnostic: %s", logs.String())
	}
}

func TestHookNotifierCancellationDiscardsQueuedEvents(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	calls := 0
	n, _ := testHookNotifier(ctx, t, func(ctx context.Context, _ HookContext) []hookResult {
		calls++
		if calls == 1 {
			close(started)
		}
		<-ctx.Done()
		return nil
	})
	n.Notify(HookContext{Event: EventUp})
	awaitHook(t, started)
	if !n.Notify(HookContext{Event: EventSwitch}) {
		t.Fatal("queued notification rejected")
	}
	cancel()
	if n.Notify(HookContext{Event: EventDown}) {
		t.Error("notification accepted after cancellation")
	}
	done := make(chan struct{})
	go func() { n.Close(); close(done) }()
	awaitHook(t, done)
	if calls != 1 {
		t.Errorf("executed %d notifications, want only the in-flight event", calls)
	}
}

func TestHookNotifierRejectsBeforeFirstNotification(t *testing.T) {
	t.Parallel()
	for _, stop := range []string{"cancel", "close"} {
		t.Run(stop, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			n, _ := testHookNotifier(ctx, t, func(context.Context, HookContext) []hookResult {
				calls++
				return nil
			})
			if stop == "cancel" {
				cancel()
			} else {
				n.Close()
			}
			if n.Notify(HookContext{Event: EventUp}) {
				t.Error("stopped notifier accepted an event")
			}
			n.Close()
			n.Close()
			if calls != 0 {
				t.Errorf("executed %d notifications after stop", calls)
			}
		})
	}
}

func TestHookNotifierConcurrentNotifyAndClose(t *testing.T) {
	t.Parallel()
	var accepted, executed atomic.Int32
	n, _ := testHookNotifier(t.Context(), t, func(context.Context, HookContext) []hookResult {
		executed.Add(1)
		return nil
	})
	n.Notify(HookContext{Event: EventUp})
	accepted.Add(1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			<-start
			if i%3 == 0 {
				n.Close()
			} else if n.Notify(HookContext{Event: EventSwitch}) {
				accepted.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	n.Close()
	if got, want := executed.Load(), accepted.Load(); got != want {
		t.Errorf("Close drained %d events, want all %d accepted events", got, want)
	}
	if n.Notify(HookContext{Event: EventDown}) {
		t.Error("closed notifier accepted an event")
	}
}

func TestHookNotifierExecutesScriptsAndReportsResults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	order := filepath.Join(dir, "order")
	var want []string
	for i := range 10 {
		name := fmt.Sprintf("%02d.sh", i)
		body := "echo " + name + " >> " + order
		switch i {
		case 1:
			body += "\necho failure-output >&2\nexit 7"
		case 2:
			body += "\nsleep 5"
		}
		writeHook(t, filepath.Join(dir, "switch.d"), name, body)
		if i < 8 {
			want = append(want, name)
		}
	}
	mreg := metrics.New()
	var logs bytes.Buffer
	n := NewHookNotifier(t.Context(), dir, 200*time.Millisecond, mreg, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(n.Close)
	if !n.Notify(HookContext{Event: EventSwitch}) {
		t.Fatal("notification rejected")
	}
	n.Close()
	data, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(data)); !slices.Equal(got, want) {
		t.Errorf("executed scripts = %v, want %v", got, want)
	}
	for result, want := range map[string]float64{"ok": 6, "nonzero": 1, "timeout": 1} {
		var m dto.Metric
		if err := mreg.HookInvocations.WithLabelValues("switch", result).Write(&m); err != nil {
			t.Fatal(err)
		}
		if got := m.GetCounter().GetValue(); got != want {
			t.Errorf("%s invocations = %v, want %v", result, got, want)
		}
	}
	for _, want := range []string{"result=nonzero", "exitCode=7", "failure-output", "result=timeout", "limit=8"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %q in hook diagnostics: %s", want, logs.String())
		}
	}
	if got := strings.Count(logs.String(), "hook skipped: per-event limit reached"); got != 2 {
		t.Errorf("skipped diagnostics = %d, want 2: %s", got, logs.String())
	}
}

func TestHookNotifierMissingDirectoryIsQuiet(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	n := NewHookNotifier(t.Context(), t.TempDir(), 0, metrics.New(), slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(n.Close)
	if !n.Notify(HookContext{Event: EventUp}) {
		t.Fatal("notification rejected")
	}
	n.Close()
	if logs.Len() != 0 {
		t.Errorf("missing hook directory produced diagnostics: %s", logs.String())
	}
}

func TestHookNotifierCancellationKillsScriptsAndDescendants(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	orphan := filepath.Join(dir, "orphan")
	queued := filepath.Join(dir, "queued")
	writeHook(t, filepath.Join(dir, "up.d"), "fork.sh",
		"(sleep 1; touch "+orphan+") &\ntouch "+started+"\nsleep 5")
	writeHook(t, filepath.Join(dir, "down.d"), "queued.sh", "touch "+queued)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	n := NewHookNotifier(ctx, dir, 0, metrics.New(), slog.Default())
	t.Cleanup(n.Close)
	n.Notify(HookContext{Event: EventUp})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !n.Notify(HookContext{Event: EventDown}) {
		t.Fatal("queued notification rejected")
	}
	cancel()
	done := make(chan struct{})
	go func() { n.Close(); close(done) }()
	awaitHook(t, done)
	// Past the descendant's sleep: surviving cancellation would leave a file.
	time.Sleep(2 * time.Second)
	for _, path := range []string{orphan, queued} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("cancelled hook left %s (stat error: %v)", path, err)
		}
	}
}
