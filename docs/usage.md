# Usage

## Commands

### Escalate (default action)

Create a time-limited privilege escalation. Your identity is resolved from your
OIDC token via `SelfSubjectReview` — it cannot be supplied or forged via flags.

```bash
# Cluster-wide ClusterRoleBinding
kubectl escalate \
  --to cluster-admin \
  --duration 1h \
  --reason "Deploying CNPG update"

# Namespace-scoped RoleBinding
kubectl escalate \
  --to editor \
  --namespace tenant-acme \
  --duration 30m \
  --reason "Fixing broken deployment"
```

**Flags**

| Flag | Required | Description |
|---|---|---|
| `--to` | ✓ | ClusterRole (cluster-wide) or Role (namespace-scoped) to bind |
| `--duration` | ✓ | TTL, e.g. `1h`, `30m`, `90m` |
| `--reason` | ✓ | Human-readable justification (stored in annotation, auditable) |
| `--namespace` / `-n` | — | Scope to a `RoleBinding`; omit for a cluster-wide `ClusterRoleBinding` |
| `--kubeconfig` | — | Path to kubeconfig; default: `$KUBECONFIG` → `~/.kube/config` |

---

### Status

List active escalations.

```bash
# Your own escalations only
kubectl escalate status

# All users (requires list permission on ClusterRoleBindings)
kubectl escalate status --all
```

Example output:

```
REQUESTER               ROLE            SCOPE              EXPIRES AT            REMAINING
eike@layer87.de         cluster-admin   cluster            2026-05-24T16:00:00Z  55m0s
eike@layer87.de         editor          ns/tenant-acme     2026-05-24T15:30:00Z  25m0s
```

---

### Revoke

Delete active escalations before their TTL elapses.

```bash
# Revoke your own escalations
kubectl escalate revoke

# Revoke all users' escalations (requires delete on ClusterRoleBindings)
kubectl escalate revoke --all
```

---

## Common workflows

### Daily operations

```bash
# Check whether you have any active escalations
kubectl escalate status

# Escalate for 30 minutes to debug a pod
kubectl escalate \
  --to cluster-admin \
  --duration 30m \
  --reason "Debugging CrashLoopBackOff in prod"

# Do your work …

# Revoke early once done — don't wait for the TTL
kubectl escalate revoke
```

### Incident response

```bash
# Escalate for 2 hours during a P0 incident
kubectl escalate \
  --to cluster-admin \
  --duration 2h \
  --reason "P0: database failover JIRA-4821"

# During the incident — see who else has active escalations
kubectl escalate status --all

# After the incident — revoke everything
kubectl escalate revoke --all
```

---

## Reading the audit trail

### Kubernetes Events

The operator emits a `Warning/EscalationExpired` event whenever a binding is
deleted by TTL, and the plugin emits `Warning/EscalationRevoked` on manual
revocation.

```bash
# All escalation events across all namespaces, newest last
kubectl get events \
  --field-selector reason=EscalationExpired \
  -A --sort-by='.lastTimestamp'
```

### Raw annotation inspection

```bash
# List all managed bindings and their expiry
kubectl get clusterrolebindings \
  -l kube-escalate/managed=true \
  -o custom-columns=\
NAME:.metadata.name,\
REQUESTER:.metadata.annotations.kube-escalate/requester,\
EXPIRES:.metadata.annotations.kube-escalate/expires-at,\
REASON:.metadata.annotations.kube-escalate/reason
```

### Prometheus queries

```promql
# Active escalations right now
kube_escalate_active_escalations

# Rate of new escalations per minute
rate(kube_escalate_escalations_total[5m]) * 60

# Escalations expired by TTL in the last hour
increase(kube_escalate_expired_total[1h])

# Escalations manually revoked in the last hour
increase(kube_escalate_revoked_total[1h])

# Average escalation duration (minutes)
rate(kube_escalate_duration_seconds_sum[1h])
  / rate(kube_escalate_duration_seconds_count[1h]) / 60
```
