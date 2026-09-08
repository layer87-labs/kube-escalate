# Usage

## Commands

### Targets

Show which roles you are allowed to escalate to.

```bash
kubectl escalate targets

# Include roles bindable inside a namespace
kubectl escalate targets --namespace tenant-acme
```

```
TARGET          SCOPE     DESCRIPTION
cluster-admin   cluster   full cluster access — use sparingly
```

The list comes from a `SelfSubjectRulesReview` — the API server's own
evaluation of your permissions — so it is correct regardless of how your
cluster's RBAC is arranged or which group carries the grant. It needs no
permission beyond what any authenticated user already has for itself.

If it prints **"You may not escalate to any role"**, that is an RBAC question,
not a kube-escalate one: someone must grant your group the `bind` verb on the
roles you should be able to request. The most common cause is a group-name
mismatch — see [install.md](install.md#check-the-group-name--this-is-the-mistake-everyone-makes).

The `DESCRIPTION` column is filled from a `kube-escalate/description`
annotation on the target role, when present and readable.

**No maximum duration is shown.** The ceiling is enforced by the operator
(`--max-duration`) and the client has no way to read it. Displaying a guessed
value would be worse than showing none: a request beyond the ceiling is not
rejected, it is silently shortened, so you would plan around a number that will
not hold. Watch for an `EscalationClamped` event instead.

---

### Escalate (default action)

Create a time-limited privilege escalation. Your identity is resolved via
`SelfSubjectReview` from whatever authenticator the API server trusts — it
cannot be supplied or forged via flags.

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

> Events expire after roughly an hour. For retrospective audit ("who held admin
> last Tuesday, and why?") ship the operator's structured logs off-cluster —
> they carry requester, role, scope, reason and duration on every line.

All events come from the **operator**, not the plugin — it decides which one
applies when it finalizes a binding:

| Reason | Type | When |
|---|---|---|
| `EscalationExpired` | Warning | The TTL elapsed and the binding was deleted |
| `EscalationRevoked` | Normal | The binding was deleted before its TTL |
| `EscalationClamped` | Warning | The requested TTL exceeded `--max-duration`; the effective expiry is in the message |

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
# Active escalations right now.
# Use max, NOT sum: with more than one replica every pod serves this gauge with
# the same value (it is counted from cluster state at scrape time, not only by
# the leader), so summing multiplies it by the replica count.
max by (user, role, namespace) (kube_escalate_active_escalations)

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

---

## Two things that surprise people

### Expiry gives no warning

Access ends silently. The next `kubectl` call simply returns `Forbidden`, which
is disorienting in the middle of a repair.

```bash
kubectl escalate status    # shows the remaining time
```

There is no renewal command by design — request a new escalation, with a fresh
reason, so the audit trail shows a decision rather than a drift.

### Even while escalated, you may not be able to create ordinary RBAC

If your cluster is hardened with the recommended admission policy, that policy
matches on your **group**, not on your role. Escalating grants you
`cluster-admin`, but it does not change the groups in your token — so you are
still constrained: every binding you create must be kube-escalate-managed, carry
an expiry, and name you as its subject.

This is intentional. The policy is precisely what stops an escalation from being
converted into permanent access; exempting escalated users would defeat it.

To change RBAC properly, go through your infrastructure-as-code, which runs as
`system:masters` and is exempt. Someone who does not know this will look in the
wrong place at three in the morning.
