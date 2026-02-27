# OBSERVABILITY_PLAN.md — k8s-vault-webhook

> **Purpose**: Define all custom Prometheus metrics, logging structure, and alerting rules for the webhook and rotation controller.

---

## 1. Current State (Confirmed From Source)

- `metrics.NewPrometheus(prometheus.DefaultRegisterer)` is called in `main.go`.
- This registers ONLY the `kubewebhook` framework metrics (webhook call count, latency).
- **Zero custom business metrics exist** — no per-provider, per-namespace, or injection-result tracking.
- Logrus is configured with optional JSON output. No structured log fields beyond `app=k8s-secret-injector`.

---

## 2. Custom Prometheus Metrics

All custom metrics must be registered in a new `metrics/metrics.go` package and injected into the `mutatingWebhook` struct.

### 2a. Webhook Metrics

| Metric Name | Type | Description |
|---|---|---|
| `k8sv_webhook_mutations_total` | CounterVec | Total pod mutations attempted |
| `k8sv_webhook_mutations_failed_total` | CounterVec | Total pod mutations that failed |
| `k8sv_webhook_admission_duration_seconds` | HistogramVec | Duration of entire admission webhook handler |
| `k8sv_webhook_registry_lookups_total` | CounterVec | Total image registry lookups (CMD/ENTRYPOINT resolution) |
| `k8sv_webhook_registry_lookup_duration_seconds` | HistogramVec | Duration of registry lookup API calls |
| `k8sv_webhook_registry_cache_hits_total` | Counter | Image config cache hits |
| `k8sv_webhook_registry_cache_misses_total` | Counter | Image config cache misses |
| `k8sv_webhook_excluded_pods_total` | Counter | Pods skipped due to namespace/annotation exclusion |

### 2b. Labels for Webhook Metrics

```
k8sv_webhook_mutations_total{
    provider   = "vault" | "aws" | "azure" | "gcp"
    namespace  = "<kubernetes namespace>"
    result     = "success" | "failed" | "skipped"
}

k8sv_webhook_registry_lookups_total{
    registry = "docker.io" | "ecr" | "gcr.io" | "<hostname>"
    result   = "success" | "failed" | "cache_hit"
}
```

### 2c. Rotation Controller Metrics (Phase 2)

| Metric Name | Type | Description |
|---|---|---|
| `k8sv_rotation_checks_total` | CounterVec | Total version check polls per provider |
| `k8sv_rotation_restarts_total` | CounterVec | Total rolling restarts triggered |
| `k8sv_rotation_check_errors_total` | CounterVec | Total errors during version checks |
| `k8sv_rotation_check_duration_seconds` | HistogramVec | Duration of version check API call |
| `k8sv_rotation_version_age_seconds` | GaugeVec | Seconds since last version change was detected |

```
k8sv_rotation_checks_total{
    provider  = "vault" | "aws" | "azure" | "gcp"
    namespace = "<kubernetes namespace>"
    result    = "unchanged" | "rotated" | "error"
}
```

### 2d. Histogram Buckets

```go
// admission_duration_seconds — most webhook calls should be <500ms
Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}

// registry_lookup_duration_seconds — external network calls; can vary widely
Buckets: []float64{0.1, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0}
```

---

## 3. Log Structure

### 3a. Existing Fields (Confirmed In Code)
- `app = "k8s-secret-injector"` (set in `main.go`)
- Level: `DEBUG`, `INFO`, `WARN`, `FATAL`
- Format: JSON (if `enable_json_log=true`) or text

### 3b. Required Additional Fields (Per Mutation)

All log lines during a mutation should include structured fields:

```json
{
  "level": "info",
  "app": "k8s-secret-injector",
  "provider": "vault",
  "namespace": "production",
  "pod": "myapp-xxxx-yyyy",
  "result": "mutated",
  "duration_ms": 142,
  "msg": "Pod mutation complete"
}
```

### 3c. Log Events To Add

| Event | Level | Fields |
|---|---|---|
| Pod excluded (namespace/annotation) | INFO | `namespace`, `pod`, `reason` |
| Required annotation missing | WARN | `provider`, `annotation`, `namespace` |
| Registry lookup started | DEBUG | `image`, `registry` |
| Registry lookup cache hit | DEBUG | `image` |
| Registry lookup failed | ERROR | `image`, `error` |
| `VAULT_SKIP_VERIFY=true` injected | WARN | `namespace`, `pod` — currently silent |
| Secret mutation complete | INFO | `provider`, `namespace`, `pod`, `containers_mutated` |
| Secret rotation triggered | INFO | `provider`, `namespace`, `pod_owner`, `old_version`, `new_version` |
| Version check failed | ERROR | `provider`, `namespace`, `pod`, `error` |

### 3d. Audit Log Events (Separate, Structured)

For security-sensitive operations, emit dedicated audit log entries (separate from debug logs):

```json
{
  "audit": true,
  "event": "secret_injected",
  "provider": "vault",
  "secret_path": "secrets/v2/app",
  "namespace": "production",
  "pod": "myapp-xxxx",
  "service_account": "myapp-sa",
  "timestamp": "2025-01-01T12:00:00Z"
}
```

> **Note**: Secret values must NEVER appear in logs. Only paths, roles, and versions.

---

## 4. Metrics Endpoint Configuration

Current (from `main.go`):
```
Option A: /metrics on the SAME TLS port (8443) — default
Option B: Separate HTTP port via TELEMETRY_LISTEN_ADDRESS env var
```

**Recommendation**: Always use Option B (separate port) in production. Mixing metrics with webhook TLS endpoint adds attack surface. Helm chart should expose the metrics port as a separate `Service` with optional `ServiceMonitor` for Prometheus Operator.

---

## 5. Alert Suggestions (For Prometheus Alertmanager)

```yaml
# Alert: Webhook Failure Rate Too High
- alert: K8sVaultWebhookHighFailureRate
  expr: |
    rate(k8sv_webhook_mutations_failed_total[5m]) /
    rate(k8sv_webhook_mutations_total[5m]) > 0.1
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "k8s-vault-webhook failure rate > 10%"
    description: "Provider {{ $labels.provider }} in namespace {{ $labels.namespace }} is failing"

# Alert: Webhook Admission Latency High
- alert: K8sVaultWebhookHighLatency
  expr: histogram_quantile(0.95, rate(k8sv_webhook_admission_duration_seconds_bucket[5m])) > 8
  for: 1m
  labels:
    severity: warning
  annotations:
    summary: "Webhook p95 latency > 8s — risk of admission timeout"

# Alert: Registry Lookup Failures
- alert: K8sVaultRegistryLookupErrors
  expr: rate(k8sv_webhook_registry_lookups_total{result="failed"}[5m]) > 0
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Image registry lookups failing — check registry connectivity"

# Alert: Secret Rotation Errors (Phase 2)
- alert: K8sVaultRotationErrors
  expr: rate(k8sv_rotation_check_errors_total[10m]) > 0
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Secret rotation version checks failing for {{ $labels.provider }}"
```

---

## 6. Grafana Dashboard (Planned)

A Grafana dashboard JSON should be shipped in `deploy/grafana/dashboard.json` with:
- Mutation rate (success/failure) per provider.
- Admission latency p50/p95/p99.
- Registry cache hit rate.
- Rotation events timeline (Phase 2).
- Top namespaces by mutation volume.
