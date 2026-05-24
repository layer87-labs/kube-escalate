package plugin

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// EscalateOptions holds the parameters for the escalate command.
type EscalateOptions struct {
	// Role is the ClusterRole (cluster-wide) or Role (namespace-scoped) to bind.
	Role string
	// Namespace scopes the escalation to a RoleBinding when non-empty.
	// An empty value creates a cluster-wide ClusterRoleBinding.
	Namespace string
	// Duration is how long the escalation remains valid.
	Duration time.Duration
	// Reason is the human-readable justification stored in the annotation.
	Reason string
}

// getSelfIdentity calls the SelfSubjectReview API to obtain the caller's verified
// OIDC identity. The API server populates the response from the validated token;
// it cannot be forged by the caller.
func getSelfIdentity(ctx context.Context, cs kubernetes.Interface) (username string, groups []string, err error) {
	result, err := cs.AuthenticationV1().SelfSubjectReviews().Create(
		ctx, &authv1.SelfSubjectReview{}, metav1.CreateOptions{},
	)
	if err != nil {
		return "", nil, fmt.Errorf("getSelfIdentity: %w", err)
	}
	return result.Status.UserInfo.Username,
		result.Status.UserInfo.Groups,
		nil
}

var nonAlphanumRE = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeForName converts an arbitrary string (e.g. "eike@layer87.de") into a
// segment safe for use in a Kubernetes resource name.
func sanitizeForName(s string) string {
	clean := nonAlphanumRE.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(clean, "-")
}

// escalationName produces a unique, human-readable CRB / RB name.
func escalationName(username string) string {
	return fmt.Sprintf("kube-escalate-%s-%d",
		sanitizeForName(username), time.Now().Unix())
}

// Escalate creates an annotated ClusterRoleBinding (cluster-wide) or RoleBinding
// (namespace-scoped) that the operator will delete once the TTL elapses.
// The caller's identity is always resolved via SelfSubjectReview — never from
// CLI arguments.
func Escalate(ctx context.Context, opts EscalateOptions, cs kubernetes.Interface, w io.Writer) error {
	username, groups, err := getSelfIdentity(ctx, cs)
	if err != nil {
		return fmt.Errorf("escalate: %w", err)
	}

	name := escalationName(username)
	expiresAt := time.Now().UTC().Add(opts.Duration)

	labels := map[string]string{LabelManaged: "true"}
	annotations := map[string]string{
		AnnotationRequester:      username,
		AnnotationExpiresAt:      expiresAt.Format(time.RFC3339),
		AnnotationReason:         opts.Reason,
		AnnotationOriginalGroups: strings.Join(groups, ","),
	}

	if opts.Namespace == "" {
		return clusterEscalate(ctx, cs, name, username, opts.Role, labels, annotations, w)
	}
	return namespaceEscalate(ctx, cs, name, username, opts.Role, opts.Namespace, labels, annotations, w)
}

func clusterEscalate(
	ctx context.Context,
	cs kubernetes.Interface,
	name, username, role string,
	labels, annotations map[string]string,
	w io.Writer,
) error {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     role,
		},
		Subjects: []rbacv1.Subject{
			{APIGroup: rbacv1.GroupName, Kind: "User", Name: username},
		},
	}

	if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("clusterEscalate: %w", err)
	}

	fmt.Fprintf(w, "✓ Escalated  requester=%s  role=%s  scope=cluster  expires=%s\n",
		username, role, annotations[AnnotationExpiresAt])
	fmt.Fprintf(w, "  Resource: ClusterRoleBinding/%s\n", name)
	return nil
}

func namespaceEscalate(
	ctx context.Context,
	cs kubernetes.Interface,
	name, username, role, namespace string,
	labels, annotations map[string]string,
	w io.Writer,
) error {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     role,
		},
		Subjects: []rbacv1.Subject{
			{APIGroup: rbacv1.GroupName, Kind: "User", Name: username},
		},
	}

	if _, err := cs.RbacV1().RoleBindings(namespace).Create(ctx, rb, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("namespaceEscalate: %w", err)
	}

	fmt.Fprintf(w, "✓ Escalated  requester=%s  role=%s  scope=ns/%s  expires=%s\n",
		username, role, namespace, annotations[AnnotationExpiresAt])
	fmt.Fprintf(w, "  Resource: RoleBinding/%s/%s\n", namespace, name)
	return nil
}
