package plugin

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// TargetsOptions controls the behaviour of the targets command.
type TargetsOptions struct {
	// Namespace scopes the query to namespaced Role targets. Empty asks about
	// cluster-wide ClusterRole targets only.
	Namespace string

	// OperatorNamespace is where the operator's published configuration is
	// read from. Empty means DefaultOperatorNamespace.
	OperatorNamespace string
}

// Target is one role the caller is permitted to escalate to.
type Target struct {
	// Name is the ClusterRole or Role name.
	Name string
	// Scope is "cluster" for a ClusterRole, or "ns/<namespace>" for a Role.
	Scope string
	// Description is taken from the target role's kube-escalate/description
	// annotation when present and readable, otherwise empty.
	Description string
}

// Targets lists the roles the caller may escalate to, as evaluated by the
// API server.
//
// The permitted targets are derived from a SelfSubjectRulesReview rather than
// by reading the caller's ClusterRole directly. That matters for three
// reasons: it is the API server's own evaluation, so it stays correct across
// multiple roles, aggregation, and whichever group carries the grant; it
// needs no permission beyond what any authenticated user already has for
// itself; and it does not assume the cluster's RBAC is laid out the way any
// particular operator laid it out.
//
// A permitted target is a rule granting the `bind` verb on `clusterroles`
// (or `roles`) restricted by resourceNames. An unrestricted `bind` grant is
// reported explicitly rather than silently expanded, because the set of
// bindable roles is then "every role in the cluster" and enumerating it would
// be both misleading and a different question.
func Targets(ctx context.Context, opts TargetsOptions, cs kubernetes.Interface, w io.Writer) error {
	review := &authzv1.SelfSubjectRulesReview{
		Spec: authzv1.SelfSubjectRulesReviewSpec{
			// The API server requires a namespace for the review. For
			// cluster-scoped questions any namespace yields the same
			// cluster-scoped rules; "default" is the conventional choice.
			Namespace: namespaceOrDefault(opts.Namespace),
		},
	}

	result, err := cs.AuthorizationV1().SelfSubjectRulesReviews().Create(
		ctx, review, metav1.CreateOptions{},
	)
	if err != nil {
		return fmt.Errorf("targets: %w", err)
	}
	if result.Status.EvaluationError != "" {
		// Partial results are still worth showing, but the user must know the
		// list may be incomplete rather than assume it is exhaustive.
		fmt.Fprintf(w, "warning: the API server could not fully evaluate your permissions: %s\n\n",
			result.Status.EvaluationError)
	}

	targets, unrestricted := collectTargets(result.Status.ResourceRules, opts.Namespace)

	if len(targets) == 0 {
		if unrestricted {
			fmt.Fprintln(w,
				"You hold an unrestricted bind grant: you may escalate to ANY role in scope.")
			fmt.Fprintln(w,
				"No list is shown because it would be every role in the cluster.")
			return nil
		}
		fmt.Fprintln(w, "You may not escalate to any role.")
		fmt.Fprintln(w,
			"This is an RBAC question, not a kube-escalate one: someone must grant your")
		fmt.Fprintln(w,
			"group the 'bind' verb on the roles you should be able to request.")
		return nil
	}

	// An unrestricted grant alongside scoped ones is the normal state *while
	// already escalated*: the active binding contributes verbs ["*"] on
	// everything. Suppressing the table there would hide the answer at the
	// one moment the user is most likely to ask again, so the scoped targets
	// are always shown and the wildcard is reported as a note.
	if unrestricted {
		fmt.Fprintln(w,
			"Note: you currently also hold an unrestricted bind grant, so the effective")
		fmt.Fprintln(w,
			"set is every role in scope. If you are mid-escalation that is expected and")
		fmt.Fprintln(w,
			"ends with it — 'kubectl escalate status' shows the remaining time.")
		fmt.Fprintln(w)
	}

	annotateDescriptions(ctx, cs, targets)

	// The ceiling is shown only when it can actually be established. When it
	// cannot, the column is dropped rather than filled with a guess — a
	// request beyond the ceiling is silently shortened, not rejected, so a
	// wrong number would let someone plan around a deadline that will not
	// hold.
	maxDuration, haveMax := LookupMaxDuration(ctx, cs, opts.OperatorNamespace)

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	header := "TARGET\tSCOPE\tDESCRIPTION"
	if haveMax {
		header = "TARGET\tSCOPE\tMAX DURATION\tDESCRIPTION"
	}
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return fmt.Errorf("targets: write header: %w", err)
	}
	for _, t := range targets {
		var err error
		if haveMax {
			_, err = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, t.Scope, maxDuration, t.Description)
		} else {
			_, err = fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, t.Scope, t.Description)
		}
		if err != nil {
			return fmt.Errorf("targets: write row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("targets: flush output: %w", err)
	}

	if haveMax {
		fmt.Fprintf(w,
			"\nRequests longer than %s are not rejected — they are shortened to it.\n", maxDuration)
	}
	fmt.Fprintln(w, "\nEscalate with:  kubectl escalate --to <TARGET> --duration <d> --reason <why>")
	return nil
}

// namespaceOrDefault returns ns, or "default" when empty.
func namespaceOrDefault(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// collectTargets extracts bindable role names from the reviewed rules.
// It returns the targets sorted by name, and whether an unrestricted bind
// grant (no resourceNames) was found.
func collectTargets(rules []authzv1.ResourceRule, namespace string) ([]Target, bool) {
	seen := map[string]Target{}
	unrestricted := false

	for _, rule := range rules {
		if !containsAny(rule.Verbs, "bind", "*") {
			continue
		}
		if !containsAny(rule.APIGroups, "rbac.authorization.k8s.io", "*") {
			continue
		}

		clusterScoped := containsAny(rule.Resources, "clusterroles", "*")
		namespaceScoped := containsAny(rule.Resources, "roles", "*")
		if !clusterScoped && !namespaceScoped {
			continue
		}

		if len(rule.ResourceNames) == 0 {
			unrestricted = true
			continue
		}

		for _, name := range rule.ResourceNames {
			// A ClusterRole may also be bound namespace-scoped via a
			// RoleBinding, so a cluster-scoped grant is reported under the
			// requested namespace too when one was given.
			if clusterScoped {
				scope := "cluster"
				if namespace != "" {
					scope = "cluster or ns/" + namespace
				}
				seen[scope+"/"+name] = Target{Name: name, Scope: scope}
			}
			if namespaceScoped && namespace != "" {
				scope := "ns/" + namespace
				seen[scope+"/"+name] = Target{Name: name, Scope: scope}
			}
		}
	}

	targets := make([]Target, 0, len(seen))
	for _, t := range seen {
		targets = append(targets, t)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Name != targets[j].Name {
			return targets[i].Name < targets[j].Name
		}
		return targets[i].Scope < targets[j].Scope
	})
	return targets, unrestricted
}

// annotateDescriptions fills in each target's Description from the target
// ClusterRole's kube-escalate/description annotation. Failure is silent by
// design: reading ClusterRoles is a separate permission that a requester is
// not required to hold, and a missing description must not turn a working
// command into an error.
func annotateDescriptions(ctx context.Context, cs kubernetes.Interface, targets []Target) {
	for i := range targets {
		cr, err := cs.RbacV1().ClusterRoles().Get(ctx, targets[i].Name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		targets[i].Description = cr.Annotations[AnnotationDescription]
	}
}

// containsAny reports whether haystack contains at least one of needles.
func containsAny(haystack []string, needles ...string) bool {
	for _, h := range haystack {
		for _, n := range needles {
			if strings.EqualFold(h, n) {
				return true
			}
		}
	}
	return false
}
