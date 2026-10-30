// Package metrics owns the daemon's Prometheus registry and its
// Unix-socket HTTP endpoint. docs/metrics.md fixes the metric catalog —
// every metric defined here corresponds to one row in that table,
// so name and label drift is detectable by diffing this file
// against the doc.
//
// Each metric is exposed as a public field on Registry. The
// cmd/wanwatchd daemon updates them from its event handlers — off
// the values the probe / rtnl / selector / apply / state layers
// return — via prometheus/client_golang's standard
// `.WithLabelValues(...).Set(...)` / `.Inc()` / `.Observe(...)`
// API. Typed wrapper methods are a follow-up if call sites prove
// fragile.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace is the Prometheus metric-name prefix for every metric
// in this package — `wanwatch_*` per docs/metrics.md.
const Namespace = "wanwatch"

// routeDurationBuckets spans 100 µs to 1 s. A route write is one
// netlink round trip that usually takes well under a millisecond, so
// prometheus.DefBuckets, which start at 5 ms, would put almost every
// write in the first bucket.
var routeDurationBuckets = []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.1, 1}

// Registry bundles the typed metric handles plus the underlying
// prometheus.Registry. One per daemon instance, created at startup
// and passed to every package that needs to record a sample.
type Registry struct {
	reg *prometheus.Registry

	// Probe layer — labels per docs/metrics.md.
	ProbeRTT    *prometheus.GaugeVec
	ProbeJitter *prometheus.GaugeVec
	ProbeLoss   *prometheus.GaugeVec

	// WAN layer.
	WanCarrier        *prometheus.GaugeVec
	WanOperstate      *prometheus.GaugeVec
	WanFamilyHealthy  *prometheus.GaugeVec
	WanHealthy        *prometheus.GaugeVec
	WanCarrierChanges *prometheus.CounterVec

	// Group layer.
	GroupSelected  *prometheus.GaugeVec
	GroupDecisions *prometheus.CounterVec

	// Apply layer — split per-family vs family-agnostic so labels
	// stay non-empty (Prometheus best practice).
	ApplyRouteDuration *prometheus.HistogramVec
	ApplyRouteErrors   *prometheus.CounterVec
	ApplyOpErrors      *prometheus.CounterVec

	// Daemon-wide.
	StatePublications prometheus.Counter
	HookInvocations   *prometheus.CounterVec
	BuildInfo         *prometheus.GaugeVec
}

// New constructs a Registry with every metric in docs/metrics.md
// registered. The returned Registry's Handler() serves /metrics.
func New() *Registry {
	reg := prometheus.NewRegistry()
	r := &Registry{
		reg: reg,

		ProbeRTT: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "probe",
			Name:      "rtt_seconds",
			Help:      "Last sample's round-trip time per probe target, seconds.",
		}, []string{"wan", "target", "family"}),

		ProbeJitter: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "probe",
			Name:      "jitter_seconds",
			Help:      "Per-(WAN, family) jitter across the sliding window, seconds.",
		}, []string{"wan", "family"}),

		ProbeLoss: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "probe",
			Name:      "loss_ratio",
			Help:      "Per-(WAN, family) packet loss fraction in [0, 1].",
		}, []string{"wan", "family"}),

		WanCarrier: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "wan",
			Name:      "carrier",
			Help:      "Per-WAN carrier state: 1 = up, 0 = down, -1 = unknown.",
		}, []string{"wan"}),

		WanOperstate: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "wan",
			Name:      "operstate",
			Help:      "Per-WAN operstate as a state-set: 1 for the current state, 0 for the others; an operstate outside the set reports as unknown.",
		}, []string{"wan", "state"}),

		WanFamilyHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "wan",
			Name:      "family_healthy",
			Help:      "Per-(WAN, family) health verdict from the probe+threshold pipeline: 1 = healthy.",
		}, []string{"wan", "family"}),

		WanHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "wan",
			Name:      "healthy",
			Help:      "Aggregate per-WAN health under the configured family-health policy: 1 = healthy.",
		}, []string{"wan"}),

		WanCarrierChanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "wan",
			Name:      "carrier_changes_total",
			Help:      "Carrier transitions observed per WAN.",
		}, []string{"wan"}),

		GroupSelected: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "group",
			Name:      "selected",
			Help:      "1 for the Selection of `group`, 0 for its other Members; every Member is 0 from startup.",
		}, []string{"group", "wan"}),

		GroupDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "group",
			Name:      "decisions_total",
			Help:      "Decisions emitted per group, partitioned by reason ∈ {health,carrier}.",
		}, []string{"group", "reason"}),

		ApplyRouteDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "apply",
			Name:      "route_duration_seconds",
			Help:      "Wall time of a default-route RTM_NEWROUTE call.",
			Buckets:   routeDurationBuckets,
		}, []string{"group", "family"}),

		ApplyRouteErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "apply",
			Name:      "route_errors_total",
			Help:      "Default-route writes that returned a netlink error.",
		}, []string{"group", "family"}),

		ApplyOpErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "apply",
			Name:      "op_errors_total",
			Help:      "Family-agnostic apply ops that returned an error.",
		}, []string{"group", "op"}),

		StatePublications: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "state",
			Name:      "publications_total",
			Help:      "Successful atomic writes of /run/wanwatch/state.json.",
		}),

		HookInvocations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "hook",
			Name:      "invocations_total",
			Help:      "Hook runs by Group, event, and result: ok, nonzero, timeout, or error.",
		}, []string{"group", "event", "result"}),

		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "build_info",
			Help:      "Build identification — set to 1 with version/revision/goversion labels at startup.",
		}, []string{"version", "revision", "goversion"}),
	}

	reg.MustRegister(
		r.ProbeRTT, r.ProbeJitter, r.ProbeLoss,
		r.WanCarrier, r.WanOperstate, r.WanFamilyHealthy, r.WanHealthy, r.WanCarrierChanges,
		r.GroupSelected, r.GroupDecisions,
		r.ApplyRouteDuration, r.ApplyRouteErrors, r.ApplyOpErrors,
		r.StatePublications, r.HookInvocations, r.BuildInfo,
	)
	return r
}

// SetBuildInfo sets wanwatch_build_info to 1 for the binary's
// version, revision, and Go toolchain version.
func (r *Registry) SetBuildInfo(version, revision, goversion string) {
	r.BuildInfo.With(prometheus.Labels{
		"version":   version,
		"revision":  revision,
		"goversion": goversion,
	}).Set(1)
}

// Handler returns the http.Handler that serves the Prometheus
// scrape endpoint. Mount it under /metrics in the Server.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}
