package operator

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metrics is the singleton escalation metrics instance registered with
// the controller-runtime Prometheus registry at package initialisation.
var Metrics = newEscalationMetrics()

// escalationMetrics holds all Prometheus instruments exposed by kube-escalate.
type escalationMetrics struct {
	activeEscalations *prometheus.GaugeVec
	escalationsTotal  *prometheus.CounterVec
	expiredTotal      *prometheus.CounterVec
	revokedTotal      *prometheus.CounterVec
	durationSeconds   *prometheus.HistogramVec
}

func newEscalationMetrics() *escalationMetrics {
	m := &escalationMetrics{
		activeEscalations: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "kube_escalate_active_escalations",
				Help: "Number of currently active (non-expired) escalations.",
			},
			[]string{"user", "role", "namespace"},
		),
		escalationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kube_escalate_escalations_total",
				Help: "Total number of escalations created via the plugin.",
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
		m.activeEscalations,
		m.escalationsTotal,
		m.expiredTotal,
		m.revokedTotal,
		m.durationSeconds,
	)

	return m
}

// RecordCreation increments the escalations_total counter and the active gauge.
// Called by the plugin after successfully creating an escalated CRB.
func (m *escalationMetrics) RecordCreation(user, role, namespace string) {
	m.escalationsTotal.WithLabelValues(user, role, namespace).Inc()
	m.activeEscalations.WithLabelValues(user, role, namespace).Inc()
}

// RecordExpiry decrements the active gauge, increments the expired counter,
// and records the completed duration. Called by the operator reconciler.
func (m *escalationMetrics) RecordExpiry(user, role, namespace string, durationSec float64) {
	m.expiredTotal.WithLabelValues(user, role, namespace).Inc()
	m.activeEscalations.WithLabelValues(user, role, namespace).Dec()
	m.durationSeconds.WithLabelValues(user, role, namespace).Observe(durationSec)
}

// RecordRevocation decrements the active gauge, increments the revoked counter,
// and records the completed duration. Called by the plugin revoke command.
func (m *escalationMetrics) RecordRevocation(user, role, namespace string, durationSec float64) {
	m.revokedTotal.WithLabelValues(user, role, namespace).Inc()
	m.activeEscalations.WithLabelValues(user, role, namespace).Dec()
	m.durationSeconds.WithLabelValues(user, role, namespace).Observe(durationSec)
}
