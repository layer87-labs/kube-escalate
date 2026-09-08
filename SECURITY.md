# Security Policy

## Reporting a vulnerability

**Reporting process, contact address and response times are defined
organization-wide:** see the [layer87-labs security policy](https://github.com/layer87-labs/.github/blob/main/SECURITY.md).

In short: do not open a public issue — report to **security@layer87.de** or via
[GitHub Security Advisories](https://github.com/layer87-labs/kube-escalate/security/advisories/new).

This file exists only to add what is specific to kube-escalate: **what counts
as a vulnerability here**. For a Just-in-Time privilege escalation tool that
boundary is easy to misjudge in both directions, so it is written down.

When reporting, please include the affected version (`kubectl escalate
--version`, operator image tag, chart version), your Kubernetes version and
distribution, and what an attacker gains.

## Threat model

kube-escalate addresses **standing privilege and accident**: credentials that
hold elevated access around the clock and get stolen, reused in a script, or
used carelessly — and the inability to reconstruct who held which role, when,
and why.

It does **not** contain a user who is authorised to escalate and chooses to
abuse it. When the target role is `cluster-admin`, the time bound is only
enforced against someone who cooperates with it. During the escalation window
that user is a full cluster admin and can delete any admission policy
constraining them, rewrite their own `kube-escalate/expires-at` annotation,
strip the cleanup finalizer, uninstall the operator, or create an ordinary
permanent `ClusterRoleBinding`. None of that is preventable from inside the
cluster once `cluster-admin` has been granted — by this or by any other JIT
tool.

See [docs/architecture.md](docs/architecture.md#threat-model--what-this-does-and-does-not-protect-against)
for the full discussion.

## Out of scope

Known and documented properties, not vulnerabilities:

- An escalated `cluster-admin` making their access permanent (see above).
- Escalating to a role the requester was granted `bind` permission on. **Who
  may escalate to what is decided by your cluster's RBAC**, not by
  kube-escalate — the tool deliberately enforces no allow-list of its own.
- Bindings not expiring while the operator is stopped or uninstalled. This is
  a deliberate availability trade-off: escalation does not depend on the
  operator being reachable, so the operator being down delays cleanup rather
  than blocking access. Bindings are reconciled when it returns.
- Kubernetes Events disappearing after roughly an hour. Events are a
  convenience surface; retrospective audit requires shipping the operator's
  structured logs off-cluster.
- `kubectl escalate status` showing the originally requested TTL rather than
  the clamped one when a request exceeded `--max-duration`. The operator never
  rewrites the annotation (doing so makes its own update fail the API server's
  RBAC escalation check); the `EscalationClamped` event carries the effective
  expiry.

## In scope — we want to hear about these

- Obtaining an escalation to a role you were **not** granted `bind` on.
- Escalating as, or on behalf of, a **different** identity than your own.
- A binding that outlives its TTL while the operator is running and healthy.
- Forging or spoofing the requester identity recorded on a binding.
- Privilege escalation via the operator's own ServiceAccount.
- Anything that lets an unauthenticated or unauthorised caller create,
  extend, or prevent cleanup of an escalation.
