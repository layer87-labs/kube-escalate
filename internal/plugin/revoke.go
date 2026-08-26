package plugin

import (
	"context"
	"fmt"
	"io"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RevokeOptions controls the behaviour of the revoke command.
type RevokeOptions struct {
	// All revokes escalations for every user, not just the caller.
	All bool
}

// Revoke deletes active kube-escalate ClusterRoleBindings and RoleBindings before
// their TTL elapses. When opts.All is false only the current user's escalations
// are removed.  Output is written to w.
func Revoke(ctx context.Context, opts RevokeOptions, cs kubernetes.Interface, w io.Writer) error {
	currentUser := ""
	if !opts.All {
		u, _, err := getSelfIdentity(ctx, cs)
		if err != nil {
			return fmt.Errorf("revoke: %w", err)
		}
		currentUser = u
	}

	selector := LabelManaged + "=true"
	revoked := 0

	// ── cluster-wide escalations ──────────────────────────────────────────────
	crbs, err := cs.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return fmt.Errorf("revoke: list ClusterRoleBindings: %w", err)
	}

	for i := range crbs.Items {
		crb := &crbs.Items[i]
		if currentUser != "" && crb.Annotations[AnnotationRequester] != currentUser {
			continue
		}
		if err := cs.RbacV1().ClusterRoleBindings().Delete(
			ctx, crb.Name, metav1.DeleteOptions{},
		); err != nil {
			return fmt.Errorf("revoke: delete ClusterRoleBinding %s: %w", crb.Name, err)
		}
		if _, err := fmt.Fprintf(w, "✓ Revoked  ClusterRoleBinding/%s  (%s → %s)\n",
			crb.Name, crb.Annotations[AnnotationRequester], crb.RoleRef.Name); err != nil {
			return fmt.Errorf("revoke: write output: %w", err)
		}
		revoked++
	}

	// ── namespace-scoped escalations ──────────────────────────────────────────
	rbs, err := cs.RbacV1().RoleBindings(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return fmt.Errorf("revoke: list RoleBindings: %w", err)
	}

	for i := range rbs.Items {
		rb := &rbs.Items[i]
		if currentUser != "" && rb.Annotations[AnnotationRequester] != currentUser {
			continue
		}
		if err := cs.RbacV1().RoleBindings(rb.Namespace).Delete(
			ctx, rb.Name, metav1.DeleteOptions{},
		); err != nil {
			return fmt.Errorf("revoke: delete RoleBinding %s/%s: %w", rb.Namespace, rb.Name, err)
		}
		if _, err := fmt.Fprintf(w, "✓ Revoked  RoleBinding/%s/%s  (%s → %s)\n",
			rb.Namespace, rb.Name, rb.Annotations[AnnotationRequester], rb.RoleRef.Name); err != nil {
			return fmt.Errorf("revoke: write output: %w", err)
		}
		revoked++
	}

	if revoked == 0 {
		if _, err := fmt.Fprintln(w, "No escalations to revoke."); err != nil {
			return fmt.Errorf("revoke: write output: %w", err)
		}
	}
	return nil
}
