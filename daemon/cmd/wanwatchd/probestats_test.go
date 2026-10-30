package main

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/petohorvath/nixos-wanwatch/daemon/internal/probe"
)

// sampled is the ProbeResult a Pinger reports after pushing samples
// into each Target's Window of the given size.
func sampled(tb testing.TB, wan string, fam probe.Family, size int, samples map[string][]probe.Sample) probe.ProbeResult {
	tb.Helper()
	windows := make(map[string]*probe.WindowStats, len(samples))
	for target, ss := range samples {
		w, err := probe.NewWindow(size)
		if err != nil {
			tb.Fatalf("NewWindow(%d): %v", size, err)
		}
		for _, s := range ss {
			w.Push(s)
		}
		windows[target] = w
	}
	return probe.ProbeResult{Wan: wan, Family: fam, Stats: probe.Aggregate(windows)}
}

// rawFamily reads a WAN's Family object from state.json as a State
// reader sees it, without the daemon's own decoding.
func rawFamily(tb testing.TB, h *harness, wan, fam string) map[string]any {
	tb.Helper()
	wans, _ := rawState(tb, h)["wans"].(map[string]any)
	w, _ := wans[wan].(map[string]any)
	fams, _ := w["families"].(map[string]any)
	f, ok := fams[fam].(map[string]any)
	if !ok {
		tb.Fatalf("State has no Family %s of WAN %s", fam, wan)
	}
	return f
}

// scrapeSeries scrapes h's metrics endpoint and returns each series of
// metric, keyed by its label set, with its value as exposed.
func scrapeSeries(tb testing.TB, h *harness, metric string) map[string]string {
	tb.Helper()
	rec := httptest.NewRecorder()
	h.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK {
		tb.Fatalf("scrape status = %d, want 200", rec.Code)
	}
	series := map[string]string{}
	for line := range strings.Lines(rec.Body.String()) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), metric+"{")
		if !ok {
			continue
		}
		labels, value, _ := strings.Cut(rest, "} ")
		series[labels] = value
	}
	return series
}

// stat is an expected statistic: a value, or none for no data.
type stat struct {
	value float64
	none  bool
}

var noData = stat{none: true}

func val(v float64) stat { return stat{value: v} }

// stats is what State and the per-Family gauges must report for one
// (WAN, Family), and the per-Target RTT gauge for each Target.
type stats struct {
	rtt, jitter, loss stat
	targetRTT         map[string]stat
}

// assertStateStat checks a State statistic: null for no data.
func assertStateStat(tb testing.TB, where, key string, fam map[string]any, want stat) {
	tb.Helper()
	got, present := fam[key]
	switch {
	case !present:
		tb.Errorf("%s: State has no %s", where, key)
	case want.none && got != nil:
		tb.Errorf("%s: State %s = %#v, want null", where, key, got)
	case !want.none && got != want.value:
		tb.Errorf("%s: State %s = %#v, want %v", where, key, got, want.value)
	}
}

// assertGauge checks an exposed gauge series: NaN for no data.
func assertGauge(tb testing.TB, where, series string, got map[string]string, labels string, want stat) {
	tb.Helper()
	raw, ok := got[labels]
	if !ok {
		tb.Errorf("%s: no %s{%s} series", where, series, labels)
		return
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		tb.Errorf("%s: %s{%s} = %q, not a float", where, series, labels, raw)
		return
	}
	if (want.none && !math.IsNaN(v)) || (!want.none && v != want.value) {
		w := "NaN"
		if !want.none {
			w = strconv.FormatFloat(want.value, 'g', -1, 64)
		}
		tb.Errorf("%s: %s{%s} = %s, want %s", where, series, labels, raw, w)
	}
}

// TestProbeStatistics pins the probe statistics of every (WAN, Family)
// as State and the probe gauges report them, from Samples pushed into
// real Windows. RTT and jitter are null in State and NaN as gauges
// until a Sample in the Window succeeds, loss until the Window holds a
// Sample, and one successful Sample has a jitter of 0. A Family's
// statistic is the mean over the Targets with data for it, and each
// Target's mean RTT is its own gauge. The per-Family gauges carry only
// the wan and family labels.
func TestProbeStatistics(t *testing.T) {
	t.Parallel()
	cfg := baseConfig()
	backup := cfg.Wans["backup"]
	backup.Probe.Targets.V4 = []string{"203.0.113.53", "203.0.113.54"}
	cfg.Wans["backup"] = backup
	const size = 5 // baseConfig's windowSize
	lost := probe.Sample{Lost: true}
	ms := func(v uint64) probe.Sample { return probe.Sample{RTTMicros: v * 1000} }

	h := startHarness(t, cfg)
	h.Link(carrier("eth0", true))
	h.Link(carrier("wwan0", true))

	// want starts with every Window empty.
	want := map[[2]string]stats{}
	for name, w := range cfg.Wans {
		for _, fam := range w.Probe.Targets.Families() {
			s := stats{rtt: noData, jitter: noData, loss: noData, targetRTT: map[string]stat{}}
			for _, target := range w.Probe.Targets.For(fam) {
				s.targetRTT[target] = noData
			}
			want[[2]string{name, fam.String()}] = s
		}
	}

	assertAll := func(step string) {
		t.Helper()
		rtt := scrapeSeries(t, h, "wanwatch_probe_rtt_seconds")
		jitter := scrapeSeries(t, h, "wanwatch_probe_jitter_seconds")
		loss := scrapeSeries(t, h, "wanwatch_probe_loss_ratio")
		targetRTT := scrapeSeries(t, h, "wanwatch_probe_target_rtt_seconds")
		var targets int
		for key, s := range want {
			wan, fam := key[0], key[1]
			where := step + ": " + wan + " " + fam
			f := rawFamily(t, h, wan, fam)
			assertStateStat(t, where, "rttSeconds", f, s.rtt)
			assertStateStat(t, where, "jitterSeconds", f, s.jitter)
			assertStateStat(t, where, "lossRatio", f, s.loss)
			labels := `family="` + fam + `",wan="` + wan + `"`
			assertGauge(t, where, "wanwatch_probe_rtt_seconds", rtt, labels, s.rtt)
			assertGauge(t, where, "wanwatch_probe_jitter_seconds", jitter, labels, s.jitter)
			assertGauge(t, where, "wanwatch_probe_loss_ratio", loss, labels, s.loss)
			for target, v := range s.targetRTT {
				labels := `family="` + fam + `",target="` + target + `",wan="` + wan + `"`
				assertGauge(t, where, "wanwatch_probe_target_rtt_seconds", targetRTT, labels, v)
				targets++
			}
		}
		for name, series := range map[string]map[string]string{
			"wanwatch_probe_rtt_seconds":    rtt,
			"wanwatch_probe_jitter_seconds": jitter,
			"wanwatch_probe_loss_ratio":     loss,
		} {
			if len(series) != len(want) {
				t.Errorf("%s: %s has series %v, want one per (WAN, Family)", step, name, series)
			}
		}
		if len(targetRTT) != targets {
			t.Errorf("%s: wanwatch_probe_target_rtt_seconds has series %v, want one per Target", step, targetRTT)
		}
	}

	// Before any Sample, every Window is empty.
	assertAll("before any Sample")

	// Each first full Window cooks its Family, which republishes State.
	h.Probe(sampled(t, "primary", probe.FamilyV4, size, map[string][]probe.Sample{
		"198.51.100.53": {lost, lost, lost, lost, lost},
	}))
	want[[2]string{"primary", "v4"}] = stats{
		rtt: noData, jitter: noData, loss: val(1),
		targetRTT: map[string]stat{"198.51.100.53": noData},
	}
	assertAll("fully lost Window")

	h.Probe(sampled(t, "primary", probe.FamilyV6, size, map[string][]probe.Sample{
		"2001:db8:53::1": {lost, lost, lost, lost, ms(15)},
	}))
	want[[2]string{"primary", "v6"}] = stats{
		rtt: val(0.015), jitter: val(0), loss: val(0.8),
		targetRTT: map[string]stat{"2001:db8:53::1": val(0.015)},
	}
	assertAll("one successful Sample")

	// Only the second Target has an RTT: 10, 20, and 30 ms average to
	// 20 ms with a population standard deviation of 8.164 ms.
	h.Probe(sampled(t, "backup", probe.FamilyV4, size, map[string][]probe.Sample{
		"203.0.113.53": {lost, lost, lost, lost, lost},
		"203.0.113.54": {ms(10), lost, ms(20), lost, ms(30)},
	}))
	want[[2]string{"backup", "v4"}] = stats{
		rtt: val(0.02), jitter: val(0.008164), loss: val(0.7),
		targetRTT: map[string]stat{"203.0.113.53": noData, "203.0.113.54": val(0.02)},
	}
	assertAll("a lost Target beside a measured one")
}
