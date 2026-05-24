# Installation

## Prerequisites

- Kubernetes 1.26+
- `kubectl` configured with cluster access
- Helm 3.x (operator install only)

---

## Operator

The operator runs in the cluster and enforces TTLs on escalated bindings.

```bash
helm install kube-escalate oci://ghcr.io/layer87-labs/charts/kube-escalate \
  --namespace kube-system \
  --create-namespace
```

Verify the operator is running:

```bash
kubectl -n kube-system get pods -l app.kubernetes.io/name=kube-escalate
```

### Key configuration values

| Value | Default | Description |
|---|---|---|
| `image.tag` | Chart `appVersion` | Override the operator image tag |
| `replicaCount` | `1` | Set to `2` with `leaderElection.enabled=true` for HA |
| `leaderElection.enabled` | `true` | Required when `replicaCount > 1` |
| `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus Operator `ServiceMonitor` |
| `resources.limits.memory` | `128Mi` | Operator memory limit |

Full reference: [`deploy/helm/values.yaml`](../deploy/helm/values.yaml)

---

## Plugin (`kubectl-escalate`)

The plugin runs on your local machine and creates escalated bindings using your
cluster credentials.

### Linux — amd64

```bash
curl -Lo kubectl-escalate.tar.gz \
  https://github.com/layer87-labs/kube-escalate/releases/latest/download/kubectl-escalate_linux_amd64.tar.gz
tar xf kubectl-escalate.tar.gz kubectl-escalate
chmod +x kubectl-escalate
sudo mv kubectl-escalate /usr/local/bin/
```

### macOS — Apple Silicon

```bash
curl -Lo kubectl-escalate.tar.gz \
  https://github.com/layer87-labs/kube-escalate/releases/latest/download/kubectl-escalate_darwin_arm64.tar.gz
tar xf kubectl-escalate.tar.gz kubectl-escalate
chmod +x kubectl-escalate
sudo mv kubectl-escalate /usr/local/bin/
```

### macOS — Intel

```bash
curl -Lo kubectl-escalate.tar.gz \
  https://github.com/layer87-labs/kube-escalate/releases/latest/download/kubectl-escalate_darwin_amd64.tar.gz
tar xf kubectl-escalate.tar.gz kubectl-escalate
chmod +x kubectl-escalate
sudo mv kubectl-escalate /usr/local/bin/
```

### Windows — amd64

Download `kubectl-escalate_windows_amd64.zip` from the
[latest release](https://github.com/layer87-labs/kube-escalate/releases/latest),
extract `kubectl-escalate.exe`, and place it on your `PATH`.

Verify:

```bash
kubectl escalate --version
```

> **Krew** publication is planned after the first stable release.

---

## RBAC prerequisites

### Plugin user

The user running `kubectl escalate` needs:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kube-escalate-user
rules:
  # Resolve identity via SelfSubjectReview (tamper-proof OIDC identity).
  - apiGroups: ["authentication.k8s.io"]
    resources: ["selfsubjectreviews"]
    verbs: ["create"]

  # Create cluster-wide escalations.
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["clusterrolebindings"]
    verbs: ["create"]

  # Create namespace-scoped escalations (only needed for --namespace usage).
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["rolebindings"]
    verbs: ["create"]
```

For `kubectl escalate status --all` and `kubectl escalate revoke --all` the user
additionally needs `list` and `delete` on `clusterrolebindings`.

### Operator

Installed automatically by the Helm chart.
See [`deploy/helm/templates/clusterrole.yaml`](../deploy/helm/templates/clusterrole.yaml).
