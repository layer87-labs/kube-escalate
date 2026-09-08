package plugin_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/layer87-labs/kube-escalate/internal/plugin"
)

// rulesCS returns a fake clientset whose SelfSubjectRulesReview answers with
// the given resource rules.
func rulesCS(t *testing.T, rules []authzv1.ResourceRule, objects ...runtime.Object) *k8sfake.Clientset {
	t.Helper()
	cs := k8sfake.NewSimpleClientset(objects...)
	cs.PrependReactor("create", "selfsubjectrulesreviews",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, &authzv1.SelfSubjectRulesReview{
				Status: authzv1.SubjectRulesReviewStatus{ResourceRules: rules},
			}, nil
		},
	)
	return cs
}

func bindRule(resource string, names ...string) authzv1.ResourceRule {
	return authzv1.ResourceRule{
		Verbs:         []string{"bind"},
		APIGroups:     []string{"rbac.authorization.k8s.io"},
		Resources:     []string{resource},
		ResourceNames: names,
	}
}

// TestTargets_ListsBindableClusterRoles is the shape observed on a live
// cluster: a single bind grant scoped by resourceNames.
func TestTargets_ListsBindableClusterRoles(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{bindRule("clusterroles", "cluster-admin")})
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))

	assert.Contains(t, out.String(), "cluster-admin")
	assert.Contains(t, out.String(), "cluster")
	assert.Contains(t, out.String(), "kubectl escalate --to")
}

// TestTargets_IgnoresUnrelatedRules ensures we filter on the bind verb and the
// RBAC API group rather than reporting every rule the user happens to hold.
func TestTargets_IgnoresUnrelatedRules(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{
		{Verbs: []string{"get", "list"}, APIGroups: []string{""}, Resources: []string{"pods"}},
		{Verbs: []string{"create"}, APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"clusterrolebindings"}},
		{Verbs: []string{"bind"}, APIGroups: []string{"apps"},
			Resources: []string{"clusterroles"}, ResourceNames: []string{"not-rbac"}},
		bindRule("clusterroles", "editor"),
	})
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))

	assert.Contains(t, out.String(), "editor")
	assert.NotContains(t, out.String(), "pods")
	assert.NotContains(t, out.String(), "clusterrolebindings")
	assert.NotContains(t, out.String(), "not-rbac",
		"a bind grant in a different API group must not be reported")
}

// TestTargets_NoGrantExplainsWhy — an empty list is the most likely first-run
// outcome (the group in the grant not matching the user's actual groups), so
// it must say what to do rather than print nothing.
func TestTargets_NoGrantExplainsWhy(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{
		{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"pods"}},
	})
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))

	assert.Contains(t, out.String(), "may not escalate")
	assert.Contains(t, out.String(), "bind")
}

// TestTargets_UnrestrictedBindIsReportedNotExpanded: a bind grant without
// resourceNames means "any role". Enumerating every ClusterRole would both
// mislead and answer a different question.
func TestTargets_UnrestrictedBindIsReportedNotExpanded(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{
		{Verbs: []string{"bind"}, APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"clusterroles"}},
	})
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))

	assert.Contains(t, out.String(), "unrestricted")
	assert.NotContains(t, out.String(), "TARGET\t", "no table for an unbounded grant")
}

// TestTargets_WhileEscalatedStillShowsScopedTargets is a regression test found
// by running the command against a live cluster while escalated: the active
// cluster-admin binding contributes verbs ["*"] on everything, which made the
// command report only "unrestricted" and swallow the actual answer — at the
// one moment a user is most likely to ask it again.
func TestTargets_WhileEscalatedStillShowsScopedTargets(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{
		// Contributed by the active escalation.
		{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
		// The standing, scoped grant.
		bindRule("clusterroles", "cluster-admin"),
	})
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))

	assert.Contains(t, out.String(), "TARGET", "the scoped table must still be shown")
	assert.Contains(t, out.String(), "cluster-admin")
	assert.Contains(t, out.String(), "unrestricted", "the wildcard is still disclosed")
	assert.Contains(t, out.String(), "escalate status",
		"points at how to see the remaining time")
}

// TestTargets_WildcardVerbCounts — cluster-admin holds verbs ["*"], which
// includes bind.
func TestTargets_WildcardVerbCounts(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{
		{Verbs: []string{"*"}, APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"clusterroles"}, ResourceNames: []string{"view"}},
	})
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))
	assert.Contains(t, out.String(), "view")
}

// TestTargets_DescriptionAnnotationIsShown covers the optional per-target
// description.
func TestTargets_DescriptionAnnotationIsShown(t *testing.T) {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cluster-admin",
			Annotations: map[string]string{plugin.AnnotationDescription: "everything, everywhere"},
		},
	}
	cs := rulesCS(t, []authzv1.ResourceRule{bindRule("clusterroles", "cluster-admin")}, cr)
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))
	assert.Contains(t, out.String(), "everything, everywhere")
}

// TestTargets_UnreadableClusterRoleStillLists — reading ClusterRoles is a
// separate permission a requester need not hold. A missing description must
// never turn a working command into an error.
func TestTargets_UnreadableClusterRoleStillLists(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{bindRule("clusterroles", "cluster-admin")})
	cs.PrependReactor("get", "clusterroles",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, assert.AnError
		},
	)
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out),
		"an unreadable ClusterRole must not fail the command")
	assert.Contains(t, out.String(), "cluster-admin")
}

// TestTargets_NamespaceScopedRoles covers --namespace.
func TestTargets_NamespaceScopedRoles(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{bindRule("roles", "edit")})
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(),
		plugin.TargetsOptions{Namespace: "tenant-acme"}, cs, &out))

	assert.Contains(t, out.String(), "edit")
	assert.Contains(t, out.String(), "ns/tenant-acme")
}

// TestTargets_EvaluationErrorIsSurfaced — partial results must not be mistaken
// for a complete list.
func TestTargets_EvaluationErrorIsSurfaced(t *testing.T) {
	cs := k8sfake.NewSimpleClientset()
	cs.PrependReactor("create", "selfsubjectrulesreviews",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, &authzv1.SelfSubjectRulesReview{
				Status: authzv1.SubjectRulesReviewStatus{
					ResourceRules:   []authzv1.ResourceRule{bindRule("clusterroles", "cluster-admin")},
					EvaluationError: "some authorizer failed",
				},
			}, nil
		},
	)
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))
	assert.Contains(t, out.String(), "warning")
	assert.Contains(t, out.String(), "some authorizer failed")
	assert.Contains(t, out.String(), "cluster-admin", "partial results are still shown")
}

// ─── max-duration column (issue #9) ──────────────────────────────────────────

func maxDurationCM(ns, value string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: plugin.ConfigMapName, Namespace: ns},
		Data:       map[string]string{plugin.ConfigKeyMaxDuration: value},
	}
}

// TestTargets_ShowsMaxDurationWhenPublished — the column appears only when the
// operator actually published the value.
func TestTargets_ShowsMaxDurationWhenPublished(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{bindRule("clusterroles", "cluster-admin")},
		maxDurationCM(plugin.DefaultOperatorNamespace, "8h"))
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))

	assert.Contains(t, out.String(), "MAX DURATION")
	assert.Contains(t, out.String(), "8h")
	assert.Contains(t, out.String(), "are not rejected")
}

// TestTargets_OmitsMaxDurationWhenAbsent covers an older chart that does not
// render the ConfigMap: the command must still work, without the column.
func TestTargets_OmitsMaxDurationWhenAbsent(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{bindRule("clusterroles", "cluster-admin")})
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))

	assert.NotContains(t, out.String(), "MAX DURATION")
	assert.Contains(t, out.String(), "cluster-admin", "the command still works")
}

// TestTargets_OmitsMaxDurationWhenMalformed — a value that does not parse must
// drop the column rather than print something misleading.
func TestTargets_OmitsMaxDurationWhenMalformed(t *testing.T) {
	cs := rulesCS(t, []authzv1.ResourceRule{bindRule("clusterroles", "cluster-admin")},
		maxDurationCM(plugin.DefaultOperatorNamespace, "eight hours"))
	var out bytes.Buffer

	require.NoError(t, plugin.Targets(context.Background(), plugin.TargetsOptions{}, cs, &out))

	assert.NotContains(t, out.String(), "MAX DURATION")
	assert.NotContains(t, out.String(), "eight hours")
}

// TestLookupMaxDuration_UnreadableIsNotAnError — a requester need not hold get
// on the ConfigMap, and that must never fail a command.
func TestLookupMaxDuration_UnreadableIsNotAnError(t *testing.T) {
	cs := k8sfake.NewSimpleClientset()
	cs.PrependReactor("get", "configmaps",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, assert.AnError
		},
	)

	_, ok := plugin.LookupMaxDuration(context.Background(), cs, "")
	assert.False(t, ok, "an unreadable ConfigMap yields no value, not an error")
}

// TestLookupMaxDuration_CustomNamespace covers --operator-namespace.
func TestLookupMaxDuration_CustomNamespace(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(maxDurationCM("platform-tools", "2h30m"))

	d, ok := plugin.LookupMaxDuration(context.Background(), cs, "platform-tools")
	require.True(t, ok)
	assert.Equal(t, 150*time.Minute, d)
}
