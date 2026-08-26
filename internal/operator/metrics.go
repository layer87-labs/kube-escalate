package operator

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metrics is the singleton escalation metrics instance registered with
// the controller-runtime Prometheus registry at package initialisation.
var Metrics = newEscalationMetrics()

// escalationMetrics holds the cumulative Prometheus instruments exposed by
// kube-escalate. The number of *currently active* escalations is deliberately
// not tracked here — see activeEscalationsCollector.
type escalationMetrics struct {
	escalationsTotal *prometheus.CounterVec
	expiredTotal     *prometheus.CounterVec
	revokedTotal     *prometheus.CounterVec
	durationSeconds  *prometheus.HistogramVec
}

func newEscalationMetrics() *escalationMetrics {
	m := &escalationMetrics{
		escalationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kube_escalate_escalations_total",
				Help: "Total number of escalations registered by the operator.",
			},
			[]string{"user", "role", "namespace"},
		),
		expiredTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kube_escalate_expired_total",
				Help: "Total number of escalations deleted by the TTL controller.",
			},
			[]string{"user", "role", "namespace"},
		),
		revokedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kube_escalate_revoked_total",
				Help: "Total number of escalations manually revoked via kubectl escalate revoke.",
			},
			[]string{"user", "role", "namespace"},
		),
		durationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "kube_escalate_duration_seconds",
				Help:    "Duration in seconds of completed escalations (expired or revoked).",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"user", "role", "namespace"},
		),
	}

	metrics.Registry.MustRegister(
		m.escalationsTotal,
		m.expiredTotal,
		m.revokedTotal,
		m.durationSeconds,
	)

	return m
}

// RecordCreation increments the escalations_total counter.
// Called by the operator on the first reconcile of a managed binding.
func (m *escalationMetrics) RecordCreation(user, role, namespace string) {
	m.escalationsTotal.WithLabelValues(user, role, namespace).Inc()
}

// RecordExpiry increments the expired counter and records the completed
// duration. Called by the operator reconciler during finalization.
func (m *escalationMetrics) RecordExpiry(user, role, namespace string, durationSec float64) {
	m.expiredTotal.WithLabelValues(user, role, namespace).Inc()
	m.durationSeconds.WithLabelValues(user, role, namespace).Observe(durationSec)
}

// RecordRevocation increments the revoked counter and records the completed
// duration. Called by the operator reconciler during finalization.
func (m *escalationMetrics) RecordRevocation(user, role, namespace string, durationSec float64) {
	m.revokedTotal.WithLabelValues(user, role, namespace).Inc()
	m.durationSeconds.WithLabelValues(user, role, namespace).Observe(durationSec)
}

// activeEscalationsDesc describes the kube_escalate_active_escalations gauge.
var activeEscalationsDesc = prometheus.NewDesc(
	"kube_escalate_active_escalations",
	"Number of currently active (not yet deleted) escalations, counted from live cluster state.",
	[]string{"user", "role", "namespace"},
	nil,
)

// activeEscalationsCollector reports the number of active escalations by
// counting the managed bindings that actually exist at scrape time.
//
// This deliberately does NOT maintain an incremental gauge. An incremental
// gauge is only correct as long as the process that owns it observes every
// increment and every decrement. The operator does not: creations are counted
// on a binding's *first* reconcile (identified by the absence of the cleanup
// finalizer), so after any restart — a rollout, a leader election change, a
// kured node drain — bindings that already carry the finalizer are never
// re-counted. The gauge would resume from zero while escalations are still
// active, and would then go *negative* as those escalations expired and
// decremented it.
//
// Counting from the informer cache at scrape time is drift-free by
// construction and costs a cached List, no API server round-trip.
type activeEscalationsCollector struct {
	client client.Client
}

// NewActiveEscalationsCollector returns the prometheus.Collector backing the
// kube_escalate_active_escalations gauge, counting managed bindings from c at
// scrape time.
func NewActiveEscalationsCollector(c client.Client) prometheus.Collector {
	return &activeEscalationsCollector{client: c}
}

// Describe implements prometheus.Collector.
func (c *activeEscalationsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- activeEscalationsDesc
}

// Collect implements prometheus.Collector. On a cache read error it reports
// no samples rather than a wrong number — a gap in the series is honest,
// a fabricated zero is not.
func (c *activeEscalationsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()
	type key struct{ user, role, scope string }
	counts := map[key]int{}

	var crbs rbacv1.ClusterRoleBindingList
	if err := c.client.List(ctx, &crbs, client.MatchingLabels{LabelManaged: "true"}); err != nil {
		return
	}
	for i := range crbs.Items {
		crb := &crbs.Items[i]
		counts[key{crb.Annotations[AnnotationRequester], crb.RoleRef.Name, "cluster"}]++
	}

	var rbs rbacv1.RoleBindingList
	if err := c.client.List(ctx, &rbs, client.MatchingLabels{LabelManaged: "true"}); err != nil {
		return
	}
	for i := range rbs.Items {
		rb := &rbs.Items[i]
		counts[key{rb.Annotations[AnnotationRequester], rb.RoleRef.Name, "ns/" + rb.Namespace}]++
	}

	for k, n := range counts {
		ch <- prometheus.MustNewConstMetric(
			activeEscalationsDesc,
			prometheus.GaugeValue,
			float64(n),
			k.user, k.role, k.scope,
		)
	}
}
