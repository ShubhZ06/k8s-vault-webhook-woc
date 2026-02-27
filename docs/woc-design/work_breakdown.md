# Technical Work Breakdown Structure — k8s-vault-webhook WoC

---

## Feature 1: SecretManager Plugin Interface Refactor

### Objective
Replace the current hard-coded `if azure... if aws... if gcp... if vault...` chains in `mutateContainers` and `SecretsMutator` with a unified Go interface and a plugin registry, enabling new providers to be added without modifying core mutation logic.

### Current State
- `vault.go`, `aws.go`, `azure.go`, `gcp.go` each define their own struct types with no shared interface.
- `mutateContainers` in `main.go` calls each provider with separate `if` blocks (lines 265–283).
- `SecretsMutator` in `main.go` validates each provider with separate `if` blocks (lines 416–483).
- `parseSecretManagerConfig` in `main.go` reads all 4 providers regardless of which is enabled.

### Required Code Changes (File-Level)
| File | Change |
|---|---|
| `NEW: provider/interface.go` | Define `SecretManager` interface |
| `NEW: provider/registry.go` | Provider registration map |
| `vault.go` → `provider/vault/vault.go` | Implement `SecretManager` interface |
| `aws.go` → `provider/aws/aws.go` | Implement `SecretManager` interface |
| `azure.go` → `provider/azure/azure.go` | Implement `SecretManager` interface |
| `gcp.go` → `provider/gcp/gcp.go` | Implement `SecretManager` interface |
| `main.go` | Replace `if` chains with `for _, p := range enabledProviders` loop |
| `types.go` | Update `secretManagerConfig` to use provider registry |

### Required Architectural Changes
- Introduce a `provider/` directory as a sub-package.
- `parseSecretManagerConfig` returns a slice of enabled `SecretManager` implementations.
- Each provider's `Enabled()` method gates inclusion.

### Risks
- Breaking change to `secretManagerConfig` struct — all callers must be updated.
- Compilation risk if interface methods are under-specified.

### Testing Strategy
- Unit test for each provider implementing the interface.
- Table-driven test: given annotations → expected provider slice.
- Integration test: create a fake webhook request, assert correct container mutation.

### Acceptance Criteria
- Adding a new provider requires creating ONE new file in `provider/` only.
- All existing 4 providers pass their existing behavior tests.
- `mutateContainers` contains zero provider-specific `if` blocks.

---

## Feature 2: Fix `mutationInProgress` Flag Bug

### Objective
Fix the confirmed bug where `mutationInProgress` is not reset between container iterations in `mutateContainers`, causing unintended command injection into containers that have no secret prefix annotations.

### Current State
- `mutationInProgress` is declared at line 216, set to `true` if any provider is enabled, but never reset at the start of each container iteration.
- Effect: Once any container sets `mutationInProgress = true`, all subsequent containers in the same pod also have their `Command` overwritten.

### Required Code Changes (File-Level)
| File | Change |
|---|---|
| `main.go` | Move `var mutationInProgress bool` declaration inside the `for i, container := range containers` loop body |

### Risks
- Low risk change — 1-line scope fix, but MUST be covered by a regression test.

### Testing Strategy
- Unit test: Pod with 2 containers where container[0] has vault annotation but container[1] does not — assert container[1] command is NOT replaced.

### Acceptance Criteria
- The regression test passes.
- CI pipeline added to prevent regression.

---

## Feature 3: Namespace / Workload Exclusion

### Objective
Prevent the webhook from mutating system-critical namespaces (e.g., `kube-system`, `cert-manager`) or specific pods/deployments via configurable exclusion lists. This is a safety guard for production clusters.

### Current State
- No exclusion logic exists anywhere in the codebase.
- The `SecretsMutator` processes all incoming pod admission requests unconditionally.
- The webhook `failurePolicy` setting is entirely left to Helm/user — no guidance in code.

### Required Code Changes (File-Level)
| File | Change |
|---|---|
| `main.go` | Add namespace exclusion check at the top of `SecretsMutator` |
| `main.go` `init()` | Add viper defaults for `excluded_namespaces` (comma-separated) |
| `NEW: exclusion.go` | `isExcluded(ns string, annotations map[string]string) bool` helper |
| `annotations.go` | Add `AnnotationExclude = "k8s-vault-webhook/exclude"` opt-out annotation constant |

### Required Architectural Changes
- Webhook `MutatingWebhookConfiguration` in Helm chart should include `namespaceSelector` to exclude `kube-system` by default.
- Document `failurePolicy: Ignore` vs `Fail` tradeoff in ARCHITECTURE.md.

### Risks
- If exclusion is misconfigured, legitimate pods may not get secrets injected.
- `failurePolicy: Fail` can cause cluster-wide pod scheduling failures if webhook crashes.

### Testing Strategy
- Unit test: excluded namespace → `SecretsMutator` returns `(false, nil)` immediately.
- Unit test: pod with `k8s-vault-webhook/exclude: "true"` annotation → skipped.

### Acceptance Criteria
- `kube-system` is excluded by default.
- Opt-out annotation documented and tested.

---

## Feature 4: Secret Rotation / Versioning

### Objective
Implement a mechanism to detect when a secret has been updated in the external provider (Vault, AWS, GCP, Azure) and trigger pod re-injection. This requires a controller loop separate from the admission webhook.

### Current State
- Once a pod is running, secrets are NEVER refreshed unless the pod is restarted.
- No controller, informer, or reconciliation logic exists.
- The webhook itself is stateless — it has no knowledge of previously processed pods.

### Required Code Changes (File-Level)
| File | Change |
|---|---|
| `NEW: controller/controller.go` | controller-runtime based reconciler for pod secret refresh |
| `NEW: controller/watcher.go` | Annotation-driven secret version polling per provider |
| `main.go` | Start controller manager concurrently alongside webhook server |
| `annotations.go` | Add `AnnotationSecretVersion`, `AnnotationRotationInterval` constants |
| `NEW: provider/interface.go` | Add `GetCurrentVersion(ctx) (string, error)` to `SecretManager` interface |

### Required Architectural Changes
- Introduce `controller-runtime` manager that watches Pods/Deployments with the webhook's annotations.
- On a configurable `rotationInterval`, query the external provider for the current secret version.
- If version differs from the version stored in a pod annotation (`vault.opstree.secret.manager/injected-version`), trigger a rolling restart via `kubectl rollout restart`-equivalent patch.
- A new `SecretVersionStore` (CRD or ConfigMap) tracks last known version per pod/namespace.

### Risks
- Rolling restarts can cause service disruptions if not coordinated with PodDisruptionBudgets.
- External provider rate limits — must implement exponential backoff.
- Version concept differs by provider (Vault has KV versions, AWS has version IDs, GCP has version numbers, Azure has versions and enabled states).

### Testing Strategy
- Unit test: mock provider returns version `v2` when pod annotation says `v1` → assert rollout patch is triggered.
- Integration test with a real Vault dev instance and a test pod.

### Acceptance Criteria
- Pod annotations are updated with `injected-version` on first injection.
- Controller detects version change within 1 rotation interval.
- Rolling restart is triggered only when version genuinely changes.
- No restart is triggered when version is identical.

---

## Feature 5: CRD-Based Policy / RBAC

### Objective
Introduce a `VaultSecretPolicy` CRD to allow cluster admins to define which namespaces/service accounts are permitted to use which secret paths, decoupling security policy from raw annotations on pods.

### Current State
- All configuration is done purely via pod annotations.
- No RBAC enforcement exists — any pod in any namespace can use any vault path if the webhook is deployed.
- No CRD, no controller, no policy resource.

### Required Code Changes (File-Level)
| File | Change |
|---|---|
| `NEW: api/v1alpha1/vaultsecretpolicy_types.go` | CRD struct definition with kubebuilder markers |
| `NEW: api/v1alpha1/zz_generated.deepcopy.go` | Auto-generated (via controller-gen) |
| `NEW: config/crd/bases/` | Generated CRD YAML manifests |
| `NEW: controller/policy_controller.go` | Reconciler for VaultSecretPolicy resources |
| `main.go` | Add policy admission check: does this pod's SA have an allowed policy? |

### Required Architectural Changes
- controller-gen and controller-runtime are required dependencies.
- Policy controller watches `VaultSecretPolicy` objects, builds an in-memory allow-list.
- Webhook's `SecretsMutator` checks the allow-list before proceeding with mutation.

### Risks
- Chicken-and-egg: webhook + CRD controller must both be running for policy enforcement.
- CRD schema versioning is complex to get right initially.
- Policy cache invalidation requires care — stale cache could allow or deny wrong requests.

### Testing Strategy
- Unit test: policy allows namespace A → pod in A is mutated.
- Unit test: policy denies namespace B → pod in B rejected with informative error.
- e2e test: deploy CRD + controller + webhook, create policy, verify enforcement.

### Acceptance Criteria
- A `VaultSecretPolicy` with `allowedNamespaces: [production]` prevents mutation in `staging`.
- CRD is validated via OpenAPI schema (kubebuilder markers).

---

## Feature 6: Observability (Custom Prometheus Metrics)

### Objective
Add structured, labeled Prometheus metrics that go beyond the framework-level default metrics and provide actionable signal for operators.

### Current State
- Only framework-level metrics from `kubewebhook` are exposed.
- No custom metrics for injection success/failure per provider, per namespace, secret resolution latency, or cache performance.

### Required Code Changes (File-Level)
| File | Change |
|---|---|
| `NEW: metrics/metrics.go` | Define and register all custom Prometheus counters/histograms/gauges |
| `main.go` | Inject `metrics.Recorder` into `mutatingWebhook` struct |
| `types.go` | Add `metricsRecorder` field to `mutatingWebhook` |
| `main.go:mutateContainers` | Record injection events before return |
| `registry/registry.go` | Record cache hit/miss counters |

### Testing Strategy
- Unit test: after a successful mutation, verify counter was incremented with correct labels.
- Integration test: scrape `/metrics` endpoint and assert metric presence.

### Acceptance Criteria
- See OBSERVABILITY_PLAN.md for full metric list.
- All metrics have `provider`, `namespace`, `result` labels at minimum.

---

## Feature 7: Test Coverage

### Objective
Establish a baseline of unit, integration, and e2e tests to ensure correctness and prevent regressions during WoC development.

### Current State
- **Zero test files exist in the repository.** This is the highest-priority gap.

### Required Code Changes (File-Level)
| File | Change |
|---|---|
| `NEW: main_test.go` | Tests for `mutateContainers`, `mutatePod`, `parseSecretManagerConfig`, `filterAndSortMapNumStr` |
| `NEW: annotation_test.go` | Verify all annotation constants are non-empty and unique |
| `NEW: vault_test.go` | Test `vault.mutateContainer`, `vault.setArgs`, `vault.setEnvVars` |
| `NEW: aws_test.go` | Test `aws.mutateContainer`, `aws.setArgs` |
| `NEW: azure_test.go` | Test `azure.mutateContainer`, `azure.setArgs` |
| `NEW: gcp_test.go` | Test `gcp.mutateContainer`, `gcp.setArgs` |
| `NEW: registry/registry_test.go` | Test `parseContainerImage`, `IsAllowedToCache`, `fixDockerHubImage` |
| `NEW: e2e/` | End-to-end tests using envtest |

### Testing Strategy
- Use `k8s.io/client-go/kubernetes/fake` for fake kube client in unit tests.
- Use `sigs.k8s.io/controller-runtime/pkg/envtest` for integration tests.
- Target 80% unit test coverage for all files in `main` package.

### Acceptance Criteria
- `make test` runs all tests.
- CI pipeline enforces minimum 70% coverage gate.
- The `mutationInProgress` regression test is included.

---

## Feature 8: Go Version & Dependency Modernization

### Objective
Upgrade Go version, replace deprecated APIs, and modernize dependencies.

### Current State
- `go.mod` specifies `go 1.16`.
- `ioutil.ReadAll` is deprecated since Go 1.16 (use `io.ReadAll`).
- `registry.go` uses `io/ioutil` package-level import.
- `kubewebhook v0.11.0` — needs evaluation for whether a newer version is available.

### Required Code Changes (File-Level)
| File | Change |
|---|---|
| `go.mod` | Upgrade to Go 1.21+ |
| `registry/registry.go` | Replace `ioutil.ReadAll` → `io.ReadAll`, remove `io/ioutil` import |
| `Dockerfile` | Update `FROM golang:1.16` → `golang:1.21-alpine` or `golang:1.22` |

### Risks
- Minor — Go maintains backward compatibility. Check if `kubewebhook` library has breaking API changes.

### Acceptance Criteria
- `go vet ./...` passes with zero warnings.
- `golangci-lint` passes.
- `gosec` scan passes (already scripted in `scripts/gosec.sh`).

---

## Implementation Priority Order

```
P0 (Blocking): Feature 2 (Bug Fix), Feature 7 (Tests)
P1 (Foundation): Feature 1 (Plugin Interface), Feature 3 (Namespace Exclusion), Feature 8 (Go Modernization)
P2 (Value-Add): Feature 6 (Observability), Feature 4 (Rotation)
P3 (Advanced): Feature 5 (CRD Policy)
```
