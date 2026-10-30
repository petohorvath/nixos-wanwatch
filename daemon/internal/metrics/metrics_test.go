package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// scrape runs `r`'s handler against an in-memory request and
// returns the response body. Lets the metric catalog be asserted
// without a real Unix-socket listener.
func scrape(t *testing.T, r *Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	r.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(body)
}

func TestNewRegistersEveryCatalogMetric(t *testing.T) {
	t.Parallel()
	r := New()
	// Touch each *Vec metric with a representative label set so it
	// appears in the scrape output — Prometheus elides Vec
	// metrics that have never been observed.
	r.ProbeRTT.WithLabelValues("primary", "1.1.1.1", "v4").Set(12.3)
	r.ProbeJitter.WithLabelValues("primary", "v4").Set(1.2)
	r.ProbeLoss.WithLabelValues("primary", "v4").Set(0)
	r.WanCarrier.WithLabelValues("primary").Set(1)
	r.WanOperstate.WithLabelValues("primary", "up").Set(1)
	r.WanFamilyHealthy.WithLabelValues("primary", "v4").Set(1)
	r.WanHealthy.WithLabelValues("primary").Set(1)
	r.WanCarrierChanges.WithLabelValues("primary").Inc()
	r.GroupSelected.WithLabelValues("home", "primary").Set(1)
	r.GroupDecisions.WithLabelValues("home", "carrier").Inc()
	r.ApplyRouteDuration.WithLabelValues("home", "v4").Observe(0.001)
	r.ApplyRouteErrors.WithLabelValues("home", "v4").Inc()
	r.ApplyOpErrors.WithLabelValues("home", "ifindex_lookup").Inc()
	r.StatePublications.Inc()
	r.HookInvocations.WithLabelValues("home", "up", "ok").Inc()
	r.SetBuildInfo("0.1.0", "deadbeef", runtime.Version())

	body := scrape(t, r)
	for _, want := range []string{
		"wanwatch_probe_rtt_seconds",
		"wanwatch_probe_jitter_seconds",
		"wanwatch_probe_loss_ratio",
		"wanwatch_wan_carrier",
		"wanwatch_wan_operstate",
		"wanwatch_wan_family_healthy",
		"wanwatch_wan_healthy",
		"wanwatch_wan_carrier_changes_total",
		"wanwatch_group_selected",
		"wanwatch_group_decisions_total",
		"wanwatch_apply_route_duration_seconds",
		"wanwatch_apply_route_errors_total",
		"wanwatch_apply_op_errors_total",
		"wanwatch_state_publications_total",
		"wanwatch_hook_invocations_total",
		"wanwatch_build_info",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape body missing metric %q", want)
		}
	}
}

func TestRegistryReflectsSetValues(t *testing.T) {
	t.Parallel()
	r := New()
	r.WanCarrier.WithLabelValues("primary").Set(1)
	r.WanCarrier.WithLabelValues("backup").Set(0)

	body := scrape(t, r)
	if !strings.Contains(body, `wanwatch_wan_carrier{wan="primary"} 1`) {
		t.Errorf("scrape missing primary=1; body:\n%s", body)
	}
	if !strings.Contains(body, `wanwatch_wan_carrier{wan="backup"} 0`) {
		t.Errorf("scrape missing backup=0; body:\n%s", body)
	}
}

func TestStatePublicationsIncrement(t *testing.T) {
	t.Parallel()
	// Scalar Counter — distinct API from *Vec; verify it still
	// surfaces in the scrape.
	r := New()
	r.StatePublications.Inc()
	r.StatePublications.Inc()
	r.StatePublications.Inc()

	body := scrape(t, r)
	if !strings.Contains(body, "wanwatch_state_publications_total 3") {
		t.Errorf("expected state_publications_total = 3; body:\n%s", body)
	}
}

// Route writes are netlink round trips that usually finish well
// under a millisecond, so the buckets start at 100 µs.
func TestApplyRouteDurationBuckets(t *testing.T) {
	t.Parallel()
	r := New()
	r.ApplyRouteDuration.WithLabelValues("home", "v4").Observe(0.0003)

	bucket := regexp.MustCompile(`(?m)^wanwatch_apply_route_duration_seconds_bucket\{family="v4",group="home",le="([^"]+)"\} (\d+)$`)
	var bounds, counts []string
	for _, m := range bucket.FindAllStringSubmatch(scrape(t, r), -1) {
		bounds = append(bounds, m[1])
		counts = append(counts, m[2])
	}
	wantBounds := []string{"0.0001", "0.00025", "0.0005", "0.001", "0.0025", "0.005", "0.01", "0.025", "0.1", "1", "+Inf"}
	if !slices.Equal(bounds, wantBounds) {
		t.Errorf("bucket bounds = %v, want %v", bounds, wantBounds)
	}
	// A 300 µs write lands from the 500 µs bucket up.
	wantCounts := []string{"0", "0", "1", "1", "1", "1", "1", "1", "1", "1", "1"}
	if !slices.Equal(counts, wantCounts) {
		t.Errorf("cumulative bucket counts = %v, want %v", counts, wantCounts)
	}
}

// The label names follow Prometheus's own *_build_info series.
func TestBuildInfoLabels(t *testing.T) {
	t.Parallel()
	r := New()
	r.SetBuildInfo("0.1.0", "abc1234", "go1.26.0")

	want := `wanwatch_build_info{goversion="go1.26.0",revision="abc1234",version="0.1.0"} 1`
	if body := scrape(t, r); !strings.Contains(body, want) {
		t.Errorf("scrape missing %q; body:\n%s", want, body)
	}
}
