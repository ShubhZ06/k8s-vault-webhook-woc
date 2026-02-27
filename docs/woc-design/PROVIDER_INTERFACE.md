# PROVIDER_INTERFACE.md — k8s-vault-webhook

> **Purpose**: Define the `SecretManager` interface that all secret manager providers must implement, the registration/discovery mechanism, and a skeleton template for creating new providers.

---

## 1. Problem With Current Design (Confirmed From Source)

Current providers (`vault.go`, `aws.go`, `azure.go`, `gcp.go`) each define independent struct types. They share no common interface. The core mutation logic in `main.go` uses hard-coded sequential `if` blocks:

```go
// Current — must change every file to add a new provider
if secretManagerConfig.azure.config.enabled { container = secretManagerConfig.azure.mutateContainer(container) }
if secretManagerConfig.aws.config.enabled   { container = secretManagerConfig.aws.mutateContainer(container) }
if secretManagerConfig.gcp.config.enabled   { container = secretManagerConfig.gcp.mutateContainer(container) }
if secretManagerConfig.vault.config.enabled { container = secretManagerConfig.vault.mutateContainer(container) }
```

**Target design**: A slice of enabled `SecretManager` implementations is built once in `parseSecretManagerConfig`. The mutation loop is provider-agnostic.

---

## 2. Proposed `SecretManager` Interface

```
File: provider/interface.go
Package: provider
```

```go
// SecretManager defines the contract that every secret manager backend must implement.
type SecretManager interface {
    // Name returns a human-readable ID for the provider (e.g., "vault", "aws", "gcp", "azure").
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

    // GetCurrentVersion queries the external secret manager for the current version
    // identifier of the secret. Used by the rotation controller (Phase 2).
    // Returns an opaque string version token. If versioning is not supported, return "".
    // [PROPOSED — Phase 2]: Implement this only when building the rotation controller.
    GetCurrentVersion(ctx context.Context) (string, error)
}
```

---

## 3. Provider Configuration Structure

Each provider owns its configuration and parses it from annotations in its own `FromAnnotations` constructor:

```
File: provider/<name>/<name>.go
```

```go
// Pattern: Each provider has a FromAnnotations constructor.
// This is called from a central parseProviders() function.

type VaultProvider struct {
    config VaultConfig
}

type VaultConfig struct {
    Enabled           bool
    Addr              string
    Path              string
    Role              string
    // ... etc
}

// FromAnnotations constructs a VaultProvider from a pod annotation map.
// Returns the provider (which may have Enabled() == false if annotation not set).
func FromAnnotations(annotations map[string]string) *VaultProvider {
    enabled, _ := strconv.ParseBool(annotations[AnnotationVaultEnabled])
    return &VaultProvider{
        config: VaultConfig{
            Enabled: enabled,
            Addr:    annotations[AnnotationVaultService],
            // ...
        },
    }
}
```

---

## 4. Registration Mechanism (Strategy Pattern)

Instead of a global map (which requires `init()` side effects), use an **explicit provider factory slice** in the config parser:

```
File: provider/registry.go
```

```go
// ProviderFactory is a function that builds a SecretManager from pod annotations.
type ProviderFactory func(annotations map[string]string) SecretManager

// All registered provider factories — order determines arg prefix order in containers.
var registeredFactories = []ProviderFactory{
    vault.FromAnnotations,
    aws.FromAnnotations,
    azure.FromAnnotations,
    gcp.FromAnnotations,
}

// BuildEnabledProviders returns only the providers that are enabled
// according to the pod's annotations.
func BuildEnabledProviders(annotations map[string]string) []SecretManager {
    var enabled []SecretManager
    for _, factory := range registeredFactories {
        p := factory(annotations)
        if p.Enabled() {
            enabled = append(enabled, p)
        }
    }
    return enabled
}
```

**Adding a new provider** then requires only:
1. Creating `provider/newprovider/newprovider.go` implementing `SecretManager`.
2. Adding `newprovider.FromAnnotations` to `registeredFactories` in `provider/registry.go`.
3. Adding annotation constants to `annotations.go`.

Zero changes to `main.go` mutation logic.

---

## 5. Updated Mutation Loop (Conceptual)

```go
// In main.go mutateContainers — after interface refactor:
func (mw *mutatingWebhook) mutateContainers(
    containers []corev1.Container,
    podSpec *corev1.PodSpec,
    providers []provider.SecretManager,  // <-- replaces secretManagerConfig
    ns string,
) (bool, error) {
    mutated := false
    for i, container := range containers {
        // ... envVar collection logic (unchanged) ...

        mutationInProgress := false  // <-- FIXED: declared inside loop
        for _, p := range providers {
            container = p.MutateContainer(container)
            mutationInProgress = true
        }

        if !mutationInProgress {
            continue
        }
        mutated = true
        // ... volume mount injection ...
        containers[i] = container
    }
    return mutated, nil
}
```

---

## 6. Provider Skeleton Template

```go
// File: provider/example/example.go
// Package: example
// Copy this file to implement a new secret manager provider.

package example

import (
    "context"
    "fmt"

    corev1 "k8s.io/api/core/v1"
    "k8s-vault-webhook/annotations"
    "k8s-vault-webhook/provider"
)

// ExampleConfig holds configuration parsed from pod annotations.
type ExampleConfig struct {
    Enabled    bool
    SecretName string
    // Add provider-specific fields here
}

// ExampleProvider implements provider.SecretManager.
type ExampleProvider struct {
    config ExampleConfig
}

// Compile-time interface check — causes build failure if interface is not satisfied.
var _ provider.SecretManager = (*ExampleProvider)(nil)

// FromAnnotations constructs an ExampleProvider from the pod annotation map.
func FromAnnotations(a map[string]string) provider.SecretManager {
    enabled, _ := strconv.ParseBool(a[annotations.AnnotationExampleEnabled])
    return &ExampleProvider{
        config: ExampleConfig{
            Enabled:    enabled,
            SecretName: a[annotations.AnnotationExampleSecretName],
        },
    }
}

func (p *ExampleProvider) Name() string { return "example" }

func (p *ExampleProvider) Enabled() bool { return p.config.Enabled }

func (p *ExampleProvider) Validate() error {
    if p.config.SecretName == "" {
        return fmt.Errorf("example provider: secret name annotation is required")
    }
    return nil
}

func (p *ExampleProvider) MutateContainer(c corev1.Container) corev1.Container {
    args := []string{"example", fmt.Sprintf("--secret-name=%s", p.config.SecretName), "--"}
    c.Args = append(args, c.Args...)
    return c
}

func (p *ExampleProvider) ExtraVolumes() []corev1.Volume {
    return nil // No extra volumes needed for this provider
}

func (p *ExampleProvider) GetCurrentVersion(ctx context.Context) (string, error) {
    // TODO: call the example secret manager API to get current secret version
    return "", nil
}
```

---

## 7. Interface Enforcement

Use compile-time assertions in each provider file to guarantee interface compliance:

```go
// At package level in each provider file:
var _ provider.SecretManager = (*VaultProvider)(nil)
var _ provider.SecretManager = (*AWSProvider)(nil)
var _ provider.SecretManager = (*AzureProvider)(nil)
var _ provider.SecretManager = (*GCPProvider)(nil)
```

This ensures a build failure — not a runtime panic — if any provider fails to implement a new interface method.
