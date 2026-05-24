package plugin

// Label and annotation keys written by the plugin and read by the operator.
// These must stay in sync with internal/operator/controller.go.
const (
	// LabelManaged marks a binding as managed by kube-escalate.
	LabelManaged = "kube-escalate/managed"

	// AnnotationExpiresAt holds the RFC3339 timestamp after which the binding is deleted.
	AnnotationExpiresAt = "kube-escalate/expires-at"

	// AnnotationRequester holds the verified OIDC identity from SelfSubjectReview.
	AnnotationRequester = "kube-escalate/requester"

	// AnnotationReason holds the human-readable reason supplied by the requester.
	AnnotationReason = "kube-escalate/reason"

	// AnnotationOriginalGroups holds the requester's OIDC group membership at request time.
	AnnotationOriginalGroups = "kube-escalate/original-groups"
)
