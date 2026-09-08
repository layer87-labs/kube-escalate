# kube-escalate

[![CI](https://github.com/layer87-labs/kube-escalate/actions/workflows/ci.yaml/badge.svg)](https://github.com/layer87-labs/kube-escalate/actions/workflows/ci.yaml)
[![Release](https://github.com/layer87-labs/kube-escalate/actions/workflows/release.yaml/badge.svg)](https://github.com/layer87-labs/kube-escalate/actions/workflows/release.yaml)
[![Go](https://img.shields.io/badge/Go-1.26-blue)](go.mod)
[![License](https://img.shields.io/github/license/layer87-labs/kube-escalate)](LICENSE)

Kubernetes operator + kubectl plugin for **Just-in-Time privilege escalation**.

Give people a small everyday role and let them raise it to a bigger one for a
bounded time, with the reason recorded — instead of standing cluster-admin that
is only ever used for ten minutes a month.

No CRDs, no database, no external dependencies. State lives in ordinary
`ClusterRoleBinding` / `RoleBinding` objects.

---

## How it works

```
kubectl escalate ──► SelfSubjectReview ──► ClusterRoleBinding
                      (identity from            (kube-escalate/managed=true,
                       the API server)           expires-at annotation)
                                                        │
                                                        ▼
                                               operator reconciler
                                               deletes at expiry
                                               emits Event + metrics
```

The plugin asks the API server who you are (`SelfSubjectReview`) and creates an
annotated binding. The operator watches those bindings and deletes them when the
TTL elapses.

Your identity comes from whatever authenticator the API server already trusts —
any OIDC provider, or client certificates. It cannot be forged via CLI flags,
and kube-escalate never talks to an identity provider itself.

**What decides where you may escalate to is your cluster's RBAC, not this
tool.** kube-escalate enforces no allow-list of its own. See
[docs/install.md](docs/install.md#rbac-prerequisites).

---

## Install

### Operator

```bash
helm install kube-escalate oci://ghcr.io/layer87-labs/charts/kube-escalate \
  --namespace kube-escalate --create-namespace
```

Installing the operator grants nobody anything — it only enforces expiry. The
RBAC that lets people request escalations is a separate, deliberate step:
[docs/install.md](docs/install.md#rbac-prerequisites).

### Plugin

The plugin is **client-side**: it runs on your workstation, not in the cluster.
Deploying the operator installs nothing for your users.

Releases ship plain binaries — there is no tarball to unpack.

```bash
VERSION=0.5.0   # or: gh release view --repo layer87-labs/kube-escalate --json tagName -q .tagName
mkdir -p ~/.local/bin

gh release download "$VERSION" --repo layer87-labs/kube-escalate \
  --pattern "kubectl-escalate_${VERSION}_linux-amd64" \
  --pattern 'sha256sum.txt'

# Verify before trusting it — see below for why this matters
grep "kubectl-escalate_${VERSION}_linux-amd64$" sha256sum.txt | sha256sum -c -

install -m 0755 "kubectl-escalate_${VERSION}_linux-amd64" ~/.local/bin/kubectl-escalate
kubectl escalate --version
```

Anything on your `PATH` named `kubectl-escalate` becomes `kubectl escalate`.

> **Check the checksum.** A download that stops early leaves a *valid* ELF
> binary that segfaults on every call with **no output at all** — it looks
> exactly like a broken release. `sha256sum -c` turns that into an obvious
> one-line failure.

macOS, Windows, and signature verification: [docs/install.md](docs/install.md).

---

## Usage

```bash
# Where am I allowed to escalate to?
kubectl escalate targets

# Escalate cluster-wide for an hour
kubectl escalate --to cluster-admin --duration 1h --reason "CNPG hotfix"

# What is active, and for how much longer?
kubectl escalate status

# Give it back early — don't wait for the TTL
kubectl escalate revoke

# Namespace-scoped instead
kubectl escalate --to editor --namespace tenant-acme \
  --duration 30m --reason "Fixing broken deployment"
```

`targets` asks the API server what your permissions actually are, so it stays
correct however your cluster's RBAC is arranged.

Access ends without warning: the next `kubectl` call simply returns
`Forbidden`. `kubectl escalate status` shows the remaining time.

---

## Documentation

| | |
|---|---|
| [docs/install.md](docs/install.md) | Operator, plugin, verification, RBAC prerequisites |
| [docs/usage.md](docs/usage.md) | All commands, workflows, audit trail |
| [docs/architecture.md](docs/architecture.md) | Component diagram, security model, threat model |

Guides for cluster operators — hardening with admission policies, designing a
base tier, observability — live at
**<https://layer87-labs.github.io/docs/>**.

---

## Security

kube-escalate addresses **standing privilege and accident**, not a malicious
insider who is already allowed to escalate. An escalation to `cluster-admin` is
not containment: during the window that person can remove the very controls
that bound them. See [SECURITY.md](SECURITY.md) and
[docs/architecture.md](docs/architecture.md#threat-model--what-this-does-and-does-not-protect-against).

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).

---

## Out of scope

- Multi-person approval workflows
- Web UI or dashboard
- Krew publication
- Namespace isolation enforcement (a separate concern, e.g. Kyverno)
