# Implementation Readiness Checklist — k8s-vault-webhook WoC

> All items must be confirmed before writing production code for the corresponding feature.
> Items marked ⚠️ need a decision or verification from the project maintainer.

---

## Tier 0 — Must Complete First (Blocking Everything)

- [ ] **Bug confirmed**: Verify the `mutationInProgress` flag scope bug by running a test pod with 2 containers (one annotated, one not) against the current webhook. Observe if both containers get their command overridden.
- [ ] **No tests baseline**: Confirm `go test ./...` currently produces zero test results (no `_test.go` files). Record coverage baseline as `0%`.
- [ ] ⚠️ **Go module version decision**: Decide whether to upgrade from `go 1.16` to `go 1.21+` now or later. This affects `ioutil` deprecation fix and dependency updates.
- [ ] ⚠️ **`kubewebhook` upgrade path**: Determine if `slok/kubewebhook v0.11.0` has API-breaking changes in newer versions. Check if upgrade is in scope for WoC.
- [ ] **CI pipeline check**: Verify the Azure Pipeline (`/.azure-pipelines`) and Travis CI (`.travis.yml`) are still functional. If not, decide which CI system to use for WoC.
- [ ] **Dev environment setup**: Confirm minikube or kind cluster is available with the webhook deployed. All contributing students must validate `make build-code && make test` before starting.

---

## Tier 1 — Foundation (Required Before Feature Work)

### Provider Interface
- [ ] ⚠️ **Interface method set approved**: Review `SecretManager` interface in `PROVIDER_INTERFACE.md`. Maintainer must approve method signatures before they are coded.
- [ ] ⚠️ **`GetCurrentVersion` in v1 or v2?**: Decide if `GetCurrentVersion` should be in the initial interface (requires all 4 providers to implement it immediately) or added in a v2 interface for Phase 2 only.
- [ ] **Provider directory structure agreed**: Maintainer confirms `provider/<name>/<name>.go` directory layout before any files are created.

### Namespace Exclusion
- [ ] ⚠️ **Default excluded namespaces list approved**: Confirm which namespaces are excluded by default. Suggested: `kube-system`, `kube-public`, `kube-node-lease`. Others?
- [ ] ⚠️ **`failurePolicy` default decision**: Choose `Ignore` or `Fail` as the default in the Helm chart `MutatingWebhookConfiguration`. Document the security tradeoff explicitly.

### Go Modernization
- [ ] **`ioutil.ReadAll` → `io.ReadAll`** replacement in `registry/registry.go` approved.
- [ ] **Dockerfile base image** updated from `golang:1.16` to agreed target version.

---

## Tier 2 — Feature-Level Readiness

### Secret Rotation / Versioning
- [ ] ⚠️ **Approach A vs B for recording injected-version**: Decide whether the webhook queries the provider at mutation time (latency risk) or the injector binary writes back post-startup (RBAC complexity). See `ROTATION_DESIGN.md` §7.
- [ ] ⚠️ **Minimum rotation interval**: Agree on a minimum floor (e.g., 60s) to prevent rate limits.
- [ ] **controller-runtime added as direct dependency** in `go.mod`. Verify version compatibility with `k8s.io/api v0.21.0`.
- [ ] **Leader election configuration**: Confirm if multi-replica webhook deployments are used and thus whether leader election is mandatory.
- [ ] **Rolling restart impact assessment**: Confirm that existing Deployments use `RollingUpdate` strategy (not `Recreate`) before triggering rotation-driven restarts.

### CRD Policy
- [ ] ⚠️ **CRD scope decision**: Cluster-scoped `VaultSecretPolicy` (as designed) or Namespace-scoped? Namespace-scoped is less powerful but safer for multi-tenant clusters.
- [ ] ⚠️ **Default-deny vs default-allow**: Is the policy model default-deny (no policy = access blocked) or default-allow (no policy = access permitted as today)? This is a **breaking change** if default-deny is chosen.
- [ ] **controller-gen version agreed upon** for CRD manifest generation.
- [ ] **`ENABLE_POLICY_ENFORCEMENT` env var name finalized** for backward-compatible rollout.

### Observability
- [ ] **All metric names in `OBSERVABILITY_PLAN.md` approved** by maintainer (naming conventions).
- [ ] **Histogram bucket values reviewed** for production-representative latency ranges.
- [ ] ⚠️ **Audit log format decision**: Plain text, JSON, or a separate audit sink (e.g., Kubernetes audit webhook)?
- [ ] **Grafana dashboard in repo or separate?**

---

## Tier 3 — Testing & Release

- [ ] **Target unit test coverage agreed**: Minimum 70% or 80% for `make test` gate?
- [ ] **envtest or integration cluster for integration tests?**: Confirm if `sigs.k8s.io/controller-runtime/pkg/envtest` is acceptable or if a real cluster is required.
- [ ] **e2e test provider accounts**: Do contributing students have access to a Vault dev instance, AWS account (with Secrets Manager), or GCP/Azure account for integration tests? Or will all providers use mock clients?
- [ ] **Changelog format**: Continue existing `CHANGELOG.md` format or adopt Conventional Commits?
- [ ] **Helm chart updates**: Confirm whether Helm chart changes (in the separate `helm-charts` repo) are in WoC scope. If yes, who maintains the chart PR?

---

## Confirmed Facts (No Decision Needed)

These are facts derived from the codebase that do not require clarification:

| Fact | Source |
|---|---|
| `mutationInProgress` flag is NOT reset per container iteration — confirmed bug | `main.go:L216` |
| Zero test files exist | Repository-wide search |
| `ioutil.ReadAll` is present and deprecated | `registry/registry.go:L141` |
| Vault TLS skip verify is silently injected with no warning log | `vault.go:L46-51` |
| Image cache has no expiry (`NoExpiration`) — unbounded memory | `registry/registry.go:L60` |
| No graceful shutdown handler exists | `main.go:L591-599` |
| ConfigMap/Secret reads in admission path are synchronous (no cache) | `main.go:lookForValueFrom`, `lookForEnvFrom` |
| 4 providers exist with confirmed working code: Vault, AWS, Azure, GCP | Source code |
| Helm chart is in a SEPARATE repository (not this one) | `README.md` |

---

## Sign-Off Required Before Code Phase Starts

| Item | Responsible | Status |
|---|---|---|
| Bug fix for `mutationInProgress` reviewed and approved | Maintainer | ⬜ Pending |
| Provider interface method signatures approved | Maintainer | ⬜ Pending |
| `failurePolicy` default chosen | Maintainer | ⬜ Pending |
| CRD scope and enforcement model decided | Maintainer | ⬜ Pending |
| Go version upgrade approved | Maintainer | ⬜ Pending |
| Test coverage minimum agreed | Team | ⬜ Pending |
