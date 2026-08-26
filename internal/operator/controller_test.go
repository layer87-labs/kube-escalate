package operator_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/layer87-labs/kube-escalate/internal/operator"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	return s
}

func newTestSetup(t *testing.T, objs ...client.Object) (*operator.EscalationReconciler, *kevents.FakeRecorder, client.Client) {
	t.Helper()
	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	recorder := kevents.NewFakeRecorder(32)
	r := operator.NewEscalationReconciler(c, recorder)
	return r, recorder, c
}

func managedCRB(name, requester, role, expiresAt string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				operator.LabelManaged: "true",
			},
			Annotations: map[string]string{
				operator.AnnotationRequester: requester,
				operator.AnnotationExpiresAt: expiresAt,
				operator.AnnotationReason:    "unit-test escalation",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     role,
		},
		Subjects: []rbacv1.Subject{
			{APIGroup: rbacv1.GroupName, Kind: "User", Name: "oidc:" + requester},
		},
	}
}

func reconcileRequest(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}
}

func namespacedReconcileRequest(namespace, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

func managedRB(namespace, name, requester, role, expiresAt string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				operator.LabelManaged: "true",
			},
			Annotations: map[string]string{
				operator.AnnotationRequester: requester,
				operator.AnnotationExpiresAt: expiresAt,
				operator.AnnotationReason:    "unit-test escalation",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     role,
		},
		Subjects: []rbacv1.Subject{
			{APIGroup: rbacv1.GroupName, Kind: "User", Name: "oidc:" + requester},
		},
	}
}

// getRB fetches the current state of a RoleBinding from the fake store.
func getRB(t *testing.T, c client.Client, namespace, name string) (*rbacv1.RoleBinding, bool) {
	t.Helper()
	var rb rbacv1.RoleBinding
	err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, &rb)
	if errors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)
	return &rb, true
}

// getCRB fetches the current state of a CRB from the fake store.
func getCRB(t *testing.T, c client.Client, name string) (*rbacv1.ClusterRoleBinding, bool) {
	t.Helper()
	var crb rbacv1.ClusterRoleBinding
	err := c.Get(context.Background(), types.NamespacedName{Name: name}, &crb)
	if errors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)
	return &crb, true
}

// drainEvent reads one event from the recorder channel and returns it.
// Fails the test if no event is available.
func drainEvent(t *testing.T, recorder *kevents.FakeRecorder) string {
	t.Helper()
	select {
	case event := <-recorder.Events:
		return event
	default:
		t.Fatal("expected an event in the recorder but channel was empty")
		return ""
	}
}

// ─── lifecycle: expiry ───────────────────────────────────────────────────────

func TestReconcile_ExpiredCRB_FullLifecycle(t *testing.T) {
	expiredAt := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	crb := managedCRB("kube-escalate-expired-001", "eike@layer87.de", "cluster-admin", expiredAt)

	reconciler, recorder, c := newTestSetup(t, crb)
	ctx := context.Background()
	req := reconcileRequest(crb.Name)

	// ── Cycle 1: first reconcile — finalizer added, requeued ─────────────────
	result, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "cycle 1: informer re-enqueues after finalizer Update")

	updated, exists := getCRB(t, c, crb.Name)
	require.True(t, exists, "cycle 1: CRB should still exist")
	assert.Contains(t, updated.Finalizers, operator.FinalizerName,
		"cycle 1: finalizer must be present")

	// ── Cycle 2: TTL elapsed — Delete called, DeletionTimestamp set ──────────
	result, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "cycle 2: no explicit requeue needed")

	// ── Cycle 3: finalization — event emitted, finalizer removed, CRB gone ───
	result, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "cycle 3: done")

	_, exists = getCRB(t, c, crb.Name)
	assert.False(t, exists, "cycle 3: CRB must have been deleted")

	event := drainEvent(t, recorder)
	assert.Contains(t, event, operator.EventReasonExpired)
	assert.Contains(t, event, "eike@layer87.de")
}

// ─── lifecycle: active → requeue ─────────────────────────────────────────────

func TestReconcile_ActiveCRB_RequeuesAfterFinalizerAdded(t *testing.T) {
	futureAt := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	crb := managedCRB("kube-escalate-active-002", "eike@layer87.de", "cluster-admin", futureAt)

	reconciler, _, c := newTestSetup(t, crb)
	ctx := context.Background()
	req := reconcileRequest(crb.Name)

	// Cycle 1: add finalizer
	result, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "cycle 1: informer re-enqueues after finalizer Update")

	// Cycle 2: active TTL → requeue with duration
	result, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0),
		"cycle 2: should schedule a requeue")
	assert.LessOrEqual(t, result.RequeueAfter, time.Hour+2*time.Second,
		"cycle 2: requeue interval should be ≤ remaining TTL + 1 s")

	// CRB must still exist with finalizer
	updated, exists := getCRB(t, c, crb.Name)
	require.True(t, exists)
	assert.Contains(t, updated.Finalizers, operator.FinalizerName)
}

// ─── lifecycle: manual revocation ────────────────────────────────────────────

func TestReconcile_ManualRevocation_EmitsRevokedEvent(t *testing.T) {
	futureAt := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	crb := managedCRB("kube-escalate-revoke-003", "eike@layer87.de", "cluster-admin", futureAt)

	reconciler, recorder, c := newTestSetup(t, crb)
	ctx := context.Background()
	req := reconcileRequest(crb.Name)

	// Cycle 1: add finalizer
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	// Simulate "kubectl escalate revoke": plugin calls Delete.
	// The fake client respects finalizers: sets DeletionTimestamp, keeps the object.
	current, exists := getCRB(t, c, crb.Name)
	require.True(t, exists)
	require.NoError(t, c.Delete(ctx, current))

	// Confirm DeletionTimestamp is now set
	updated, exists := getCRB(t, c, crb.Name)
	require.True(t, exists, "object should still exist (finalizer blocks deletion)")
	assert.False(t, updated.DeletionTimestamp.IsZero(),
		"DeletionTimestamp must be set after Delete with finalizer")

	// Cycle 2: finalization — TTL not elapsed → EscalationRevoked
	result, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	_, exists = getCRB(t, c, crb.Name)
	assert.False(t, exists, "CRB must be gone after finalization")

	event := drainEvent(t, recorder)
	assert.Contains(t, event, operator.EventReasonRevoked)
	assert.Contains(t, event, "eike@layer87.de")
}

// ─── edge cases ──────────────────────────────────────────────────────────────

func TestReconcile_NotFoundCRB_IsIgnored(t *testing.T) {
	reconciler, _, _ := newTestSetup(t)

	result, err := reconciler.Reconcile(context.Background(), reconcileRequest("does-not-exist"))

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_CRBWithoutExpiresAt_IsSkipped(t *testing.T) {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "no-expiry-crb",
			Labels: map[string]string{operator.LabelManaged: "true"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view",
		},
	}

	reconciler, _, c := newTestSetup(t, crb)

	result, err := reconciler.Reconcile(context.Background(), reconcileRequest(crb.Name))

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "no-expiry CRB: must not requeue")

	// CRB must still exist and must NOT have had a finalizer added.
	updated, exists := getCRB(t, c, crb.Name)
	require.True(t, exists)
	assert.NotContains(t, updated.Finalizers, operator.FinalizerName,
		"no finalizer should be added when expires-at is absent")
}

func TestReconcile_MalformedExpiresAt_ReturnsError(t *testing.T) {
	crb := managedCRB("malformed-expiry", "eike@layer87.de", "cluster-admin", "not-a-timestamp")

	reconciler, _, _ := newTestSetup(t, crb)

	_, err := reconciler.Reconcile(context.Background(), reconcileRequest(crb.Name))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse expires-at")
}

// ─── namespace-scoped RoleBindings ──────────────────────────────────────────

func TestReconcile_ExpiredRB_FullLifecycle(t *testing.T) {
	expiredAt := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	rb := managedRB("tenant-acme", "kube-escalate-expired-rb-001", "eike@layer87.de", "editor", expiredAt)

	reconciler, recorder, c := newTestSetup(t, rb)
	ctx := context.Background()
	req := namespacedReconcileRequest(rb.Namespace, rb.Name)

	// Cycle 1: add finalizer.
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	updated, exists := getRB(t, c, rb.Namespace, rb.Name)
	require.True(t, exists)
	assert.Contains(t, updated.Finalizers, operator.FinalizerName)

	// Cycle 2: TTL elapsed — Delete called.
	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	// Cycle 3: finalization.
	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	_, exists = getRB(t, c, rb.Namespace, rb.Name)
	assert.False(t, exists, "RB must have been deleted — this is the fix for the namespace-scoped TTL gap")

	event := drainEvent(t, recorder)
	assert.Contains(t, event, operator.EventReasonExpired)
	assert.Contains(t, event, "eike@layer87.de")
}

func TestReconcile_ActiveRB_RequeuesAfterFinalizerAdded(t *testing.T) {
	futureAt := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	rb := managedRB("tenant-acme", "kube-escalate-active-rb-002", "eike@layer87.de", "editor", futureAt)

	reconciler, _, c := newTestSetup(t, rb)
	ctx := context.Background()
	req := namespacedReconcileRequest(rb.Namespace, rb.Name)

	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	result, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	updated, exists := getRB(t, c, rb.Namespace, rb.Name)
	require.True(t, exists)
	assert.Contains(t, updated.Finalizers, operator.FinalizerName)
}

// ─── max-duration clamp ──────────────────────────────────────────────────────

func TestReconcile_RequestBeyondMaxDuration_IsClamped(t *testing.T) {
	requestedAt := time.Now().UTC().Add(1000 * time.Hour).Format(time.RFC3339)
	crb := managedCRB("kube-escalate-toolong-004", "eike@layer87.de", "cluster-admin", requestedAt)

	reconciler, recorder, c := newTestSetup(t, crb)
	reconciler.MaxDuration = 1 * time.Hour
	ctx := context.Background()
	req := reconcileRequest(crb.Name)

	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	updated, exists := getCRB(t, c, crb.Name)
	require.True(t, exists)
	clampedExpiry, err := time.Parse(time.RFC3339, updated.Annotations[operator.AnnotationExpiresAt])
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().UTC().Add(1*time.Hour), clampedExpiry, 5*time.Second,
		"expires-at must be clamped to ~now+MaxDuration, not the requested 1000h")

	event := drainEvent(t, recorder)
	assert.Contains(t, event, operator.EventReasonClamped)
}

func TestReconcile_RequestWithinMaxDuration_IsUnchanged(t *testing.T) {
	requestedAt := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339)
	crb := managedCRB("kube-escalate-fine-005", "eike@layer87.de", "view", requestedAt)

	reconciler, recorder, c := newTestSetup(t, crb)
	reconciler.MaxDuration = 1 * time.Hour
	ctx := context.Background()
	req := reconcileRequest(crb.Name)

	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	updated, exists := getCRB(t, c, crb.Name)
	require.True(t, exists)
	assert.Equal(t, requestedAt, updated.Annotations[operator.AnnotationExpiresAt])
	assert.Empty(t, recorder.Events, "no clamp event expected when the request is within bounds")
}

func TestReconcile_AlreadyDeletedCRB_IsNoOp(t *testing.T) {
	reconciler, recorder, _ := newTestSetup(t) // empty store

	result, err := reconciler.Reconcile(context.Background(), reconcileRequest("already-gone"))

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Empty(t, recorder.Events)
}
