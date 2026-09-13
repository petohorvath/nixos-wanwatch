package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/apply"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/config"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/rtnl"
	"github.com/petohorvath/nixos-wanwatch/daemon/internal/state"
)

func readPublishedState(t *testing.T, d *daemon) state.State {
	t.Helper()
	data, err := os.ReadFile(d.cfg.Global.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var snap state.State
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// Capture delivered Hook data through scripts, keeping the notifier's
// execution adapter private. Event marker files let tests wait for an
// initial notification without closing delivery before the next Decision.
func captureDecisionHooks(t *testing.T, d *daemon) string {
	t.Helper()
	dir := t.TempDir()
	for _, event := range []string{"up", "down", "switch"} {
		writeHook(t, filepath.Join(d.cfg.Global.HooksDir, event+".d"), "capture.sh",
			`printf '%s|%s|%s|%s|%s\n' "$WANWATCH_EVENT" "$WANWATCH_GROUP" "$WANWATCH_WAN_OLD" "$WANWATCH_WAN_NEW" "$WANWATCH_TS" >> `+dir+`/events
touch `+dir+`/"$WANWATCH_EVENT"`)
	}
	return dir
}

func readDecisionHooks(t *testing.T, dir string) []state.HookContext {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "events"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var hooks []state.HookContext
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 5 {
			t.Fatalf("invalid captured Hook: %q", line)
		}
		stamp, err := time.Parse(time.RFC3339Nano, fields[4])
		if err != nil {
			t.Fatal(err)
		}
		hooks = append(hooks, state.HookContext{
			Event: state.Event(fields[0]), Group: fields[1],
			WanOld: fields[2], WanNew: fields[3], Timestamp: stamp,
		})
	}
	return hooks
}

// A partial Apply must not publish a switch or flush the vacated WAN.
// Exercise every hard failure through the real route-building adapter and
// every retry source through daemon event handlers.
func TestDecisionPublicationWaitsForApply(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"ifindex", "v4 write", "v6 write"} {
		for _, retry := range []string{"partial Probe", "full Probe", "Gateway"} {
			t.Run(failure+"/"+retry, func(t *testing.T) {
				t.Parallel()
				cfg := testCfgWithGroup()
				wan := cfg.Wans["primary"]
				wan.Probe.Thresholds = config.Thresholds{LossPctUp: 10, LossPctDown: 20, RttMsUp: 100, RttMsDown: 200}
				wan.Probe.Hysteresis = config.Hysteresis{ConsecutiveUp: 1, ConsecutiveDown: 1}
				cfg.Wans["primary"] = wan
				d := testDaemon(t, cfg)
				d.gateways.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
				d.gateways.set("eth0", rtnl.RouteFamilyV6, net.ParseIP("2001:db8::1"))
				d.gateways.set("wwan0", rtnl.RouteFamilyV4, net.ParseIP("198.51.100.1"))
				hookDir := captureDecisionHooks(t, d)
				var flushed []string
				d.interfaceAddrs = func(iface string) ([]net.IP, error) {
					flushed = append(flushed, iface)
					return nil, nil
				}
				failing := false
				d.ifindexOf = func(iface string) (int, error) {
					if iface == "eth0" {
						if failing && failure == "ifindex" {
							return failingIfindex(iface)
						}
						return 1, nil
					}
					return 2, nil
				}
				var attempts []probe.Family
				d.writeRoute = func(_ context.Context, r apply.DefaultRoute) error {
					attempts = append(attempts, r.Family)
					if failing && r.Family.String()+" write" == failure {
						return errors.New("route write failed")
					}
					return nil
				}
				d.handleLinkEvent(t.Context(), rtnl.LinkEvent{Name: "wwan0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp})
				waitForHookPath(t, filepath.Join(hookDir, "up"), 3*time.Second)
				hooks := readDecisionHooks(t, hookDir)
				before := readPublishedState(t, d).Groups["home"]
				if before.Active == nil || *before.Active != "backup" || len(hooks) != 1 {
					t.Fatalf("setup: State = %+v, Hooks = %+v", before, hooks)
				}

				failing = true
				attempts = nil
				d.handleLinkEvent(t.Context(), rtnl.LinkEvent{Name: "eth0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp})
				hooks = readDecisionHooks(t, hookDir)
				pending := readPublishedState(t, d).Groups["home"]
				if pending.Active == nil || *pending.Active != "backup" || !pending.ActiveSince.Equal(*before.ActiveSince) || pending.DecisionsTotal != 2 {
					t.Fatalf("failed Apply published a switch: %+v", pending)
				}
				if len(hooks) != 1 || len(flushed) != 0 {
					t.Fatalf("failed Apply delivered Hooks or flushed conntrack: Hooks=%+v, flushes=%v", hooks, flushed)
				}
				if failure != "ifindex" {
					slices.Sort(attempts)
					if !slices.Equal(attempts, []probe.Family{probe.FamilyV4, probe.FamilyV6}) {
						t.Errorf("partial Apply attempted %v, want both families even on failure", attempts)
					}
				}

				failing = false
				attempts = nil
				d.handleProbeResult(t.Context(), probe.ProbeResult{Wan: "backup", Family: probe.FamilyV4})
				d.handleRouteEvent(t.Context(), rtnl.RouteEvent{Op: rtnl.RouteEventAdd, Iface: "wwan0", Family: rtnl.RouteFamilyV4, Gateway: net.ParseIP("198.51.100.2")})
				if len(attempts) != 0 {
					t.Fatalf("events for the old Selection retried Apply: %v", attempts)
				}
				if retry == "Gateway" {
					d.handleRouteEvent(t.Context(), rtnl.RouteEvent{Op: rtnl.RouteEventAdd, Iface: "eth0", Family: rtnl.RouteFamilyV4, Gateway: net.ParseIP("192.0.2.2")})
				} else {
					d.handleProbeResult(t.Context(), probe.ProbeResult{
						Wan: "primary", Family: probe.FamilyV4,
						Stats: probe.FamilyStats{RTTMicros: 10_000, WindowFilled: retry == "full Probe"},
					})
				}
				d.hooks.Close()
				hooks = readDecisionHooks(t, hookDir)
				slices.Sort(attempts)
				if !slices.Equal(attempts, []probe.Family{probe.FamilyV4, probe.FamilyV6}) {
					t.Errorf("retry attempted %v, want both families", attempts)
				}
				snap := readPublishedState(t, d)
				committed := snap.Groups["home"]
				if committed.Active == nil || *committed.Active != "primary" || committed.DecisionsTotal != 2 {
					t.Fatalf("retry State = %+v, want primary and 2 Decisions", committed)
				}
				if len(hooks) != 2 || hooks[1].Event != state.EventSwitch || hooks[1].WanOld != "backup" || hooks[1].WanNew != "primary" {
					t.Fatalf("retry Hooks = %+v, want one deferred backup-to-primary switch", hooks)
				}
				if committed.ActiveSince == nil || !committed.ActiveSince.Equal(hooks[1].Timestamp) {
					t.Error("committed Selection and Hook have different timestamps")
				}
				// Partial Windows only retry Apply; no later Health/Gateway
				// publication overwrites this Decision's updatedAt.
				if retry == "partial Probe" && !snap.UpdatedAt.Equal(hooks[1].Timestamp) {
					t.Error("State publication and Hook have different timestamps")
				}
				if !slices.Equal(flushed, []string{"wwan0"}) {
					t.Errorf("conntrack flushes = %v, want the vacated backup once", flushed)
				}
			})
		}
	}
}

func TestDecisionMissingGatewayCommitsThenRefreshes(t *testing.T) {
	t.Parallel()
	for _, cached := range []string{"none", "v4", "v6", "point-to-point"} {
		for _, family := range probe.AllFamilies {
			t.Run(cached+"/refresh "+family.String(), func(t *testing.T) {
				t.Parallel()
				cfg := testCfgWithGroup()
				wan := cfg.Wans["primary"]
				wan.PointToPoint = cached == "point-to-point"
				cfg.Wans["primary"] = wan
				d := testDaemon(t, cfg)
				if cached == "v4" {
					d.gateways.set("eth0", rtnl.RouteFamilyV4, net.ParseIP("192.0.2.1"))
				}
				if cached == "v6" {
					d.gateways.set("eth0", rtnl.RouteFamilyV6, net.ParseIP("2001:db8::1"))
				}
				var attempts []probe.Family
				d.writeRoute = func(_ context.Context, r apply.DefaultRoute) error {
					attempts = append(attempts, r.Family)
					if r.PointToPoint != wan.PointToPoint || (r.Gateway == nil) != wan.PointToPoint {
						t.Errorf("unexpected route mode: %+v", r)
					}
					return nil
				}
				hookDir := captureDecisionHooks(t, d)
				d.handleLinkEvent(t.Context(), rtnl.LinkEvent{Name: "eth0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp})
				waitForHookPath(t, filepath.Join(hookDir, "up"), 3*time.Second)
				hooks := readDecisionHooks(t, hookDir)
				before := readPublishedState(t, d).Groups["home"]
				if before.Active == nil || *before.Active != "primary" || before.DecisionsTotal != 1 || len(hooks) != 1 || hooks[0].Event != state.EventUp {
					t.Fatalf("soft skip did not commit: State=%+v, Hooks=%+v", before, hooks)
				}
				var want []probe.Family
				switch cached {
				case "v4":
					want = []probe.Family{probe.FamilyV4}
				case "v6":
					want = []probe.Family{probe.FamilyV6}
				case "point-to-point":
					want = []probe.Family{probe.FamilyV4, probe.FamilyV6}
				}
				slices.Sort(attempts)
				if !slices.Equal(attempts, want) {
					t.Errorf("initial route attempts = %v, want %v", attempts, want)
				}
				attempts = nil
				gateway := net.ParseIP("192.0.2.2")
				if family == probe.FamilyV6 {
					gateway = net.ParseIP("2001:db8::2")
				}
				event := rtnl.RouteEvent{Op: rtnl.RouteEventAdd, Iface: "eth0", Family: rtnl.RouteFamily(family), Gateway: gateway}
				d.handleRouteEvent(t.Context(), event)
				if !slices.Equal(attempts, []probe.Family{family}) {
					t.Errorf("Gateway refresh wrote %v, want only %v", attempts, family)
				}
				if after := readPublishedState(t, d).Groups["home"]; !reflect.DeepEqual(after, before) {
					t.Fatalf("Gateway refresh changed Selection: %+v", after)
				}
				attempts = nil
				publications := readCounter(t, d.metrics.StatePublications)
				d.handleRouteEvent(t.Context(), event)
				if len(attempts) != 0 || readCounter(t, d.metrics.StatePublications) != publications {
					t.Error("duplicate Gateway event reapplied or republished State")
				}
				d.hooks.Close()
				if hooks = readDecisionHooks(t, hookDir); len(hooks) != 1 {
					t.Fatalf("Gateway refresh delivered a Hook: %+v", hooks)
				}
			})
		}
	}
}

func TestSupersededDecisionPublishesOnlyConvergedSelection(t *testing.T) {
	t.Parallel()
	d := testDaemon(t, testCfgWithGroup())
	d.ifindexOf = func(iface string) (int, error) {
		if iface == "eth0" {
			return failingIfindex(iface)
		}
		return 2, nil
	}
	hookDir := captureDecisionHooks(t, d)
	d.handleLinkEvent(t.Context(), rtnl.LinkEvent{Name: "eth0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp})
	d.handleLinkEvent(t.Context(), rtnl.LinkEvent{Name: "wwan0", Carrier: rtnl.CarrierUp, Operstate: rtnl.OperstateUp})
	hooks := readDecisionHooks(t, hookDir)
	if got := readPublishedState(t, d).Groups["home"]; got.Active != nil || got.DecisionsTotal != 1 || len(hooks) != 0 {
		t.Fatalf("uncommitted primary became visible: State=%+v, Hooks=%+v", got, hooks)
	}
	d.handleLinkEvent(t.Context(), rtnl.LinkEvent{Name: "eth0", Carrier: rtnl.CarrierDown, Operstate: rtnl.OperstateDown})
	d.hooks.Close()
	hooks = readDecisionHooks(t, hookDir)
	if got := readPublishedState(t, d).Groups["home"]; got.Active == nil || *got.Active != "backup" || got.DecisionsTotal != 2 {
		t.Fatalf("superseding Decision did not commit backup: %+v", got)
	}
	if len(hooks) != 1 || hooks[0].Event != state.EventUp || hooks[0].WanOld != "" || hooks[0].WanNew != "backup" {
		t.Fatalf("Hooks = %+v, want only an up event for backup", hooks)
	}
}
