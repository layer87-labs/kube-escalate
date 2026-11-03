<p align="center">
  <img alt="kube-escalate: just-in-time cluster access that expires on its own" src="assets/banner.png">
</p>

<p align="center">
  <a href="https://github.com/layer87-labs/kube-escalate/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/layer87-labs/kube-escalate?display_name=tag&sort=semver"></a>
  <a href="https://github.com/layer87-labs/kube-escalate/actions/workflows/release.yaml?query=branch%3Amain"><img alt="Release pipeline (main)" src="https://img.shields.io/github/actions/workflow/status/layer87-labs/kube-escalate/release.yaml?branch=main&label=release"></a>
  <a href="go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/layer87-labs/kube-escalate"></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue.svg"></a>
</p>

# kube-escalate

**kube-escalate** gives people a small everyday role and lets them raise it to a bigger one for a bounded time,
with a reason on record, instead of standing `cluster-admin` that is used for ten minutes a month. It is a
Kubernetes operator plus a `kubectl` plugin.

```bash
kubectl escalate --to cluster-admin --duration 1h --reason "Database failover"
# ... do the work ...
# one hour later the binding is deleted and the next kubectl call returns Forbidden
```

No CRDs, no database, no admission webhook, no external service. State lives in ordinary, annotated
`ClusterRoleBinding` and `RoleBinding` objects.

## Highlights

- **Time-boxed by default.** Every escalation is a binding with an `expires-at` annotation. The operator deletes
  it when the time is up and requeues at the expiry instant, so there is no polling interval.
- **Identity from the API server.** The plugin asks `SelfSubjectReview` who you are. There is no flag to say
  otherwise, and kube-escalate never talks to an identity provider itself.
- **A ceiling on duration.** The operator clamps any requested expiry beyond `--max-duration` (default `24h`)
  and emits an `EscalationClamped` event.
- **Audit trail.** Requester, reason and original groups are stored on the binding. The operator logs each
  escalation and emits `EscalationExpired` / `EscalationRevoked` events, plus Prometheus metrics.
- **Early revoke.** `kubectl escalate revoke` deletes your own escalations; a finalizer makes sure the operator
  still emits an `EscalationRevoked` event and records the metrics.
- **Discoverable.** `kubectl escalate targets` lists the roles you may escalate to, taken from the API server's
  own `SelfSubjectRulesReview`.
- **Small footprint.** The operator can only get, list, watch, update, patch and delete bindings; it cannot
  create them. It runs as a non-root user with a read-only root filesystem and a distroless image.
- **Signed releases.** Binaries and the container image are signed with cosign (keyless); binaries ship an SPDX
  SBOM.

## How it works

```
 you                    kube-apiserver                        kube-escalate operator
  |  kubectl escalate        |                                          |
  |------------------------->|  SelfSubjectReview: who am I?            |
  |                          |  create (Cluster)RoleBinding             |
  |                          |    label      kube-escalate/managed=true |
  |                          |    annotation kube-escalate/expires-at    |
  |                          |    annotation requester, reason, groups   |
  |                          |<-----------------------------------------|  watch managed bindings
  |                          |                                          |  add finalizer, clamp to max-duration
  |                          |<-----------------------------------------|  at expiry: delete binding
  |                          |                                          |  event + metrics + log line
```

The plugin creates the binding with **your own credentials**, so whether you may bind a given role is decided by
Kubernetes RBAC (the `bind` verb, or already holding the role's permissions). kube-escalate adds no allow-list
of its own. Details: [docs/architecture.md](docs/architecture.md).

## Quick start

You need Kubernetes 1.28 or newer (the `authentication.k8s.io/v1` `SelfSubjectReview` API is GA there; the plugin has no fallback), Helm 3 with OCI support, and an
authenticator the API server trusts (OIDC or client certificates).

**1. Install the operator.** Installing it grants nobody anything; it only enforces expiry.

```bash
helm install kube-escalate oci://ghcr.io/layer87-labs/charts/kube-escalate \
  --namespace kube-escalate --create-namespace
```

**2. Decide who may escalate to what.** This is the actual security decision and it is plain RBAC: a role with
`create` on bindings and `bind` on the target role, bound to your group. A complete example is in
[docs/install.md](docs/install.md#rbac-prerequisites).

**3. Install the plugin** on each workstation (Linux amd64 shown; releases ship plain binaries and a
`sha256sum.txt`, tags are plain versions such as `0.6.3`):

```bash
VERSION=0.6.3   # see https://github.com/layer87-labs/kube-escalate/releases/latest
gh release download "$VERSION" --repo layer87-labs/kube-escalate \
  --pattern "kubectl-escalate_${VERSION}_linux-amd64" --pattern 'sha256sum.txt'
grep "kubectl-escalate_${VERSION}_linux-amd64$" sha256sum.txt | sha256sum -c -
install -m 0755 "kubectl-escalate_${VERSION}_linux-amd64" ~/.local/bin/kubectl-escalate
kubectl escalate --version
```

Always check the checksum: a truncated download is a valid-looking binary that crashes without any output.
Other platforms and signature verification: [docs/install.md](docs/install.md).

**4. Use it.**

```bash
kubectl escalate targets                                   # where may I escalate to?
kubectl escalate --to cluster-admin --duration 1h \
  --reason "Database failover"                             # cluster-wide ClusterRoleBinding
kubectl escalate --to editor --namespace tenant-acme \
  --duration 30m --reason "Fix broken deployment"          # namespaced RoleBinding
kubectl escalate status                                    # what is active, how long is left (--all for everyone)
kubectl escalate revoke                                    # give it back early (--all for everyone)
```

`--to`, `--duration` and `--reason` are required. There is no renewal command: request a new escalation, with a
fresh reason.

## Configuration

Operator flags (also exposed as Helm values, see [`deploy/helm/values.yaml`](deploy/helm/values.yaml)):

| Setting | Helm value | Default | Meaning |
| --- | --- | --- | --- |
| `--max-duration` | `maxDuration` | `24h` | Ceiling for any requested expiry; longer requests are clamped, not rejected |
| `--leader-elect` | `leaderElection.enabled` | `true` | Required for more than one replica |
| `--metrics-bind-address` | `metrics.port` | `:8080` | Prometheus endpoint |
| - | `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus Operator `ServiceMonitor` |
| - | `networkPolicy.enabled` | `false` | Restrict operator ingress and egress |
| - | `podDisruptionBudget.enabled` | `false` | For `replicaCount` above 1 |

Metrics: `kube_escalate_active_escalations`, `kube_escalate_escalations_total`, `kube_escalate_expired_total`,
`kube_escalate_revoked_total` and `kube_escalate_duration_seconds`, labelled by `user`, `role` and `namespace`.
More: [docs/install.md](docs/install.md), [docs/usage.md](docs/usage.md).

## Security considerations

kube-escalate addresses **standing privilege and accident**. It does not contain someone who is allowed to
escalate and chooses to abuse it: with `cluster-admin` they can, for example, rewrite their own `expires-at`,
strip the finalizer or create a permanent binding. Prefer narrower target roles, ship the operator's logs
off-cluster (Events live for roughly an hour), and treat "who may escalate" as the real decision. Full
discussion: [SECURITY.md](SECURITY.md) and the
[threat model](docs/architecture.md#threat-model--what-this-does-and-does-not-protect-against).

**Operator permissions** ([`clusterrole.yaml`](deploy/helm/templates/clusterrole.yaml)):

| Resources | Verbs | Why |
| --- | --- | --- |
| `clusterrolebindings`, `rolebindings` | get, list, watch, update, patch, delete | watch managed bindings, manage the finalizer, delete at expiry. No `create`. |
| `events` (core and `events.k8s.io`) | create, patch | audit events |
| `leases` (`coordination.k8s.io`) | get, list, watch, create, update, patch, delete | leader election |

The ClusterRole does not name Secrets.

**Requester permissions** are yours to define: `create` on `selfsubjectreviews`, plus `create`, `get`, `list`,
`watch` and `delete` on bindings, plus `bind` on the specific roles people may request. Without admission policies
the `create` grant cannot be narrowed by name, so a requester could create an unmanaged binding; the
[docs site](https://layer87-labs.github.io/docs/) covers hardening.

To report a vulnerability, follow [SECURITY.md](SECURITY.md).

## Documentation

| | |
| --- | --- |
| [docs/install.md](docs/install.md) | Operator, plugin, verification, RBAC prerequisites |
| [docs/usage.md](docs/usage.md) | All commands, workflows, audit trail, Prometheus queries |
| [docs/architecture.md](docs/architecture.md) | Lifecycle, security model, threat model |

Operator guides (hardening with admission policies, designing a base tier, observability) live on the
[documentation site](https://layer87-labs.github.io/docs/).

## Out of scope

Multi-person approval workflows, a web UI, identity-provider integration, and namespace isolation enforcement.

## Contributing

Issues and pull requests are welcome. Work on a feature branch, use Conventional Commits, and run the
checks before you push:

```bash
make hooks        # run `make audit` before every commit
make test         # unit and envtest tests with the race detector
make audit        # go vet, staticcheck, govulncheck, golangci-lint, race tests
make build        # operator and plugin for your platform
```

Release builds are done by the pipeline in `.github/workflows`; `.goreleaser.yaml` is for local snapshot builds
only. Layout and design decisions are described in [AGENTS.md](AGENTS.md).

## License

Apache License 2.0, see [LICENSE](LICENSE).
