package obs

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DropReason represents a closed enumeration of reasons why an ingress ping was dropped.
type DropReason string

const (
	DropReasonQueueFull    DropReason = "queue_full"
	DropReasonStale        DropReason = "stale"
	DropReasonRateLimited  DropReason = "rate_limited"
	DropReasonDeduped      DropReason = "deduped"
	DropReasonBlocked      DropReason = "blocked"
	DropReasonInvalidPoW   DropReason = "invalid_pow"
	DropReasonTargetPaused DropReason = "target_paused"
)

// PushFailureCode represents a closed enumeration of Web Push failure status codes.
type PushFailureCode string

const (
	PushFailureCode400     PushFailureCode = "400"
	PushFailureCode404     PushFailureCode = "404"
	PushFailureCode410     PushFailureCode = "410"
	PushFailureCode429     PushFailureCode = "429"
	PushFailureCode5xx     PushFailureCode = "5xx"
	PushFailureCodeTimeout PushFailureCode = "timeout"
	PushFailureCodeNetErr  PushFailureCode = "net_err"
)

// Metrics holds the Phase 1 Prometheus metric set.
// All constructors enforce closed enum types to prohibit arbitrary identifier strings (Invariant 9).
type Metrics struct {
	Registry             *prometheus.Registry
	pingsAccepted        prometheus.Counter
	pingsDropped         *prometheus.CounterVec
	digestsSent          prometheus.Counter
	pushFailures         *prometheus.CounterVec
	powDifficulty        prometheus.Gauge
	sseConnections       prometheus.Gauge
	ingressLatency       prometheus.Histogram
}

// NewMetrics initializes and registers the full Phase 1 metric set.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		Registry: reg,
		pingsAccepted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "halp_pings_accepted_total",
			Help: "Total count of accepted pings placed on the ingress queue.",
		}),
		pingsDropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "halp_pings_dropped_total",
			Help: "Total count of dropped or rejected pings partitioned by closed reason.",
		}, []string{"reason"}),
		digestsSent: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "halp_digests_sent_total",
			Help: "Total count of coalesced digest notifications dispatched.",
		}),
		pushFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "halp_push_failures_total",
			Help: "Total count of push notification delivery errors partitioned by status code.",
		}, []string{"code"}),
		powDifficulty: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "halp_pow_difficulty",
			Help: "Current global proof-of-work difficulty requirement.",
		}),
		sseConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "halp_sse_connections",
			Help: "Current active SSE connections held open for desktop and TUI clients.",
		}),
		ingressLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "halp_ingress_latency_seconds",
			Help:    "Latency histogram for POST /v1/ping handler execution.",
			Buckets: []float64{0.0005, 0.001, 0.002, 0.003, 0.005, 0.01, 0.025, 0.05},
		}),
	}

	reg.MustRegister(
		m.pingsAccepted,
		m.pingsDropped,
		m.digestsSent,
		m.pushFailures,
		m.powDifficulty,
		m.sseConnections,
		m.ingressLatency,
	)

	return m
}

// IncPingsAccepted increments halp_pings_accepted_total.
func (m *Metrics) IncPingsAccepted() {
	m.pingsAccepted.Inc()
}

// IncPingsDropped increments halp_pings_dropped_total with a validated closed reason.
func (m *Metrics) IncPingsDropped(reason DropReason) {
	m.pingsDropped.WithLabelValues(string(reason)).Inc()
}

// IncDigestsSent increments halp_digests_sent_total.
func (m *Metrics) IncDigestsSent() {
	m.digestsSent.Inc()
}

// IncPushFailures increments halp_push_failures_total with a validated closed code.
func (m *Metrics) IncPushFailures(code PushFailureCode) {
	m.pushFailures.WithLabelValues(string(code)).Inc()
}

// SetPoWDifficulty sets the current difficulty gauge.
func (m *Metrics) SetPoWDifficulty(d float64) {
	m.powDifficulty.Set(d)
}

// SetSSEConnections sets the current active SSE connections gauge.
func (m *Metrics) SetSSEConnections(n float64) {
	m.sseConnections.Set(n)
}

// IncSSEConnections increments the active SSE connections gauge.
func (m *Metrics) IncSSEConnections() {
	m.sseConnections.Inc()
}

// DecSSEConnections decrements the active SSE connections gauge.
func (m *Metrics) DecSSEConnections() {
	m.sseConnections.Dec()
}

// ObserveIngressLatency records the elapsed duration of an ingress request.
func (m *Metrics) ObserveIngressLatency(d time.Duration) {
	m.ingressLatency.Observe(d.Seconds())
}

// Handler returns an http.Handler that serves the Prometheus registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
