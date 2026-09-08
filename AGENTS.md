# kube-escalate — Agent Instructions

## What this is

Kubernetes Operator + kubectl plugin for Just-in-Time privilege escalation.
Users with restricted permissions can temporarily escalate to a higher role —
time-limited, auditable, and automatically expiring via TTL.

No CRDs. No database. No external dependencies. State lives entirely in
annotated `ClusterRoleBinding` / `RoleBinding` objects.

## Repository layout

```
cmd/
  operator/            → Operator entry point (wiring only)
  kubectl-escalate/    → Plugin entry point (wiring only)
internal/
  operator/            → controller-runtime reconciler, Prometheus metrics
  plugin/              → escalate / targets / status / revoke commands,
                         SelfSubjectReview identity
deploy/
  Containerfile        → Chainguard static base, no multi-stage build
  helm/                → Helm chart (Deployment, RBAC, PDB, NetworkPolicy, ServiceMonitor)
  crds/                → Reserved, empty (no CRDs in v1)
docs/                  → install.md, usage.md, architecture.md
.github/workflows/     → ci.yaml (relctl generate-build-infos), release.yaml (relctl pipeline)
.goreleaser.yaml       → Local snapshot builds only — NOT used in CI release
```

## Tech stack

| Concern | Tool / version |
|---|---|
| Language | Go 1.26 |
| Operator framework | `sigs.k8s.io/controller-runtime` v0.24.1 |
| Plugin client | `k8s.io/client-go` v0.36.0 |
| CLI | `github.com/spf13/cobra` v1.10.2 |
| Container base | `cgr.dev/chainguard/static:latest` (UID 65532) |
| Deployment | Helm chart (OCI: `ghcr.io/layer87-labs/charts/kube-escalate`) |
| Container registry | `ghcr.io/layer87-labs/kube-escalate` |
| Release pipeline | GitHub Actions + `relctl` (`layer87-labs/relctl-action`) |
| CI | GitHub Actions — `ci.yaml` gates on `generate-build-infos` |

## CI / Release pipeline

**Do not use GoReleaser for releases.** `.goreleaser.yaml` is for local
`goreleaser build --snapshot` only.

The release pipeline follows the relctl pattern exactly:

```
CI (PRs):
  generate-build-infos  ← layer87-labs/relctl-action/.github/workflows/generate-build-infos.yml@main
  build / audit / helm-lint  ← gate on generate-build-infos

Release (push to main):
  create-release   ← layer87-labs/relctl-action/.github/workflows/create-release.yml@main
  build (matrix)   ← make build/single BINARY=<b> SUFFIX=<os-arch>  +  cosign  +  syft
  checksums        ← sha256sum
  container-image  ← docker buildx per arch → multi-arch manifest → GHCR
  helm-publish     ← helm package + helm push OCI
  publish-release  ← relctl release publish --asset file=<path>
```

When writing or modifying workflows, read `.github/workflows/ci.yaml` and
`.github/workflows/release.yaml` first — do not invent new patterns.

## Core design decisions

### Identity (immutable)
The plugin calls `SelfSubjectReview` to resolve the caller's identity.
The username and groups are set by the API server from the validated OIDC token.
**Never read identity from CLI flags.**

```go
cs.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authv1.SelfSubjectReview{}, ...)
```

### Escalation mechanism (no CRD)
The plugin creates a standard `ClusterRoleBinding` or `RoleBinding` with:
```yaml
labels:
  kube-escalate/managed: "true"
annotations:
  kube-escalate/expires-at:      "2026-05-24T16:00:00Z"   # RFC3339
  kube-escalate/requester:       "eike@layer87.de"         # from SelfSubjectReview
  kube-escalate/reason:          "CNPG hotfix"
  kube-escalate/original-groups: "oidc:platform-operator"
```

### Finalizer lifecycle
Every managed CRB/RB gets `kube-escalate.layer87.de/cleanup` on first reconcile.
This guarantees the operator fires whether the binding is deleted by TTL expiry
or by `kubectl escalate revoke`:

```
reconcile 1  → add finalizer + RecordCreation
reconcile N  → TTL not elapsed → requeue at expiresAt+1s
reconcile N  → TTL elapsed    → client.Delete (sets DeletionTimestamp)
reconcile N+1→ DeletionTimestamp set → handleFinalization:
                 now > expiresAt  → EscalationExpired  event + RecordExpiry
                 now ≤ expiresAt  → EscalationRevoked event + RecordRevocation
               remove finalizer → object garbage-collected
```

### Prometheus metrics
The cumulative instruments are registered with `metrics.Registry` at package
init: `kube_escalate_escalations_total`, `kube_escalate_expired_total`,
`kube_escalate_revoked_total` (counters), `kube_escalate_duration_seconds`
(histogram). Labels: `{user, role, namespace}`.

`kube_escalate_active_escalations` (gauge) is **not** one of them. It is
produced by `activeEscalationsCollector`, registered in `SetupWithManager`
(it needs a client), which counts managed bindings from the manager cache at
scrape time. Do not "simplify" this back into an incremental gauge: creations
are only counted on a binding's first reconcile, so after any operator
restart the gauge would resume from zero while escalations are still live and
then go negative as they expire. See `TestActiveEscalations_CountedFromLiveState`.

### Discovering targets
`kubectl escalate targets` answers "where may I escalate to?" via
`SelfSubjectRulesReview` — the API server's own evaluation. Do not replace this
with reading the requester ClusterRole: that assumes a name, requires a
permission the requester need not hold, and breaks on aggregation or when the
grant arrives through a different group. It also deliberately shows no
max-duration column; the client cannot read the operator's `--max-duration`,
and a wrong number is worse than none (see issue #9).

## Release artifacts — read before writing install docs

`deploy/helm/Chart.yaml`'s `version`/`appVersion` are **placeholders**. The
release workflow packages with `helm package --version $VERSION --app-version
$VERSION` from the relctl-derived version, so the committed values never reach
the registry. Never quote them as the published version.

Releases ship **plain binaries**, not archives:

```
kubectl-escalate_<version>_<os>-<arch>[.exe]   +  .bundle  +  .sbom.spdx.json
operator_<version>_linux-<arch>
kube-escalate-<version>.tgz                    (Helm chart)
sha256sum.txt
```

The `.bundle` files are **cosign** bundles (`cosign sign-blob`), not GitHub
provenance attestations — `gh attestation verify` fails on them with a 404.
Verification is `cosign verify-blob --bundle … --certificate-identity-regexp …`.

## This is a public, general-purpose project

Nothing in this repository may describe one organisation's cluster: no cluster
names, identity-provider or group names, internal registries, or RBAC posture.
Identity comes from whatever authenticator the API server already validated, so
never tie documentation or code to a specific IdP. Verification runs against a
real cluster belong in that organisation's private repositories, not here.

Documentation split, as the maintainer wants it: this repo keeps **only**
`docs/install.md`, `docs/usage.md` and `docs/architecture.md`. Operator guides,
hardening, and anything longer belong on the docs site
(<https://layer87-labs.github.io/docs/>), linked from here.

## Code rules

- **Analyze first.** Inspect the repo, produce a gap list, wait for confirmation
  before writing code.
- No business logic in `main.go` — wiring only.
- Wrap all errors: `fmt.Errorf("context: %w", err)`.
- Logging: `slog` in the plugin; `logr` (via controller-runtime zap) in the operator.
- No `panic()` except fatal startup errors in `main()`.
- No secrets in code or Git.
- All exported symbols must have GoDoc comments.
- Conventional Commits: `feat:`, `fix:`, `docs:`, `chore:`
- Feature branches: `feature/<name>` — never commit directly to `main`.

## Known limitations (v1)

- **`kube-escalate` enforces no role/target allow-list.** Whether a user may bind
  themselves to a given ClusterRole/Role is entirely governed by Kubernetes' own
  RBAC privilege-escalation rules (`bind`/`escalate` verbs, or already holding
  the target role's permissions) on the caller's existing grants — see
  `docs/architecture.md`. Scoping *which* roles a given group may request is an
  IaC/RBAC concern (`resourceNames` on ClusterRoles, OIDC group mapping), not
  something this operator or plugin validates.
- **Requested TTL is capped, not validated per-role.** The operator clamps any
  `expires-at` beyond `--max-duration` (default `24h`, see `EscalationReconciler.MaxDuration`)
  on first reconcile. There is no per-role or per-user TTL policy — one cap
  applies cluster-wide.

## Out of scope (v1)

- Multi-person approval workflows
- Identity-provider API integration — identity comes from the token the API
  server already validated, via SelfSubjectReview; kube-escalate never talks to
  an IdP itself and is not tied to any particular one
- Web UI or dashboard
- Krew publication (after first stable release)
- Namespace isolation via Kyverno (separate concern)
