package plugin

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// ConfigMapName is the fixed name of the ConfigMap the Helm chart renders
	// with the operator's enforced settings. Fixed rather than release-derived
	// because a client cannot know the release name.
	ConfigMapName = "kube-escalate-config"

	// ConfigKeyMaxDuration is the key holding the enforced TTL ceiling.
	ConfigKeyMaxDuration = "maxDuration"

	// DefaultOperatorNamespace is where the chart is installed by default.
	DefaultOperatorNamespace = "kube-escalate"
)

// LookupMaxDuration reads the enforced TTL ceiling published by the Helm
// chart.
//
// It returns ok=false whenever the value cannot be established — the
// ConfigMap is absent (an older chart), unreadable (the caller lacks `get` on
// it), or malformed. Callers must then omit the ceiling entirely rather than
// substitute a guess: a request beyond the ceiling is silently shortened
// rather than rejected, so showing a wrong number would let someone plan
// around a deadline that will not hold. Nothing here is worth failing a
// command over.
func LookupMaxDuration(ctx context.Context, cs kubernetes.Interface, namespace string) (time.Duration, bool) {
	if namespace == "" {
		namespace = DefaultOperatorNamespace
	}

	cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, ConfigMapName, metav1.GetOptions{})
	if err != nil {
		return 0, false
	}

	raw, present := cm.Data[ConfigKeyMaxDuration]
	if !present {
		return 0, false
	}

	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}
