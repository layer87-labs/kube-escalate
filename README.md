# kube-escalate

[![CI](https://github.com/layer87-labs/kube-escalate/actions/workflows/ci.yaml/badge.svg)](https://github.com/layer87-labs/kube-escalate/actions/workflows/ci.yaml)
[![Release](https://github.com/layer87-labs/kube-escalate/actions/workflows/release.yaml/badge.svg)](https://github.com/layer87-labs/kube-escalate/actions/workflows/release.yaml)
[![Go](https://img.shields.io/badge/Go-1.26-blue)](go.mod)
[![License](https://img.shields.io/github/license/layer87-labs/kube-escalate)](LICENSE)

Kubernetes Operator + kubectl plugin for **Just-in-Time privilege escalation**.

Grant temporary, time-limited cluster access with automatic expiry, a full audit
trail via Kubernetes Events, and Prometheus metrics — no CRDs, no database, no
external dependencies.

---

## How it works

The plugin creates a standard `ClusterRoleBinding` annotated with an expiry
timestamp and the caller's verified OIDC identity. The operator watches these
bindings and deletes them once the TTL elapses.

```
kubectl escalate ──► SelfSubjectReview ──► ClusterRoleBinding
                      (tamper-proof            (expires-at annotation)
                       OIDC identity)                  │
                                                       ▼
                                              operator reconciler
                                              deletes on TTL expiry
                                              emits Kubernetes Event
```

Identity is **always** resolved via the Kubernetes `SelfSubjectReview` API —
the API server populates it from your validated OIDC token. It cannot be forged
via CLI arguments.

---

## Quick install

### Operator

```bash
helm install kube-escalate oci://ghcr.io/layer87-labs/charts/kube-escalate \
  --namespace kube-system \
  --create-namespace
```

### Plugin

```bash
# Linux amd64
curl -Lo kubectl-escalate.tar.gz \
  https://github.com/layer87-labs/kube-escalate/releases/latest/download/kubectl-escalate_linux_amd64.tar.gz
tar xf kubectl-escalate.tar.gz kubectl-escalate
chmod +x kubectl-escalate && sudo mv kubectl-escalate /usr/local/bin/
```

See [docs/install.md](docs/install.md) for macOS, Windows, and RBAC prerequisites.

---

## Quick usage

```bash
# Escalate cluster-wide for 1 hour
kubectl escalate \
  --to cluster-admin \
  --duration 1h \
  --reason "Deploying CNPG update"

# Check active escalations
kubectl escalate status

# Revoke early
kubectl escalate revoke

# Namespace-scoped escalation
kubectl escalate \
  --to editor \
  --namespace tenant-acme \
  --duration 30m \
  --reason "Fixing broken deployment"
```

---

## Documentation

| | |
|---|---|
| [docs/install.md](docs/install.md) | Operator (Helm), plugin install, RBAC prerequisites |
| [docs/usage.md](docs/usage.md) | All commands, common workflows, audit trail |
| [docs/architecture.md](docs/architecture.md) | Component diagram, security model, bootstrap order |

---

## Out of scope (v1)

- Multi-person approval workflows
- Web UI or dashboard
- Krew publication (planned post first stable release)
- Namespace isolation enforcement (separate concern, e.g. Kyverno)
