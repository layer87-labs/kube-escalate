// Command kubectl-escalate is the kubectl plugin for Just-in-Time privilege escalation.
// This file contains only cobra wiring — no business logic lives here.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // load cloud-provider auth plugins

	"github.com/layer87-labs/kube-escalate/internal/plugin"
)

// version, commitHash, and buildDate are injected at build time via -ldflags.
var (
	version    = "dev"
	commitHash = "unknown"
	buildDate  = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// newRootCmd builds the root command, which performs the escalation itself.
// Sub-commands (status, revoke) are registered here as well.
func newRootCmd() *cobra.Command {
	var (
		kubeconfig string
		opts       plugin.EscalateOptions
		duration   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "kubectl-escalate",
		Short: "Just-in-Time privilege escalation for Kubernetes",
		Long: `kubectl-escalate grants you a time-limited, auditable privilege escalation.

It creates an annotated ClusterRoleBinding (cluster-wide) or RoleBinding
(namespace-scoped) that the kube-escalate operator automatically deletes
once the TTL elapses.

Your identity is resolved via the SelfSubjectReview API — the Kubernetes API
server populates it from your validated OIDC token and it cannot be forged.`,
		Example: `  # Cluster-wide escalation for 1 hour
  kubectl escalate --to cluster-admin --duration 1h --reason "CNPG hotfix"

  # Namespace-scoped escalation for 30 minutes
  kubectl escalate --to editor --namespace tenant-acme \
      --duration 30m --reason "Broken deployment"`,
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commitHash, buildDate),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if duration <= 0 {
				return fmt.Errorf("--duration must be positive (e.g. 1h, 30m)")
			}
			opts.Duration = duration

			cs, err := plugin.BuildClient(kubeconfig)
			if err != nil {
				return err
			}
			return plugin.Escalate(cmd.Context(), opts, cs, cmd.OutOrStdout())
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.Role, "to", "", "ClusterRole or Role to escalate to (required)")
	f.DurationVar(&duration, "duration", 0, "Escalation TTL, e.g. 1h or 30m (required)")
	f.StringVar(&opts.Reason, "reason", "", "Human-readable justification (required)")
	f.StringVarP(&opts.Namespace, "namespace", "n", "",
		"Namespace for a scoped RoleBinding; omit for a cluster-wide ClusterRoleBinding")

	_ = cmd.MarkFlagRequired("to")
	_ = cmd.MarkFlagRequired("duration")
	_ = cmd.MarkFlagRequired("reason")

	cmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "",
		"Path to kubeconfig (default: $KUBECONFIG → ~/.kube/config → in-cluster)")

	cmd.AddCommand(newStatusCmd(&kubeconfig))
	cmd.AddCommand(newRevokeCmd(&kubeconfig))
	cmd.AddCommand(newTargetsCmd(&kubeconfig))

	return cmd
}

func newStatusCmd(kubeconfig *string) *cobra.Command {
	var opts plugin.StatusOptions

	cmd := &cobra.Command{
		Use:   "status",
		Short: "List active escalations",
		Long:  "List all active (non-expired) kube-escalate bindings.",
		Example: `  # Your own escalations
  kubectl escalate status

  # All users (requires permission to list ClusterRoleBindings cluster-wide)
  kubectl escalate status --all`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cs, err := plugin.BuildClient(*kubeconfig)
			if err != nil {
				return err
			}
			return plugin.Status(cmd.Context(), opts, cs, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&opts.All, "all", false, "Show escalations for all users")
	return cmd
}

func newTargetsCmd(kubeconfig *string) *cobra.Command {
	var opts plugin.TargetsOptions

	cmd := &cobra.Command{
		Use:     "targets",
		Aliases: []string{"list"},
		Short:   "Show which roles you may escalate to",
		Long: `Show the roles you are permitted to escalate to.

The list comes from a SelfSubjectRulesReview — the API server's own
evaluation of your permissions — so it is correct regardless of how your
cluster's RBAC is put together or which group carries the grant. It needs no
permission beyond what any authenticated user already has for itself.

The maximum duration is intentionally not shown: it is enforced by the
operator (--max-duration) and the client cannot read it. A longer request is
not rejected, it is silently shortened, so a displayed value that did not
match the enforced one would be worse than none.`,
		Example: `  # Cluster-wide targets
  kubectl escalate targets

  # Include targets bindable inside a namespace
  kubectl escalate targets --namespace tenant-acme`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cs, err := plugin.BuildClient(*kubeconfig)
			if err != nil {
				return err
			}
			return plugin.Targets(cmd.Context(), opts, cs, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "",
		"Also show roles bindable within this namespace")
	return cmd
}

func newRevokeCmd(kubeconfig *string) *cobra.Command {
	var opts plugin.RevokeOptions

	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke active escalations",
		Long:  "Delete active kube-escalate bindings before their TTL elapses.",
		Example: `  # Revoke your own escalations
  kubectl escalate revoke

  # Revoke all users' escalations (requires delete on ClusterRoleBindings)
  kubectl escalate revoke --all`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cs, err := plugin.BuildClient(*kubeconfig)
			if err != nil {
				return err
			}
			return plugin.Revoke(cmd.Context(), opts, cs, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&opts.All, "all", false, "Revoke escalations for all users")
	return cmd
}
