// Package operator implements the kube-escalate controller-runtime reconciler.
// It watches ClusterRoleBindings labelled kube-escalate/managed=true and
// deletes them once their TTL (kube-escalate/expires-at annotation) has elapsed.
// Full implementation in Phase 3.
package operator
