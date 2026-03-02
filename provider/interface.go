package provider

import (
	corev1 "k8s.io/api/core/v1"
)

// SecretManager defines the contract that every secret manager backend must implement.
type SecretManager interface {
	// Name returns a human-readable identifier for the provider (e.g., "vault", "aws").
	// Used in log messages and metric labels.
	Name() string

	// Enabled returns true if this provider has been configured via pod annotations.
	Enabled() bool

	// Validate checks that all required annotations are present and returns a descriptive
	// error if any required field is missing. Called BEFORE MutateContainer.
	Validate() error

	// MutateContainer modifies the given container to inject this provider's configuration:
	//   - Prepends provider-specific CLI args to container.Args
	//   - Appends necessary environment variables to container.Env
	//   - Appends any provider-specific VolumeMounts to container.VolumeMounts
	// The returned container is the fully mutated version.
	MutateContainer(container corev1.Container) corev1.Container

	// ExtraVolumes returns any additional pod-level Volumes this provider needs mounted.
	// These are appended to pod.Spec.Volumes by the webhook.
	// Return nil or empty slice if no extra volumes are needed.
	ExtraVolumes() []corev1.Volume
}
