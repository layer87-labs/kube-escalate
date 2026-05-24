# kube-escalate — Agent Instructions

## What this is

Kubernetes Operator + kubectl plugin for Just-in-Time privilege escalation.
Users with restricted permissions can temporarily escalate to a higher role —
time-limited, auditable, and automatically expiring via TTL.

## Tech stack

- **Language:** Go 1.23+
- **Operator:** controller-runtime, no CRD — managed ClusterRoleBindings with annotations
- **Plugin:** client-go, cobra — `kubectl escalate` subcommand
- **Build:** GoReleaser → GHCR (`ghcr.io/layer87-labs/kube-escalate`)
- **Deployment:** Helm chart, distroless Docker image
- **CI:** GitHub Actions

## Reference repository

Before writing any GitHub Actions workflow or GoReleaser config, always read:

```
../relctl/.github/workflows/
../relctl/.goreleaser.yaml
```

Copy their structure. Do not invent patterns that already exist there.

## Rules

- **Analyze first.** Always inspect the repo state and produce a gap list before writing any code.
- **Wait for confirmation** after the analysis phase and after each major phase.
- No business logic in `main.go` — wiring only.
- Wrap all errors: `fmt.Errorf("context: %w", err)`
- Logging via `slog` (structured, JSON).
- No `panic()` except fatal startup errors in `main()`.
- No secrets in code or Git.
- All exported symbols must have GoDoc comments.
- Conventional Commits: `feat:`, `fix:`, `docs:`, `chore:`

## Identity model

The plugin reads the caller's identity from the Kubernetes `SelfSubjectReview` API —
never from CLI arguments. This is tamper-proof: the API server populates it from the
validated OIDC token.

## Out of scope (v1)

- Multi-person approval workflows
- Zitadel API calls (identity comes from the OIDC token)
- Web UI or dashboard
- Krew publication (post first stable release)
- Any Layer87-specific commands