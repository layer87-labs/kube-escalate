# Installation

Three things have to be in place, and only the first is automatic:

1. **The operator** — enforces expiry. Installing it grants nobody anything.
2. **RBAC** — decides who may escalate to what. This is the actual security
   decision and nothing in this project makes it for you.
3. **The plugin** — a client-side binary each user installs on their own
   machine.

## Prerequisites

- Kubernetes **1.26+** (1.30+ if you want the admission-policy hardening)
- An authenticator the API server trusts, if you want group-based grants:
  any OIDC provider, or client certificates
- Helm 3 with OCI support

---

## Operator

```bash
helm install kube-escalate oci://ghcr.io/layer87-labs/charts/kube-escalate \
  --namespace kube-escalate --create-namespace
```

### Key configuration values

| Value | Default | Meaning |
|---|---|---|
| `maxDuration` | `24h` | Ceiling on any requested TTL. A longer request is **not rejected** — it is treated as expiring at `CreationTimestamp + maxDuration` and an `EscalationClamped` event is emitted. |
| `replicaCount` | `1` | Set >1 together with `leaderElection.enabled=true`. |
| `metrics.serviceMonitor.enabled` | `false` | Without this the `/metrics` endpoint is served but never scraped. |
| `networkPolicy.enabled` | `false` | Restricts operator ingress/egress. `networkPolicy.apiServerPort` must match your cluster (commonly 6443, or 443 on managed clusters). |
| `leaderElection.enabled` | `true` | Required for more than one replica. |

The operator's own permissions are deliberately small — `get/list/watch/update/patch/delete`
on `clusterrolebindings` and `rolebindings`, events, and its leader-election
lease. **It holds no `create` on bindings and never reads Secrets**, so it
cannot grant permissions, only remove them. See
[`deploy/helm/templates/clusterrole.yaml`](../deploy/helm/templates/clusterrole.yaml).

---

## Plugin (`kubectl-escalate`)

The plugin runs on a workstation, not in the cluster. Deploying the operator
installs nothing for your users — tell them to do this themselves.

Releases ship **plain binaries**. There is no archive to unpack.

Any executable on your `PATH` named `kubectl-escalate` is picked up by `kubectl`
as `kubectl escalate`.

### Linux — amd64

```bash
VERSION=0.5.0
mkdir -p ~/.local/bin

gh release download "$VERSION" --repo layer87-labs/kube-escalate \
  --pattern "kubectl-escalate_${VERSION}_linux-amd64" \
  --pattern 'sha256sum.txt'

grep "kubectl-escalate_${VERSION}_linux-amd64$" sha256sum.txt | sha256sum -c -

install -m 0755 "kubectl-escalate_${VERSION}_linux-amd64" ~/.local/bin/kubectl-escalate
kubectl escalate --version
```

Without `gh`, substitute the download step:

```bash
BASE=https://github.com/layer87-labs/kube-escalate/releases/download/$VERSION
curl -fLO "$BASE/kubectl-escalate_${VERSION}_linux-amd64"
curl -fLO "$BASE/sha256sum.txt"
```

`curl -f` matters: without it a 404 is written to the output file and you end up
"installing" an HTML error page.

### Other platforms

Replace the suffix; everything else is identical.

| Platform | Asset suffix |
|---|---|
| Linux arm64 | `linux-arm64` |
| macOS Apple Silicon | `darwin-arm64` |
| macOS Intel | `darwin-amd64` |
| Windows amd64 | `windows-amd64.exe` |

On macOS, Gatekeeper quarantines downloaded binaries:

```bash
xattr -d com.apple.quarantine ~/.local/bin/kubectl-escalate 2>/dev/null || true
```

On Windows, place `kubectl-escalate.exe` anywhere on `PATH`.

### Always check the checksum

A download that stops early leaves a **valid ELF binary** that segfaults with
exit code 139 on every invocation and prints **nothing at all**. It looks
exactly like a broken release build, and it will cost you an hour. `sha256sum -c`
turns that into a one-line `FAILED`.

### Verifying the signature (optional)

Binaries are signed with [cosign](https://github.com/sigstore/cosign) keyless
via GitHub OIDC. The `.bundle` files in the release are cosign bundles — note
that `gh attestation verify` does **not** work on them, it expects a different
format.

```bash
gh release download "$VERSION" --repo layer87-labs/kube-escalate \
  --pattern "kubectl-escalate_${VERSION}_linux-amd64.bundle"

cosign verify-blob \
  --bundle "kubectl-escalate_${VERSION}_linux-amd64.bundle" \
  --certificate-identity-regexp '^https://github\.com/layer87-labs/kube-escalate/\.github/workflows/release\.yaml@refs/heads/main$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  "kubectl-escalate_${VERSION}_linux-amd64"
```

Expected output: `Verified OK`.

Each binary also ships an SPDX SBOM (`*.sbom.spdx.json`).

---

## RBAC prerequisites

**This is the part that makes the tool work, and it is not optional.** With the
operator running but no RBAC, nobody can escalate and nothing will tell them
why.

### How the boundary actually works

Kubernetes decides, not kube-escalate:

- `create` on `clusterrolebindings` / `rolebindings` lets a user create a
  binding at all.
- The **`bind` verb, scoped with `resourceNames`**, is what lets them reference
  a role whose permissions they do not already hold. Without it the API server
  rejects the request, and a user could only ever bind roles they already have —
  which would make the whole tool pointless.

### Requester role

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kube-escalate-requester
rules:
  # Resolve the caller's identity, and answer "kubectl escalate targets".
  - apiGroups: ["authentication.k8s.io"]
    resources: ["selfsubjectreviews"]
    verbs: ["create"]
  - apiGroups: ["authorization.k8s.io"]
    resources: ["selfsubjectrulesreviews"]
    verbs: ["create"]

  # Create escalations, list and revoke one's own.
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["clusterrolebindings", "rolebindings"]
    verbs: ["create", "get", "list", "watch", "delete"]

  # The escalation privilege itself. Keep this list as short as you can —
  # anything not named here cannot be escalated to.
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["clusterroles"]
    resourceNames: ["cluster-admin"]
    verbs: ["bind"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kube-escalate-requester
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kube-escalate-requester
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: platform-admins        # ← your group, as Kubernetes sees it
```

Both `selfsubjectreviews` and `selfsubjectrulesreviews` are already granted to
every authenticated user by the built-in `system:basic-user` role; they are
restated here so the grant is self-contained.

### Check the group name — this is the mistake everyone makes

The subject must match the group **as the API server sees it**, including any
prefix your OIDC configuration adds. Getting this wrong produces no error at
install time; it surfaces during an incident, when nobody can escalate.

```bash
kubectl auth whoami -o jsonpath='{.status.userInfo.groups}'
```

Compare that output against the `name:` in the binding above. Then confirm from
the user's side:

```bash
kubectl escalate targets
```

If that prints "You may not escalate to any role", the grant is not reaching
you — the group name is the first thing to check.

### Target roles must already exist

kube-escalate never creates the roles you escalate *to*. `cluster-admin` is
built in; anything custom is yours to create first.

Optionally annotate a target so `kubectl escalate targets` can explain it:

```bash
kubectl annotate clusterrole cluster-admin \
  kube-escalate/description="full cluster access — use sparingly"
```

### Two things you still need

**A base tier small enough that escalating is actually necessary.** If everyday
credentials already carry broad write access, this tool is decoration.

**Admission policies**, without which the RBAC grant above is weaker than it
looks: `create` cannot be scoped by `resourceNames`, so a requester could
otherwise create an unmanaged binding with no expiry, or one naming somebody
else.

Both are covered in the operator guides at
<https://layer87-labs.github.io/docs/>.
