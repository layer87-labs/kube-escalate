// Package operator implements the kube-escalate controller-runtime reconciler.
// It watches ClusterRoleBindings labelled kube-escalate/managed=true and
// deletes them once their TTL (kube-escalate/expires-at annotation) has elapsed.
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Label and annotation keys written by the plugin and read by the operator.
const (
	// LabelManaged marks a ClusterRoleBinding as managed by kube-escalate.
	LabelManaged = "kube-escalate/managed"

	// AnnotationExpiresAt holds the RFC3339 timestamp after which the CRB is deleted.
	AnnotationExpiresAt = "kube-escalate/expires-at"

	// AnnotationRequester holds the verified OIDC identity obtained via SelfSubjectReview.
	AnnotationRequester = "kube-escalate/requester"

	// AnnotationReason holds the human-readable reason supplied by the requester.
	AnnotationReason = "kube-escalate/reason"

	// AnnotationOriginalGroups holds the requester's OIDC group membership at request time.
	AnnotationOriginalGroups = "kube-escalate/original-groups"

	// FinalizerName is placed on every managed CRB on first reconcile.
	// It guarantees the operator can emit the correct event and record the correct
	// metric regardless of whether the binding is deleted by TTL or manually revoked
	// via "kubectl escalate revoke".
	FinalizerName = "kube-escalate.layer87.de/cleanup"

	// EventReasonExpired is the Kubernetes Event reason emitted when a TTL elapses.
	EventReasonExpired = "EscalationExpired"

	// EventReasonRevoked is the Kubernetes Event reason emitted on manual revocation.
	EventReasonRevoked = "EscalationRevoked"
)

// EscalationReconciler watches ClusterRoleBindings labelled
// kube-escalate/managed=true and enforces their TTL.
type EscalationReconciler struct {
	client   client.Client
	recorder kevents.EventRecorder
}

// NewEscalationReconciler constructs a ready-to-use EscalationReconciler.
func NewEscalationReconciler(c client.Client, r kevents.EventRecorder) *EscalationReconciler {
	return &EscalationReconciler{client: c, recorder: r}
}

// SetupWithManager registers the reconciler with the controller-runtime Manager.
// Only CRBs that carry kube-escalate/managed=true are enqueued.
func (r *EscalationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rbacv1.ClusterRoleBinding{}).
		WithEventFilter(predicate.NewPredicateFuncs(hasManagedLabel)).
		Complete(r)
}

// Reconcile is the core loop.
//
// Lifecycle for a managed CRB:
//
//  1. First reconcile (no finalizer) — add FinalizerName, record creation
//     metric, requeue immediately.
//  2. Subsequent reconcile (finalizer present, TTL not elapsed) — requeue just
//     after the expiry instant.
//  3. Subsequent reconcile (TTL elapsed) — call Delete; because the finalizer is
//     set the API server records DeletionTimestamp and re-enqueues rather than
//     removing the object.
//  4. Reconcile with DeletionTimestamp set — handleFinalization: emit Event,
//     record metric, remove finalizer → object is garbage collected.
func (r *EscalationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("crb", req.Name)

	var crb rbacv1.ClusterRoleBinding
	if err := r.client.Get(ctx, req.NamespacedName, &crb); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("reconcile get CRB: %w", err)
	}

	// ── Deletion in progress ────────────────────────────────────────────────
	if !crb.DeletionTimestamp.IsZero() {
		return r.handleFinalization(ctx, &crb)
	}

	// ── Parse TTL annotation ────────────────────────────────────────────────
	expiresAtRaw, ok := crb.Annotations[AnnotationExpiresAt]
	if !ok {
		logger.Info("CRB has no expires-at annotation; skipping")
		return ctrl.Result{}, nil
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresAtRaw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile parse expires-at %q: %w", expiresAtRaw, err)
	}

	// ── First reconcile: register finalizer + record creation ───────────────
	if !controllerutil.ContainsFinalizer(&crb, FinalizerName) {
		controllerutil.AddFinalizer(&crb, FinalizerName)
		if err := r.client.Update(ctx, &crb); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile add finalizer: %w", err)
		}
		Metrics.RecordCreation(
			crb.Annotations[AnnotationRequester],
			crb.RoleRef.Name,
			"cluster",
		)
		logger.Info("escalation registered",
			"requester", crb.Annotations[AnnotationRequester],
			"role", crb.RoleRef.Name,
			"expires_at", expiresAt.Format(time.RFC3339),
		)
		// Return empty Result — the Update above triggers a watch event that
		// re-enqueues the CRB without needing an explicit Requeue.
		return ctrl.Result{}, nil
	}

	// ── TTL elapsed: initiate deletion ──────────────────────────────────────
	if time.Now().UTC().After(expiresAt) {
		return r.triggerDeletion(ctx, &crb)
	}

	// ── TTL active: requeue just after expiry ───────────────────────────────
	requeueAfter := time.Until(expiresAt) + time.Second
	logger.Info("escalation active",
		"requester", crb.Annotations[AnnotationRequester],
		"expires_at", expiresAt.Format(time.RFC3339),
		"requeue_after", requeueAfter,
	)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// triggerDeletion calls Delete on the CRB. Because the finalizer is present the
// API server sets DeletionTimestamp instead of removing the object, which
// re-enqueues the CRB for handleFinalization.
func (r *EscalationReconciler) triggerDeletion(ctx context.Context, crb *rbacv1.ClusterRoleBinding) (ctrl.Result, error) {
	log.FromContext(ctx).WithValues("crb", crb.Name).Info("TTL elapsed; triggering deletion",
		"requester", crb.Annotations[AnnotationRequester],
		"role", crb.RoleRef.Name,
	)
	if err := r.client.Delete(ctx, crb); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("triggerDeletion: %w", err)
	}
	return ctrl.Result{}, nil
}

// handleFinalization runs when DeletionTimestamp is set on a managed CRB.
// It determines whether the binding expired by TTL or was manually revoked,
// emits the appropriate Kubernetes Event, records Prometheus metrics, then
// removes the finalizer so the API server can garbage-collect the object.
func (r *EscalationReconciler) handleFinalization(ctx context.Context, crb *rbacv1.ClusterRoleBinding) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(crb, FinalizerName) {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx).WithValues("crb", crb.Name)
	requester := crb.Annotations[AnnotationRequester]
	role := crb.RoleRef.Name
	duration := time.Since(crb.CreationTimestamp.Time).Seconds()

	expiresAt, err := time.Parse(time.RFC3339, crb.Annotations[AnnotationExpiresAt])
	if err != nil {
		// Malformed annotation — treat as expiry so the binding is cleaned up.
		logger.Error(err, "expires-at unparseable during finalization; treating as expired")
		expiresAt = time.Time{} // zero → already "in the past"
	}

	if time.Now().UTC().After(expiresAt) {
		logger.Info("finalizing expired escalation", "requester", requester, "role", role)
		r.recorder.Eventf(crb, nil, corev1.EventTypeWarning, EventReasonExpired,
			"Expire", "Escalation for %s to role %s has expired", requester, role)
		Metrics.RecordExpiry(requester, role, "cluster", duration)
	} else {
		logger.Info("finalizing manually revoked escalation", "requester", requester, "role", role)
		r.recorder.Eventf(crb, nil, corev1.EventTypeNormal, EventReasonRevoked,
			"Revoke", "Escalation for %s to role %s was manually revoked", requester, role)
		Metrics.RecordRevocation(requester, role, "cluster", duration)
	}

	controllerutil.RemoveFinalizer(crb, FinalizerName)
	if err := r.client.Update(ctx, crb); err != nil {
		return ctrl.Result{}, fmt.Errorf("handleFinalization remove finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// hasManagedLabel returns true when the object carries kube-escalate/managed=true.
func hasManagedLabel(obj client.Object) bool {
	return obj.GetLabels()[LabelManaged] == "true"
}
