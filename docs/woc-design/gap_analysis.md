# Gap Analysis Report — k8s-vault-webhook

> **Scope**: Comparing current repository implementation vs. Winter of Code (WoC) proposal milestones.
> **Method**: All findings derived exclusively from repository source code. No assumptions made.

---

## 1. Current Implementation State (Confirmed From Code)

| Area | File(s) | Status |
|---|---|---|
| Webhook HTTP server (TLS + plain) | `main.go` | ✅ Complete |
| Pod admission mutation | `main.go` (`SecretsMutator`, `mutatePod`) | ✅ Complete |
| Annotation-driven config parsing | `main.go` (`parseSecretManagerConfig`) | ✅ Complete |
| HashiCorp Vault provider | `vault.go` | ✅ Complete |
| AWS Secrets Manager provider | `aws.go` | ✅ Complete |
| Azure Key Vault provider | `azure.go` | ✅ Complete |
| GCP Secret Manager provider | `gcp.go` | ✅ Complete |
| Init container injection (`copy-k8s-secret-injector`) | `main.go` (`getInitContainers`) | ✅ Complete |
| EmptyDir volume injection | `main.go` (`getVolumes`) | ✅ Complete |
| Image registry CMD/ENTRYPOINT resolution | `registry/registry.go` | ✅ Complete |
| Docker Hub, ECR, private registry support | `registry/registry.go` | ✅ Complete |
| Image config in-memory caching | `registry/registry.go` | ✅ Complete (no expiry set) |
| ECR token caching | `registry/registry.go` | ✅ Complete (12h TTL) |
| Prometheus metrics (via kubewebhook) | `main.go` | ✅ Partial — only kubewebhook-native metrics; **no custom business metrics** |
| Health probe (`/healthz`) | `main.go` | ✅ Complete |
| Multi-secret Vault paths | `annotations.go`, `main.go` | ✅ Complete (sorted by suffix number) |
| GCP service account key injection | `gcp.go`, `main.go` | ✅ Complete |
| Vault TLS volume injection | `vault.go`, `main.go` | ✅ Complete |
| Configuration via env vars (viper) | `main.go` (`init()`) | ✅ Complete |
| Unit tests | — | ❌ **MISSING** — no `*_test.go` files found |
| Integration tests | — | ❌ **MISSING** |
| Namespace exemption / exclusion logic | — | ❌ **MISSING** |
| Secret versioning / rotation detection | — | ❌ **MISSING** |
| CRD-based policy/RBAC | — | ❌ **MISSING** |
| SecretManager plugin interface | — | ❌ **MISSING** (providers are struct-based with no shared interface) |
| Reconciliation loop / controller | — | ❌ **MISSING** |
| Structured/labeled observability metrics | — | ❌ **MISSING** |
| Audit logging | — | ❌ **MISSING** |
| Helm chart (in repo) | — | ❌ Not in this repo (lives in separate `helm-charts` repo) |

---

## 2. What Is Partially Implemented

### 2a. Prometheus Metrics
- **What exists**: The `kubewebhook` framework registers a standard Prometheus recorder via `metrics.NewPrometheus(prometheus.DefaultRegisterer)`. Framework-level metrics (webhook call latency, success/fail counts) are exposed.
- **What is missing**: No custom metrics for per-provider injection success/failure, secret resolution latency, cache hit rates, or rotation events.

### 2b. Logging
- **What exists**: Logrus structured logging with JSON support via `enable_json_log` env var. Webhook-level logs with `app=k8s-secret-injector` field.
- **What is missing**: No consistent per-injection trace ID, no pod-level correlation fields, no audit trail log format.

### 2c. Registry Caching
- **What exists**: `imageCache` is initialized with `cache.NoExpiration` — meaning image configs are cached forever. This is intentional for immutable image tags but can cause stale data for mutable tags (like `:latest`).
- **What is protected**: Images with `PullPolicy: Always` or tag `latest` are excluded from cache (via `IsAllowedToCache`).
- **Issue**: Cache is in-memory only, ephemeral to process lifetime. No warm-up on restart.

### 2d. Vault Multi-Secret Support
- **What exists**: `vault.opstree.secret.manager/secret-config-N` annotations allow multiple paths, sorted numerically.
- **What is missing**: No JSON schema validation of the `secret-config` payload contents (it's passed raw). Not documented how the injector binary parses it.

### 2e. Image Pull Secret Resolution
- **What exists**: Pod-level `imagePullSecrets` are checked, and a global default `default_image_pull_secret` can be configured.
- **What is missing**: `ServiceAccount`-level `imagePullSecrets` are NOT resolved (comment in code says "automatically attached" but this is not always true for all Kubernetes flavors).

---

## 3. What Is Missing (Not in Codebase)

| Missing Item | Impact |
|---|---|
| `*_test.go` unit test files | Critical — cannot verify correctness or prevent regressions |
| Namespace/label-based exclusion webhook rules | High — webhook may mutate system namespaces unexpectedly |
| `SecretManager` Go interface | High — providers cannot be de-coupled, adding new ones requires editing 3+ locations |
| Secret rotation / re-injection (informer loop) | High — once injected, secrets are never refreshed without pod restart |
| CRD for per-namespace or per-SA secret policy | Medium — no way to enforce policies without raw annotation editing |
| Admission webhook `failurePolicy` guidance | High — not explicitly set in webhook code; left to Helm/user configuration |
| Controller-runtime or informer reconciliation loop | Medium — rotation impossible without it |
| Structured Prometheus metrics with correct labels | Medium — standard observability gap |
| Secret version pinning with rollback | Medium — only partially mentioned in Vault multi-secret |
| CHANGELOG validation / release automation | Low |
| Deprecation of `ioutil.ReadAll` (Go 1.16 → 1.21+) | Low (but present — line 141 of `registry.go`) |

---

## 4. What Is Unstable / Risky

| Risk | Location | Concern |
|---|---|---|
| Synchronous registry API call inside admission request | `registry.go:GetImageConfig` | Can exceed webhook timeout (10–30s) if registry is slow |
| Synchronous ConfigMap/Secret API calls inside admission | `main.go:lookForValueFrom`, `lookForEnvFrom` | Admission request blocked on live kube-apiserver reads |
| `mutateContainers` function is monolithic (~87 lines) | `main.go` | Poor separation of concerns, hard to test |
| `filterAndSortMapNumStr` — silent error swallow | `main.go:L398` | `Warnf` on sorting error, but continues with empty keys — could miss secrets |
| `mutationInProgress` flag scope leak | `main.go:L216` | Flag declared outside container loop; if first container mutates, second container skips early even when it has no providers enabled... wait: re-checking reveals it's re-used across iterations without being reset. **Confirmed bug**: `mutationInProgress` is declared at line 216, set inside if blocks, never reset between container iterations. If container[0] triggers mutation but container[1] does not, container[1] still gets the `k8s-secret-injector` command injection. |
| `imageCache` has `NoExpiration` | `registry.go:L60` | Unbounded memory growth in long-running webhook processes |
| No graceful shutdown | `main.go` | `http.ListenAndServeTLS` blocking without signal handling |
| `log.Fatalf` in vendor library (fallthrough) | `main.go:L598` | Uses package-level `log` not structured `logger` |
| Go 1.16 base — `ioutil` deprecated | `registry.go:L141` | `ioutil.ReadAll` deprecated since Go 1.16, removed perspective in future |

> **Critical Bug Confirmed**: `mutationInProgress` is never reset between container loop iterations in `mutateContainers`. If any earlier container triggered any provider path, later containers that have NO secret prefix annotations will still have their `Command` overridden to `/k8s-secret/k8s-secret-injector`. This is a latent correctness bug.

---

## 5. Conflicts With Proposal Milestones

| Proposal Milestone | Conflict / Gap |
|---|---|
| Plugin-based provider architecture | Current providers use hard-coded structs + `if` chains, not a registry or interface |
| Secret rotation & versioning | Zero code support — requires a full new controller component |
| Policy/RBAC CRD | Not started — requires CRD definition, controller, and webhook integration |
| Observability (custom metrics + alerting) | Only framework-level Prometheus metrics exist |
| Unit & integration test coverage | No test files exist in the repository |
| Namespace-scoped exclusions | Not implemented; high risk to system workloads |
| Reconciliation loop | Not present; would require controller-runtime setup |
