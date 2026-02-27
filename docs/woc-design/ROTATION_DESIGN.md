# ROTATION_DESIGN.md — k8s-vault-webhook

> **Purpose**: Design document for secret versioning detection and automated pod re-injection on secret rotation.

---

## 1. Problem Statement

Once a pod is created and running, the `k8s-secret-injector` binary fetches secrets exactly once — at container startup. If the secret value changes in the external provider (Vault, AWS, GCP, Azure), the running pod continues with the old value until it is restarted.

**Current behavior**: No rotation or re-injection mechanism exists. Zero code. Zero annotations.

---

## 2. Design Goals

- Detect version changes in external secret managers without polling every second.
- Trigger graceful pod restart (rolling rollout) only when version actually changes.
- No disruption to pods that are not using the webhook.
- Configurable refresh interval per pod (not global).
- Backward compatible — existing pods without the rotation annotation are unaffected.

---

## 3. Technology Choice: controller-runtime

**Choice**: `sigs.k8s.io/controller-runtime` (already in `go.mod` as transitive dependency of `k8s.io/client-go`; needs explicit direct dependency).

**Rationale**:
- `controller-runtime` provides watched `Reconcile` loops, rate limiting, backoff, and leader election out of the box.
- A simple `cache.SharedInformerFactory` loop would require manual implementation of all the above.
- `controller-runtime` is the industry-standard choice for production Kubernetes controllers.

**NOT chosen**: 
- `client-go` informers alone — too much boilerplate with no safety guarantees.
- Webhook-internal polling goroutines — webhook is stateless and should remain so; polling inside a webhook would break horizontal scaling.

---

## 4. Rotation Flow (Step-by-Step)

```
[Controller Start]
    │
    ▼
Watch all Pods in cluster
    │ Filter: only pods with webhook annotation AND rotation-interval annotation
    ▼
For each pod in watch queue:
    │
    ▼
Read pod annotation: vault.opstree.secret.manager/injected-version
    │
    ▼
Call provider.GetCurrentVersion(ctx)
    │
    ├── Same version? ──► Requeue after rotationInterval. Done.
    │
    └── Different version?
            │
            ▼
        Identify pod owner (Deployment, StatefulSet, DaemonSet) via OwnerReferences
            │
            ▼
        Patch owner's pod template annotation:
            kubectl.kubernetes.io/restartedAt = time.Now().UTC()
            │
            ▼
        Kubernetes triggers rolling restart (controlled by RollingUpdate strategy)
            │
            ▼
        New pod is created → webhook intercepts → secrets re-injected with new version
            │
            ▼
        New pod's annotation injected-version = new version
```

---

## 5. New Annotations for Rotation

Add to `annotations.go`:

```
vault.opstree.secret.manager/rotation-interval
    Value: Go duration string, e.g. "5m", "1h", "30s"
    Default: "" (empty = rotation disabled for this pod)

vault.opstree.secret.manager/injected-version
    Value: opaque version token set by webhook at injection time
    Managed: written by webhook, read by controller
    Example values:
      Vault:  "7"  (KV v2 version number)
      AWS:    "AWSCURRENT" or "abc123def456" (VersionId)
      GCP:    "3" (version number)
      Azure:  "abc123" (secret version identifier)
```

> **Note**: Provider-agnostic design — the version string is opaque. The controller does not need to understand the format; it only compares old vs. new.

---

## 6. Provider-Specific Version Retrieval

Each provider implements `GetCurrentVersion(ctx context.Context) (string, error)` in the `SecretManager` interface.

| Provider | API Used | Version Field |
|---|---|---|
| HashiCorp Vault | `GET /v1/{path}?version=` metadata | `data.metadata.version` (int) |
| AWS Secrets Manager | `DescribeSecret` API | `VersionIdsToStages` (current AWSCURRENT stage) |
| GCP Secret Manager | `projects/{project}/secrets/{name}/versions/latest` | `name` suffix number |
| Azure Key Vault | `GetSecretProperties` | `properties.version` (string) |

`[NEEDS VERIFICATION]`: Whether the `k8s-secret-injector` binary already exposes version information on stdout/stderr that the init container could capture. If so, the webhook could capture version at injection time without querying the provider again.

---

## 7. Webhook-Side: Recording the Version at Injection Time

When the webhook mutates a container, it should also record the snapshot of the current secret version in a pod annotation — captured before the pod starts.

```
Approach A (Recommended): Webhook queries provider at mutation time.
  Pro: Version is accurate at injection moment.
  Con: Adds a remote call inside the admission request path — latency risk.
  Mitigation: Use a short context timeout (3-5s), cache aggressively.

Approach B: Injector binary writes version back to pod annotation post-startup.
  Pro: No latency in admission path.
  Con: Requires RBAC permission for the pod to annotate itself (security concern).
  Con: Race condition — controller may check before pod finishes writing.

Decision: Start with Approach B for simplicity; document the security tradeoff.
Use a Kubernetes downward API projected volume or a sidecar RBAC binding to give
the pod permission to write only to its own annotation.
```

---

## 8. Rotation Controller Concurrency Model

```
Manager (controller-runtime)
    │
    ├── Reconciler (SecretVersionReconciler)
    │       Rate Limiter: exponential backoff (base 5s, max 10m)
    │       Max Concurrent Reconciles: 10 (configurable)
    │       RequeueAfter: per-pod rotation-interval annotation
    │
    └── Leader Election: enabled
            Only one controller instance polls providers at any time.
            Prevents thundering herd against Vault/AWS/GCP on restart.
```

---

## 9. Performance Considerations

| Concern | Mitigation |
|---|---|
| Rate limits on Vault/AWS/GCP | Exponential backoff on error, per-pod interval, don't poll all pods simultaneously |
| Thundering herd on controller restart | Stagger initial reconcile with jitter (controller-runtime WorkQueue supports this) |
| Large clusters (1000s of pods) | Only watch pods with the rotation annotation — use label selector on the informer |
| Registry call in admission path | Already cached in `imageCache`; rotation does NOT trigger registry re-query |
| AWS API rate limits | ECR token is already cached 12h in `registry.go`; Secret Manager calls need separate cache |

---

## 10. Backward Compatibility

- **Existing pods without `rotation-interval` annotation**: Controller ignores them entirely. Zero behavior change.
- **Existing pods without `injected-version` annotation**: Controller sets initial version on first reconcile. If version retrieval fails (e.g., network issue), controller logs error and requeues — pod is NOT restarted.
- **No restart-on-first-run**: On first controller startup, reconciler only checks and records version. Rolling restart is triggered only on SUBSEQUENT version difference detection.
- **Disable rotation per-pod**: Remove `rotation-interval` annotation or set to `""`.

---

## 11. Risk Register

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Rolling restart causes downtime | Medium | High | Requires PDB to be configured by user; document requirement |
| Version API is unavailable | Medium | Low | Requeue with backoff; do NOT restart pod on error |
| Multiple controllers racing | Low | High | Leader election prevents this |
| Annotation drift (annotation set but pod deleted mid-reconcile) | Medium | Low | controller-runtime handles object not found gracefully |
| Rotation interval too short (< 1m) | Medium | Medium | Validate annotation, set minimum floor of 60s |
