# Test protocol — 2026-08-26

End-to-end verification against the production RKE2 cluster (`layer87-prod`),
Zitadel SSO, real `kubectl escalate` plugin. Performed as part of the
maintenance + security-hardening pass on branch
`feature/security-ttl-enforcement-v2`.

## Setup

- Deployed the hardened build (RoleBinding TTL enforcement + `--max-duration`
  clamp, see `docs/architecture.md`) into the **existing prod `kube-escalate`
  Helm release**, image-only swap (`cr.layer87.de/layer87/kube-escalate-dev:0.0.0-test-security-hardening`),
  `maxDuration=10m` for a fast test cycle. Fully reverted to the original
  `ghcr.io/layer87-labs/kube-escalate:0.2.0` / default values afterward
  (`helm upgrade` back to prior values, verified via `helm get values` diff).
- All escalation requests were made as `oidc:eksrha@layer87.de` via the real
  `kubectl escalate` plugin (built from this branch), through Zitadel SSO —
  **not** via an admin/`system:masters` credential.
- A disposable, tightly-scoped RBAC grant was created and later fully deleted
  to allow this identity to exercise the flow at all (see finding 3.0 below):
  a `ClusterRole`/`ClusterRoleBinding` allowing `bind` only on the built-in
  `view` ClusterRole (a role this identity already effectively has via its
  existing `platform-operator` read grant), plus a `Role`/`RoleBinding`
  scoped to one throwaway namespace (`kube-escalate-selftest`) allowing
  `bind` only on the built-in `edit` Role there. Neither grants any access to
  real workload namespaces or to `cluster-admin`. Applied and removed with an
  admin kubeconfig (`iac-bootstrap/prod/kubeconfig.yaml`) since the test
  identity itself has no RBAC-write permissions (see finding 3.0).

## 3.1 — Deployment state (read-only, before any change)

| Item | Found |
|---|---|
| Helm release | `kube-escalate-0.1.0` (chart), `appVersion 0.1.0`, revision 6, deployed 2026-05-25 |
| Image actually running | `ghcr.io/layer87-labs/kube-escalate:0.2.0` (decoupled from chart version via `image.tag` override) |
| Replicas | 2, leader election on |
| ClusterRole (operator SA) | `get/list/watch/update/patch/delete` on `clusterrolebindings`/`rolebindings`, `create/patch` on events, full lease access — matches current repo (PR #3's RBAC fix is live) |
| `networkPolicy.enabled` | `false` — chart offers one, it's off in prod |
| Active managed bindings | none (`kubectl get clusterrolebindings -l kube-escalate/managed=true` → empty) |

No drift between deployed RBAC and the repo's current chart. The chart/image
version mismatch (0.1.0 chart / 0.2.0 image) is cosmetic but worth
straightening out at the next release.

## 3.0 — Finding: nobody can invoke kube-escalate today

`kubectl auth can-i create clusterrolebindings` → **no**, for
`oidc:eksrha@layer87.de` (group `zitadel:platform-operator`), and this is the
*only* Zitadel group with any ClusterRoleBinding in the cluster. Full audit:

- No `ClusterRole` other than `cluster-admin`/`argocd-application-controller`
  grants `create` on `clusterrolebindings`.
- The `platform-operator` ClusterRole (bound to `zitadel:platform-operator`,
  `managed-by: terraform`, `part-of: iac-bootstrap`) grants broad read
  (`get/list/watch` on `*`/`*`) and write on a fixed set of workload resources
  (deployments, secrets, jobs, configmaps, etc.) — **no RBAC-object
  permissions at all**.
- No RoleBinding anywhere binds a Zitadel group to anything RBAC-object
  related either.

**Consequence:** `kube-escalate`'s "bootstrap order step 2" (`docs/architecture.md`)
was never completed in `iac-platform`/`iac-bootstrap`. The operator is
deployed and functioning, but is currently unreachable by any real user. This
is the top item in the Phase 4 handover list.

## 3.2 — Configuration review

- No Zitadel-side role/group → target-ClusterRole mapping exists yet (there's
  nothing to map *to*, per 3.0).
- No max-TTL policy existed before this session's fix; now enforced
  server-side by the operator (`--max-duration`, default `24h`), not
  per-role/per-group — flagged as a known limitation in `AGENTS.md`.

## 3.3 — Happy path

| Case | Expected | Result |
|---|---|---|
| Cluster-wide, `--to view --duration 2m` | CRB created, `auth can-i` reflects grant, auto-deleted at TTL, `EscalationExpired` event | **Pass.** Finalizer added on first reconcile, requeued to expiry, deleted, event emitted with correct requester/role. |
| Namespace-scoped, `--to edit -n kube-escalate-selftest --duration 1m` | RoleBinding created and **auto-expires** (this is today's Blocker #1 fix, previously never possible — RBs were never watched) | **Pass — first-ever live confirmation.** Confirmed gone via `kubectl get rolebinding` after TTL. |
| `kubectl escalate status` | Lists own active escalations with correct remaining TTL | Pass |
| `kubectl escalate revoke` | Deletes active escalations early, emits `EscalationRevoked` with reason/scope/duration (today's audit-completeness fix) | **Pass.** Event message: `Escalation for oidc:eksrha@layer87.de to role view (scope=cluster, reason="phase3 revoke test") was manually revoked after 5s`. |

## 3.4 — Negative tests

| Case | Expected | Result |
|---|---|---|
| `--to cluster-admin` (requester doesn't hold cluster-admin permissions, no `escalate`/`bind` bypass) | Rejected | **Pass** — rejected server-side by Kubernetes' own RBAC privilege-escalation check, before the operator is ever involved: `... is attempting to grant RBAC permissions not currently held: {APIGroups:["*"], Resources:["*"], Verbs:["*"]}...` |
| `-n kube-escalate` (real operator namespace, no grant there) | Rejected | **Pass** — `cannot create resource "rolebindings" ... in the namespace "kube-escalate"` |
| TTL over the operator's `--max-duration` ceiling (`--duration 999h` vs `10m` ceiling) | Operator clamps `expires-at`, emits `EscalationClamped` | **Event fired correctly, but see Critical Finding below — the underlying object update failed, leaving the binding un-clamped and un-tracked.** |

Not exercised live (would require real IaC-level access to a second target
role / a second identity — out of the safe blast radius for this session):
double-request idempotency, clock-skew, expired/manipulated token replay.
These remain analytical (Phase 2 code review) rather than live-verified.

## 3.5 — Resilience

`kubectl delete pod` on **both** operator replicas simultaneously while a
`view` escalation (5m TTL) was active. New pods came up, re-acquired
leadership, and the escalation still expired exactly on schedule — survived a
full restart with no manual intervention. **Pass.**

## 3.6 — Observability

- `/metrics` endpoint reachable and serving (verified via an in-cluster
  ephemeral pod, since `networkPolicy.enabled=false` in prod there was no
  policy to work around).
- `kube_escalate_*` series were absent immediately after the resilience
  restart test — expected, Prometheus counters/gauges are in-memory only and
  reset on process restart. Not a bug: production Prometheus should already
  handle counter resets correctly via `rate()`/`increase()`. Worth calling
  out explicitly to whoever builds the Grafana dashboard (Phase 4 handover).
- `EscalationExpired` / `EscalationRevoked` / `EscalationClamped` Kubernetes
  Events all observed with correct reason codes and (after today's fix)
  enriched messages (scope, reason, duration).

## Critical finding — TTL clamp can permanently orphan a grant

**Severity: Blocker.** Found live, reproduced twice.

When the operator's first reconcile needs to *both* clamp `expires-at` (because
the request exceeded `--max-duration`) *and* add the cleanup finalizer, it
does so in a single `client.Update()` call. That call was rejected by the
Kubernetes API server's RBAC privilege-escalation check:

```
reconcile add finalizer: clusterrolebindings.rbac.authorization.k8s.io "..." is forbidden:
user "system:serviceaccount:kube-escalate:kube-escalate" ... is attempting to grant RBAC
permissions not currently held: {APIGroups:[""], Resources:["configmaps"], Verbs:["get" "list" "watch"]} ...
```

The operator's own ServiceAccount does not hold the `view` ClusterRole's
permissions (it only holds narrow RBAC-object/events/lease access — by
design, per `docs/architecture.md`'s least-privilege claim). A **plain**
finalizer-add (no annotation change) on the same `view`-bound object
succeeded without issue in the same test session — the failure is specific to
the annotation-write-plus-finalizer combination on the clamp path.

**Effect:** the reconcile fails, retries indefinitely with the same result
(observed 2 consecutive failures, 0% success), and the binding is left with
**no finalizer and an un-clamped, effectively unbounded `expires-at`
(`2026-10-07`, ~998 hours out)** — i.e. exactly the scenario the clamp exists
to prevent, except now unrecoverable by the operator itself. Manual deletion
was required to clean it up. Any real request that exceeds `--max-duration`
in production would hit this today.

**Fix required** before this branch is mergeable — tracked as a follow-up
commit on this branch, not merged into the reviewed history above.
