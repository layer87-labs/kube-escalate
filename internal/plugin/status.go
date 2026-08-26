package plugin

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// StatusOptions controls the behaviour of the status command.
type StatusOptions struct {
	// All shows escalations for every user, not just the caller.
	All bool
}

// Status lists active kube-escalate ClusterRoleBindings and RoleBindings.
// When opts.All is false only escalations belonging to the current user are shown.
// Output is written to w in a human-readable tabular format.
func Status(ctx context.Context, opts StatusOptions, cs kubernetes.Interface, w io.Writer) error {
	currentUser := ""
	if !opts.All {
		u, _, err := getSelfIdentity(ctx, cs)
		if err != nil {
			return fmt.Errorf("status: %w", err)
		}
		currentUser = u
	}

	selector := LabelManaged + "=true"

	crbs, err := cs.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return fmt.Errorf("status: list ClusterRoleBindings: %w", err)
	}

	rbs, err := cs.RbacV1().RoleBindings(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return fmt.Errorf("status: list RoleBindings: %w", err)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "REQUESTER\tROLE\tSCOPE\tEXPIRES AT\tREMAINING"); err != nil {
		return fmt.Errorf("status: write header: %w", err)
	}

	now := time.Now().UTC()
	rows := 0

	for i := range crbs.Items {
		crb := &crbs.Items[i]
		requester := crb.Annotations[AnnotationRequester]
		if currentUser != "" && requester != currentUser {
			continue
		}
		expiresAt := crb.Annotations[AnnotationExpiresAt]
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			requester, crb.RoleRef.Name, "cluster", expiresAt, remainingTTL(expiresAt, now)); err != nil {
			return fmt.Errorf("status: write row: %w", err)
		}
		rows++
	}

	for i := range rbs.Items {
		rb := &rbs.Items[i]
		requester := rb.Annotations[AnnotationRequester]
		if currentUser != "" && requester != currentUser {
			continue
		}
		expiresAt := rb.Annotations[AnnotationExpiresAt]
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			requester, rb.RoleRef.Name, "ns/"+rb.Namespace, expiresAt, remainingTTL(expiresAt, now)); err != nil {
			return fmt.Errorf("status: write row: %w", err)
		}
		rows++
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("status: flush output: %w", err)
	}

	if rows == 0 {
		if _, err := fmt.Fprintln(w, "No active escalations."); err != nil {
			return fmt.Errorf("status: write output: %w", err)
		}
	}
	return nil
}

// remainingTTL returns a human-readable duration until expiresAt, or "EXPIRED".
func remainingTTL(expiresAt string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return "unknown"
	}
	d := t.Sub(now)
	if d <= 0 {
		return "EXPIRED"
	}
	return d.Round(time.Second).String()
}
