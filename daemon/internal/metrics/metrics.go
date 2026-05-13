// Package metrics owns the daemon's Prometheus registry and its
// Unix-socket HTTP endpoint. PLAN §7.2 fixes the metric catalog —
// every metric defined here corresponds to one row in that table,
// so name and label drift is detectable by diffing this file
// against the doc.
//
// Each metric is exposed as a public field on Registry so callers
// (probe, rtnl, selector, apply, state) update them directly via
// vishvananda's standard `.WithLabelValues(...).Set(...)` /
// `.Inc()` / `.Observe(...)` API. Typed wrapper methods are a
// follow-up if call sites prove fragile.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace is the Prometheus metric-name prefix for every metric
// in this package — `wanwatch_*` per PLAN §7.2.
const Namespace = "wanwatch"

// Registry bundles the typed metric handles plus the underlying
// prometheus.Registry. One per daemon instance, created at startup
// and passed to every package that needs to record a sample.
type Registry struct {
	reg *prometheus.Registry

	// Probe layer — labels per PLAN §7.2.
	ProbeRTT     *prometheus.GaugeVec
	ProbeJitter  *prometheus.GaugeVec
	ProbeLoss    *prometheus.GaugeVec
	ProbeSamples *prometheus.CounterVec

	// WAN layer.
	WanCarrier        *prometheus.GaugeVec
	WanOperstate      *prometheus.GaugeVec
	WanFamilyHealthy  *prometheus.GaugeVec
	WanHealthy        *prometheus.GaugeVec
	WanCarrierChanges *prometheus.CounterVec

	// Group layer.
	GroupActive    *prometheus.GaugeVec
	GroupDecisions *prometheus.CounterVec

	// Apply layer — split per-family vs family-agnostic so labels
	// stay non-empty (Prometheus best practice).
	ApplyRouteDuration *prometheus.HistogramVec
	ApplyRouteErrors   *prometheus.CounterVec
	ApplyOpDuration    *prometheus.HistogramVec
	ApplyOpErrors      *prometheus.CounterVec

	// Daemon-wide.
	StatePublications prometheus.Counter
	HookInvocations   *prometheus.CounterVec
	BuildInfo         *prometheus.GaugeVec
}

// New constructs a Registry with every metric in PLAN §7.2
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

		ProbeSamples: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "probe",
			Name:      "samples_total",
			Help:      "Probe samples observed, partitioned by result ∈ {success,timeout,error}.",
		}, []string{"wan", "target", "family", "result"}),

		WanCarrier: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "wan",
			Name:      "carrier",
			Help:      "Per-WAN carrier state: 1 = up, 0 = down.",
		}, []string{"wan"}),

		WanOperstate: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "wan",
			Name:      "operstate",
			Help:      "Per-WAN IFLA_OPERSTATE value (mirrors /usr/include/linux/if.h).",
		}, []string{"wan"}),

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

		GroupActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "group",
			Name:      "active",
			Help:      "1 for the active member of `group`, 0 for the others.",
		}, []string{"group", "wan"}),

		GroupDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "group",
			Name:      "decisions_total",
			Help:      "Decisions emitted per group, partitioned by reason ∈ {health,carrier,startup,manual}.",
		}, []string{"group", "reason"}),

		ApplyRouteDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "apply",
			Name:      "route_duration_seconds",
			Help:      "Wall time of a default-route RTM_NEWROUTE call.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"group", "family"}),

		ApplyRouteErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "apply",
			Name:      "route_errors_total",
			Help:      "Default-route writes that returned a netlink error.",
		}, []string{"group", "family"}),

		ApplyOpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "apply",
			Name:      "op_duration_seconds",
			Help:      "Wall time of a family-agnostic apply op ∈ {conntrack_flush,state_write,hook,rule_install}.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"group", "op"}),

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
			Help:      "Hook invocations partitioned by event and result ∈ {ok,nonzero,timeout}.",
		}, []string{"event", "result"}),

		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "build_info",
			Help:      "Build identification — set to 1 with version/go_version/commit labels at startup.",
		}, []string{"version", "go_version", "commit"}),
	}

	reg.MustRegister(
		r.ProbeRTT, r.ProbeJitter, r.ProbeLoss, r.ProbeSamples,
		r.WanCarrier, r.WanOperstate, r.WanFamilyHealthy, r.WanHealthy, r.WanCarrierChanges,
		r.GroupActive, r.GroupDecisions,
		r.ApplyRouteDuration, r.ApplyRouteErrors, r.ApplyOpDuration, r.ApplyOpErrors,
		r.StatePublications, r.HookInvocations, r.BuildInfo,
	)
	return r
}

// Handler returns the http.Handler that serves the Prometheus
// scrape endpoint. Mount it under /metrics in the Server.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}
