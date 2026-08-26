package plugin_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/layer87-labs/kube-escalate/internal/plugin"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// fakeCS returns a fake Kubernetes clientset that answers SelfSubjectReview
// with the given username and groups.  All other API calls use the default
// fake behaviour (in-memory object tracker).
func fakeCS(t *testing.T, username string, groups []string, objects ...runtime.Object) *k8sfake.Clientset {
	t.Helper()
	cs := k8sfake.NewSimpleClientset(objects...)
	cs.PrependReactor("create", "selfsubjectreviews",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, &authv1.SelfSubjectReview{
				Status: authv1.SelfSubjectReviewStatus{
					UserInfo: authv1.UserInfo{
						Username: username,
						Groups:   groups,
					},
				},
			}, nil
		},
	)
	return cs
}

// managedCRB returns a ClusterRoleBinding pre-populated with kube-escalate metadata.
func managedCRB(name, requester, role, expiresAt string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				plugin.LabelManaged: "true",
			},
			Annotations: map[string]string{
				plugin.AnnotationRequester: requester,
				plugin.AnnotationExpiresAt: expiresAt,
				plugin.AnnotationReason:    "unit-test",
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

// managedRB returns a RoleBinding pre-populated with kube-escalate metadata.
func managedRB(name, namespace, requester, role, expiresAt string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				plugin.LabelManaged: "true",
			},
			Annotations: map[string]string{
				plugin.AnnotationRequester: requester,
				plugin.AnnotationExpiresAt: expiresAt,
				plugin.AnnotationReason:    "unit-test",
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

// ─── Escalate ─────────────────────────────────────────────────────────────────

func TestEscalate_ClusterWide_CreatesCRB(t *testing.T) {
	cs := fakeCS(t, "eike@layer87.de", []string{"oidc:platform-operator"})
	var out bytes.Buffer

	err := plugin.Escalate(context.Background(), plugin.EscalateOptions{
		Role:     "cluster-admin",
		Duration: time.Hour,
		Reason:   "CNPG hotfix",
	}, cs, &out)

	require.NoError(t, err)

	crbs, err := cs.RbacV1().ClusterRoleBindings().List(context.Background(),
		metav1.ListOptions{LabelSelector: plugin.LabelManaged + "=true"})
	require.NoError(t, err)
	require.Len(t, crbs.Items, 1, "exactly one CRB should have been created")

	crb := crbs.Items[0]
	assert.Equal(t, "cluster-admin", crb.RoleRef.Name)
	assert.Equal(t, "ClusterRole", crb.RoleRef.Kind)
	assert.Equal(t, "eike@layer87.de", crb.Annotations[plugin.AnnotationRequester])
	assert.Equal(t, "CNPG hotfix", crb.Annotations[plugin.AnnotationReason])
	assert.Equal(t, "oidc:platform-operator", crb.Annotations[plugin.AnnotationOriginalGroups])
	assert.NotEmpty(t, crb.Annotations[plugin.AnnotationExpiresAt])
	assert.Equal(t, "true", crb.Labels[plugin.LabelManaged])

	require.Len(t, crb.Subjects, 1)
	assert.Equal(t, "eike@layer87.de", crb.Subjects[0].Name)

	// expires-at must be approximately now + 1h
	expiresAt, err := time.Parse(time.RFC3339, crb.Annotations[plugin.AnnotationExpiresAt])
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(time.Hour), expiresAt, 5*time.Second)

	assert.Contains(t, out.String(), "eike@layer87.de")
	assert.Contains(t, out.String(), "cluster-admin")
}

func TestEscalate_NamespaceScoped_CreatesRB(t *testing.T) {
	cs := fakeCS(t, "eike@layer87.de", nil)
	var out bytes.Buffer

	err := plugin.Escalate(context.Background(), plugin.EscalateOptions{
		Role:      "editor",
		Namespace: "tenant-acme",
		Duration:  30 * time.Minute,
		Reason:    "Broken deployment",
	}, cs, &out)

	require.NoError(t, err)

	rbs, err := cs.RbacV1().RoleBindings("tenant-acme").List(context.Background(),
		metav1.ListOptions{LabelSelector: plugin.LabelManaged + "=true"})
	require.NoError(t, err)
	require.Len(t, rbs.Items, 1, "exactly one RB should have been created")

	rb := rbs.Items[0]
	assert.Equal(t, "editor", rb.RoleRef.Name)
	assert.Equal(t, "Role", rb.RoleRef.Kind)
	assert.Equal(t, "tenant-acme", rb.Namespace)
	assert.Equal(t, "eike@layer87.de", rb.Annotations[plugin.AnnotationRequester])

	expiresAt, err := time.Parse(time.RFC3339, rb.Annotations[plugin.AnnotationExpiresAt])
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), expiresAt, 5*time.Second)

	assert.Contains(t, out.String(), "tenant-acme")
	assert.Contains(t, out.String(), "editor")
}

// ─── Status ───────────────────────────────────────────────────────────────────

func TestStatus_ShowsCurrentUserEscalations(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	cs := fakeCS(t, "eike@layer87.de", nil,
		managedCRB("crb-eike", "eike@layer87.de", "cluster-admin", future),
		managedCRB("crb-other", "other@example.com", "view", future),
	)
	var out bytes.Buffer

	err := plugin.Status(context.Background(), plugin.StatusOptions{All: false}, cs, &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "eike@layer87.de")
	assert.NotContains(t, out.String(), "other@example.com",
		"foreign escalation must not appear when --all is false")
}

func TestStatus_All_ShowsEveryUser(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	cs := fakeCS(t, "eike@layer87.de", nil,
		managedCRB("crb-eike", "eike@layer87.de", "cluster-admin", future),
		managedCRB("crb-other", "other@example.com", "view", future),
	)
	var out bytes.Buffer

	// No SelfSubjectReview call expected when All=true, but fakeCS is set up anyway.
	err := plugin.Status(context.Background(), plugin.StatusOptions{All: true}, cs, &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "eike@layer87.de")
	assert.Contains(t, out.String(), "other@example.com")
}

func TestStatus_IncludesRoleBindings(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	cs := fakeCS(t, "eike@layer87.de", nil,
		managedRB("rb-eike", "tenant-acme", "eike@layer87.de", "editor", future),
	)
	var out bytes.Buffer

	err := plugin.Status(context.Background(), plugin.StatusOptions{All: false}, cs, &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "editor")
	assert.Contains(t, out.String(), "ns/tenant-acme")
}

func TestStatus_Empty_PrintsNoEscalations(t *testing.T) {
	cs := fakeCS(t, "eike@layer87.de", nil) // empty store
	var out bytes.Buffer

	err := plugin.Status(context.Background(), plugin.StatusOptions{}, cs, &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "No active escalations")
}

// ─── Revoke ───────────────────────────────────────────────────────────────────

func TestRevoke_DeletesCurrentUserCRB(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	crb := managedCRB("crb-eike", "eike@layer87.de", "cluster-admin", future)
	other := managedCRB("crb-other", "other@example.com", "view", future)
	cs := fakeCS(t, "eike@layer87.de", nil, crb, other)
	var out bytes.Buffer

	err := plugin.Revoke(context.Background(), plugin.RevokeOptions{All: false}, cs, &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "crb-eike")
	assert.NotContains(t, out.String(), "crb-other")

	// eike's CRB must be gone; other must remain.
	_, getErr := cs.RbacV1().ClusterRoleBindings().Get(
		context.Background(), "crb-eike", metav1.GetOptions{})
	assert.Error(t, getErr, "crb-eike should have been deleted")

	_, getErr = cs.RbacV1().ClusterRoleBindings().Get(
		context.Background(), "crb-other", metav1.GetOptions{})
	assert.NoError(t, getErr, "crb-other should still exist")
}

func TestRevoke_All_DeletesEverything(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	cs := fakeCS(t, "eike@layer87.de", nil,
		managedCRB("crb-eike", "eike@layer87.de", "cluster-admin", future),
		managedCRB("crb-other", "other@example.com", "view", future),
	)
	var out bytes.Buffer

	err := plugin.Revoke(context.Background(), plugin.RevokeOptions{All: true}, cs, &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "crb-eike")
	assert.Contains(t, out.String(), "crb-other")

	remaining, err := cs.RbacV1().ClusterRoleBindings().List(
		context.Background(),
		metav1.ListOptions{LabelSelector: plugin.LabelManaged + "=true"})
	require.NoError(t, err)
	assert.Empty(t, remaining.Items)
}

func TestRevoke_DeletesRoleBinding(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	rb := managedRB("rb-eike", "tenant-acme", "eike@layer87.de", "editor", future)
	cs := fakeCS(t, "eike@layer87.de", nil, rb)
	var out bytes.Buffer

	err := plugin.Revoke(context.Background(), plugin.RevokeOptions{All: false}, cs, &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "rb-eike")

	_, getErr := cs.RbacV1().RoleBindings("tenant-acme").Get(
		context.Background(), "rb-eike", metav1.GetOptions{})
	assert.Error(t, getErr, "rb-eike should have been deleted")
}

func TestRevoke_NothingToRevoke_PrintsMessage(t *testing.T) {
	cs := fakeCS(t, "eike@layer87.de", nil) // empty store
	var out bytes.Buffer

	err := plugin.Revoke(context.Background(), plugin.RevokeOptions{}, cs, &out)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "No escalations to revoke")
}
