package operator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/layer87-labs/kube-escalate/internal/operator"
)

// collectorFor registers the active-escalations collector against a fresh
// registry backed by a fake client holding objs.
func collectorFor(t *testing.T, objs ...client.Object) *prometheus.Registry {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(operator.NewActiveEscalationsCollector(c)))
	return reg
}

const activeMetricHelp = `# HELP kube_escalate_active_escalations Number of currently active (not yet deleted) escalations, counted from live cluster state.
# TYPE kube_escalate_active_escalations gauge
`

// TestActiveEscalations_CountedFromLiveState is the regression test for the
// gauge-drift bug. An incremental gauge is only correct while the process
// owning it observes every increment: creations are counted on a binding's
// *first* reconcile (detected by the missing cleanup finalizer), so after any
// restart — rollout, leader change, kured drain — bindings that already carry
// the finalizer are never re-counted. The gauge resumed from zero while
// escalations were live, then went negative as they expired.
//
// The objects below all already carry the finalizer, i.e. exactly the state
// the operator finds after a restart having observed no creations at all.
func TestActiveEscalations_CountedFromLiveState(t *testing.T) {
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)

	crb := managedCRB("active-crb", "eike@layer87.de", "cluster-admin", future)
	crb.Finalizers = []string{operator.FinalizerName}
	rb := managedRB("tenant-acme", "active-rb", "eike@layer87.de", "edit", future)
	rb.Finalizers = []string{operator.FinalizerName}

	reg := collectorFor(t, crb, rb)

	expected := activeMetricHelp +
		`kube_escalate_active_escalations{namespace="cluster",role="cluster-admin",user="eike@layer87.de"} 1` + "\n" +
		`kube_escalate_active_escalations{namespace="ns/tenant-acme",role="edit",user="eike@layer87.de"} 1` + "\n"

	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"kube_escalate_active_escalations"))
}

// TestActiveEscalations_AggregatesPerUserRoleScope verifies that several
// bindings sharing a {user, role, scope} tuple collapse into one sample.
func TestActiveEscalations_AggregatesPerUserRoleScope(t *testing.T) {
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)

	first := managedCRB("dup-1", "eike@layer87.de", "view", future)
	second := managedCRB("dup-2", "eike@layer87.de", "view", future)
	other := managedCRB("other-user", "someone@layer87.de", "view", future)

	reg := collectorFor(t, first, second, other)

	expected := activeMetricHelp +
		`kube_escalate_active_escalations{namespace="cluster",role="view",user="eike@layer87.de"} 2` + "\n" +
		`kube_escalate_active_escalations{namespace="cluster",role="view",user="someone@layer87.de"} 1` + "\n"

	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"kube_escalate_active_escalations"))
}

// TestActiveEscalations_IgnoresUnmanagedBindings ensures the collector only
// counts kube-escalate-managed objects, never unrelated cluster RBAC.
func TestActiveEscalations_IgnoresUnmanagedBindings(t *testing.T) {
	unmanaged := &rbacv1.ClusterRoleBinding{}
	unmanaged.Name = "some-unrelated-binding"
	unmanaged.RoleRef = rbacv1.RoleRef{
		APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin",
	}

	reg := collectorFor(t, unmanaged)

	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(""),
		"kube_escalate_active_escalations"))
}

// TestActiveEscalations_EmptyClusterReportsNothing documents that no
// escalations yields no samples at all (rather than a zero series).
func TestActiveEscalations_EmptyClusterReportsNothing(t *testing.T) {
	reg := collectorFor(t)

	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(""),
		"kube_escalate_active_escalations"))
}
