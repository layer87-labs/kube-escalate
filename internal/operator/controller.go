// Package operator implements the kube-escalate controller-runtime reconciler.
// It watches ClusterRoleBindings and RoleBindings labelled
// kube-escalate/managed=true and deletes them once their TTL
// (kube-escalate/expires-at annotation) has elapsed.
package operator

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Label and annotation keys written by the plugin and read by the operator.
const (
	// LabelManaged marks a ClusterRoleBinding/RoleBinding as managed by kube-escalate.
	LabelManaged = "kube-escalate/managed"

	// AnnotationExpiresAt holds the RFC3339 timestamp after which the binding is deleted.
	AnnotationExpiresAt = "kube-escalate/expires-at"

	// AnnotationRequester holds the verified OIDC identity obtained via SelfSubjectReview.
	AnnotationRequester = "kube-escalate/requester"

	// AnnotationReason holds the human-readable reason supplied by the requester.
	AnnotationReason = "kube-escalate/reason"

	// AnnotationOriginalGroups holds the requester's OIDC group membership at request time.
	AnnotationOriginalGroups = "kube-escalate/original-groups"

	// FinalizerName is placed on every managed CRB/RB on first reconcile.
	// It guarantees the operator can emit the correct event and record the correct
	// metric regardless of whether the binding is deleted by TTL or manually revoked
	// via "kubectl escalate revoke".
	FinalizerName = "kube-escalate.layer87.de/cleanup"

	// EventReasonExpired is the Kubernetes Event reason emitted when a TTL elapses.
	EventReasonExpired = "EscalationExpired"

	// EventReasonRevoked is the Kubernetes Event reason emitted on manual revocation.
	EventReasonRevoked = "EscalationRevoked"

	// EventReasonClamped is the Kubernetes Event reason emitted when a requested
	// TTL exceeded MaxDuration and was shortened.
	EventReasonClamped = "EscalationClamped"

	// DefaultMaxDuration is used when EscalationReconciler.MaxDuration is zero.
	DefaultMaxDuration = 24 * time.Hour
)

// EscalationReconciler watches ClusterRoleBindings and RoleBindings labelled
// kube-escalate/managed=true and enforces their TTL.
type EscalationReconciler struct {
	client   client.Client
	recorder kevents.EventRecorder

	// MaxDuration caps how far in the future expires-at may be. Requests beyond
	// this are clamped to now+MaxDuration on first reconcile. Zero means
	// DefaultMaxDuration.
	MaxDuration time.Duration
}

// NewEscalationReconciler constructs a ready-to-use EscalationReconciler.
func NewEscalationReconciler(c client.Client, r kevents.EventRecorder) *EscalationReconciler {
	return &EscalationReconciler{client: c, recorder: r}
}

// SetupWithManager registers the reconciler with the controller-runtime Manager.
// Only CRBs/RBs that carry kube-escalate/managed=true are enqueued.
func (r *EscalationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// The active-escalations gauge is collected from the manager's cache at
	// scrape time rather than maintained incrementally — see
	// activeEscalationsCollector for why. Registered here because it needs a
	// client, which does not exist at package-init time.
	if err := metrics.Registry.Register(NewActiveEscalationsCollector(mgr.GetClient())); err != nil {
		return fmt.Errorf("SetupWithManager register active-escalations collector: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&rbacv1.ClusterRoleBinding{}).
		WithEventFilter(predicate.NewPredicateFuncs(hasManagedLabel)).
		Watches(
			&rbacv1.RoleBinding{},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(predicate.NewPredicateFuncs(hasManagedLabel)),
		).
		Complete(r)
}

// maxDuration returns the configured cap, or DefaultMaxDuration if unset.
func (r *EscalationReconciler) maxDuration() time.Duration {
	if r.MaxDuration <= 0 {
		return DefaultMaxDuration
	}
	return r.MaxDuration
}

// Reconcile is the core loop. req.Namespace is empty for cluster-scoped
// ClusterRoleBindings and non-empty for namespaced RoleBindings — the two
// kinds never collide because the informer cache keys on Namespace, so this
// alone is sufficient to route to the right Get/Delete calls.
//
// Lifecycle for a managed CRB/RB:
//
//  1. First reconcile (no finalizer) — compute the effective (possibly
//     MaxDuration-clamped) expiry, add FinalizerName, record creation
//     metric, requeue immediately. The expires-at annotation itself is
//     never rewritten (see the clamp comment below for why).
//  2. Subsequent reconcile (finalizer present, TTL not elapsed) — requeue just
//     after the expiry instant.
//  3. Subsequent reconcile (TTL elapsed) — call Delete; because the finalizer is
//     set the API server records DeletionTimestamp and re-enqueues rather than
//     removing the object.
//  4. Reconcile with DeletionTimestamp set — handleFinalization: emit Event,
//     record metric, remove finalizer → object is garbage collected.
func (r *EscalationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("name", req.Name, "namespace", req.Namespace)

	if req.Namespace == "" {
		var crb rbacv1.ClusterRoleBinding
		if err := r.client.Get(ctx, req.NamespacedName, &crb); err != nil {
			if errors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("reconcile get ClusterRoleBinding: %w", err)
		}
		return r.reconcileBinding(ctx, logger, &crb, crb.RoleRef.Name, "cluster")
	}

	var rb rbacv1.RoleBinding
	if err := r.client.Get(ctx, req.NamespacedName, &rb); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("reconcile get RoleBinding: %w", err)
	}
	return r.reconcileBinding(ctx, logger, &rb, rb.RoleRef.Name, "ns/"+rb.Namespace)
}

// reconcileBinding holds the kind-agnostic lifecycle logic shared by
// ClusterRoleBindings and RoleBindings. obj must be either.
func (r *EscalationReconciler) reconcileBinding(
	ctx context.Context,
	logger interface {
		Info(msg string, kv ...any)
		Error(err error, msg string, kv ...any)
	},
	obj client.Object,
	role, scope string,
) (ctrl.Result, error) {
	// ── Deletion in progress ────────────────────────────────────────────────
	if !obj.GetDeletionTimestamp().IsZero() {
		return r.handleFinalization(ctx, logger, obj, role, scope)
	}

	// ── Parse TTL annotation ────────────────────────────────────────────────
	expiresAtRaw, ok := obj.GetAnnotations()[AnnotationExpiresAt]
	if !ok {
		logger.Info("binding has no expires-at annotation; skipping")
		return ctrl.Result{}, nil
	}

	requestedExpiresAt, err := time.Parse(time.RFC3339, expiresAtRaw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile parse expires-at %q: %w", expiresAtRaw, err)
	}

	// The effective expiry is computed from CreationTimestamp+MaxDuration on
	// every reconcile rather than ever being written back to the object's
	// expires-at annotation. Any Update to a managed binding — even one that
	// only touches an unrelated annotation — is validated by the API server
	// as if it were granting the binding's RoleRef, so an operator whose own
	// ServiceAccount doesn't hold that role's permissions gets rejected. The
	// one exception, observed reliably in production, is a finalizer-only
	// Update (AddFinalizer/RemoveFinalizer below) — Kubernetes exempts pure
	// finalizer changes from that check. Recomputing the cap on the fly
	// keeps every Update finalizer-only, so this never depends on the
	// operator holding the requested role's own permissions.
	clamped := false
	expiresAt := requestedExpiresAt
	if ceiling := obj.GetCreationTimestamp().Time.UTC().Add(r.maxDuration()); requestedExpiresAt.After(ceiling) {
		expiresAt = ceiling
		clamped = true
	}

	// ── First reconcile: register finalizer + record creation ───────────────
	if !controllerutil.ContainsFinalizer(obj, FinalizerName) {
		if clamped {
			logger.Info("requested TTL exceeds max-duration; clamping",
				"requester", obj.GetAnnotations()[AnnotationRequester],
				"requested_expires_at", requestedExpiresAt.Format(time.RFC3339),
				"clamped_expires_at", expiresAt.Format(time.RFC3339),
			)
			r.recorder.Eventf(obj, nil, corev1.EventTypeWarning, EventReasonClamped,
				"Clamp", "Requested TTL for %s exceeded the maximum of %s; effective expiry is %s (expires-at annotation is not rewritten)",
				obj.GetAnnotations()[AnnotationRequester], r.maxDuration(), expiresAt.Format(time.RFC3339))
		}

		controllerutil.AddFinalizer(obj, FinalizerName)
		if err := r.client.Update(ctx, obj); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile add finalizer: %w", err)
		}
		Metrics.RecordCreation(
			obj.GetAnnotations()[AnnotationRequester],
			role,
			scope,
		)
		logger.Info("escalation registered",
			"requester", obj.GetAnnotations()[AnnotationRequester],
			"role", role,
			"scope", scope,
			"expires_at", expiresAt.Format(time.RFC3339),
		)
		// Return empty Result — the Update above triggers a watch event that
		// re-enqueues the object without needing an explicit Requeue.
		return ctrl.Result{}, nil
	}

	// ── TTL elapsed: initiate deletion ──────────────────────────────────────
	if time.Now().UTC().After(expiresAt) {
		return r.triggerDeletion(ctx, logger, obj, role)
	}

	// ── TTL active: requeue just after expiry ───────────────────────────────
	requeueAfter := time.Until(expiresAt) + time.Second
	logger.Info("escalation active",
		"requester", obj.GetAnnotations()[AnnotationRequester],
		"expires_at", expiresAt.Format(time.RFC3339),
		"requeue_after", requeueAfter,
	)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// triggerDeletion calls Delete on the binding. Because the finalizer is present
// the API server sets DeletionTimestamp instead of removing the object, which
// re-enqueues it for handleFinalization.
func (r *EscalationReconciler) triggerDeletion(
	ctx context.Context,
	logger interface {
		Info(msg string, kv ...any)
		Error(err error, msg string, kv ...any)
	},
	obj client.Object,
	role string,
) (ctrl.Result, error) {
	logger.Info("TTL elapsed; triggering deletion",
		"requester", obj.GetAnnotations()[AnnotationRequester],
		"role", role,
	)
	if err := r.client.Delete(ctx, obj); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("triggerDeletion: %w", err)
	}
	return ctrl.Result{}, nil
}

// handleFinalization runs when DeletionTimestamp is set on a managed binding.
// It determines whether the binding expired by TTL or was manually revoked,
// emits the appropriate Kubernetes Event, records Prometheus metrics, then
// removes the finalizer so the API server can garbage-collect the object.
func (r *EscalationReconciler) handleFinalization(
	ctx context.Context,
	logger interface {
		Info(msg string, kv ...any)
		Error(err error, msg string, kv ...any)
	},
	obj client.Object,
	role, scope string,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(obj, FinalizerName) {
		return ctrl.Result{}, nil
	}

	annotations := obj.GetAnnotations()
	requester := annotations[AnnotationRequester]
	reason := annotations[AnnotationReason]
	duration := time.Since(obj.GetCreationTimestamp().Time).Seconds()

	expiresAt, err := time.Parse(time.RFC3339, annotations[AnnotationExpiresAt])
	if err != nil {
		// Malformed annotation — treat as expiry so the binding is cleaned up.
		logger.Error(err, "expires-at unparseable during finalization; treating as expired")
		expiresAt = time.Time{} // zero → already "in the past"
	}
	if ceiling := obj.GetCreationTimestamp().Time.UTC().Add(r.maxDuration()); expiresAt.After(ceiling) {
		expiresAt = ceiling
	}

	if time.Now().UTC().After(expiresAt) {
		logger.Info("finalizing expired escalation", "requester", requester, "role", role, "scope", scope)
		r.recorder.Eventf(obj, nil, corev1.EventTypeWarning, EventReasonExpired,
			"Expire", "Escalation for %s to role %s (scope=%s, reason=%q) has expired after %.0fs",
			requester, role, scope, reason, duration)
		Metrics.RecordExpiry(requester, role, scope, duration)
	} else {
		logger.Info("finalizing manually revoked escalation", "requester", requester, "role", role, "scope", scope)
		r.recorder.Eventf(obj, nil, corev1.EventTypeNormal, EventReasonRevoked,
			"Revoke", "Escalation for %s to role %s (scope=%s, reason=%q) was manually revoked after %.0fs",
			requester, role, scope, reason, duration)
		Metrics.RecordRevocation(requester, role, scope, duration)
	}

	controllerutil.RemoveFinalizer(obj, FinalizerName)
	if err := r.client.Update(ctx, obj); err != nil {
		return ctrl.Result{}, fmt.Errorf("handleFinalization remove finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// hasManagedLabel returns true when the object carries kube-escalate/managed=true.
func hasManagedLabel(obj client.Object) bool {
	return obj.GetLabels()[LabelManaged] == "true"
}
