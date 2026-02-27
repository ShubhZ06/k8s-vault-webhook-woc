# ARCHITECTURE.md — k8s-vault-webhook

> **Status**: This document reflects the CURRENT architecture plus PROPOSED changes for the Winter of Code roadmap. Proposed additions are marked `[PROPOSED]`.

---

## 1. System Overview

`k8s-vault-webhook` is a **Kubernetes Mutating Admission Webhook** (MAW). It intercepts Pod creation requests at the kube-apiserver level and modifies the Pod spec to inject secrets from external secret managers at Pod startup time.

The core design philosophy is **delegation**: the webhook itself does NOT fetch secrets. Instead, it arms each container with:
- An in-memory binary (`k8s-secret-injector`) copied via an init container.
- The necessary arguments (provider type, secret path, role, etc.) prepended to the container's original command.

This means secrets are fetched by the Pod itself, using Pod-level identity (IAM, Kubernetes ServiceAccount token, etc.), not by the webhook.

---

## 2. Current Architecture (Confirmed From Source)

```
┌───────────────────────────────────────────────────────────────┐
│                    Kubernetes API Server                        │
│                                                               │
│   kubectl apply pod.yaml                                      │
│         │                                                     │
│         ▼                                                     │
│   [Admission Chain]                                           │
│         │                                                     │
│         ▼                                                     │
│   MutatingWebhookConfiguration  ──────────────────────────────┼──► k8s-vault-webhook Pod
│   (rule: CREATE pods)                                         │         │
└───────────────────────────────────────────────────────────────┘         │
                                                                          │ HTTPS /pods
                                                                          ▼
                                                            ┌─────────────────────────┐
                                                            │  mutatingWebhook struct  │
                                                            │                         │
                                                            │  SecretsMutator()        │
                                                            │    parseConfig()         │
                                                            │    validateProvider()    │
                                                            │    mutatePod()           │
                                                            │      mutateContainers() │
                                                            └─────────────────────────┘
                                                                          │
                                              ┌───────────────────────────┼──────────────────────────┐
                                              │                           │                          │
                                              ▼                           ▼                          ▼
                                     [No Command in PodSpec]    [Inject InitContainer]     [Inject Volumes]
                                              │                           │                          │
                                              ▼                           │              EmptyDir (k8s-secret-injector)
                                   registry.GetImageConfig()              │              TLS Secret (vault-tls)
                                   (fetches CMD/ENTRYPOINT                │              GCP SA Key Secret
                                    from container registry)              │
                                              │                           ▼
                                              └──────────────► copy-k8s-secret-injector
                                                               (copies binary to EmptyDir)
                                                                          │
                                                                          ▼
                                                              Container Command Overridden:
                                                              /k8s-secret/k8s-secret-injector
                                                               [provider] [args...] -- [original cmd]
```

---

## 3. Admission Mutation Flow (Step-by-Step)

1. **kube-apiserver receives `POST /pods`** from kubectl or a controller.
2. **Admission chain triggers** the `MutatingWebhookConfiguration` that routes to this webhook at `HTTPS [webhook-service]:8443/pods`.
3. **`SecretsMutator(ctx, obj)`** is called by the kubewebhook framework.
4. **`parseSecretManagerConfig(obj)`** reads pod annotations to build a `secretManagerConfig`.
5. **Provider validation**: Checks required annotations (address, path, role for Vault; secret-name for AWS; etc.). Returns `stop=true, error` if validation fails — which causes the pod creation to be rejected.
6. **`mutatePod(pod, smCfg, ns, dryRun)`** is called.
7. **`mutateContainers`** is called first for `InitContainers`, then for `Containers`:
   - Collects env vars referencing `vault:`, `>>secret:`, or `secret:` prefixed values.
   - If no `Command` specified in container, calls `registry.GetImageConfig()` to fetch `ENTRYPOINT` + `CMD`.
   - Prepends `/k8s-secret/k8s-secret-injector` as new command; original command becomes args.
   - Calls `provider.mutateContainer()` to prepend provider-specific CLI args.
   - Appends `k8s-secret-injector` volume mount.
8. **`getInitContainers`**: If any container was mutated, prepends the `copy-k8s-secret-injector` init container.
9. **`getVolumes`**: Appends the `EmptyDir` volume plus any provider-specific volumes.
10. **JSONPatch response** is returned to kube-apiserver with all mutations.
11. **Pod is admitted** with the modified spec. kube-apiserver stores it to etcd. Scheduler places it.
12. **At pod startup**: Init container copies binary. App container runs `k8s-secret-injector [provider] [args] -- [original cmd]`. It fetches secrets, sets them in env, and `exec`s the original process.

---

## 4. [PROPOSED] Secret Versioning & Rotation Flow

```
┌─────────────────────────────────────────────────────────┐
│  SecretVersion Controller (new — controller-runtime)     │
│                                                         │
│  Watches: Pods with webhook annotations                 │
│  Interval: per-pod annotation (rotation-interval)       │
│                                                         │
│  For each watched pod:                                  │
│    1. Read pod annotation: injected-version             │
│    2. Call provider.GetCurrentVersion(ctx)              │
│    3. If version changed:                               │
│         a. Update pod annotation injected-version       │
│         b. Patch Deployment/StatefulSet for rollout     │
└─────────────────────────────────────────────────────────┘
               │
               │ provider.GetCurrentVersion()
               ▼
  ┌──────────────────┐   ┌────────────────┐   ┌──────────────┐   ┌──────────────┐
  │  Vault KV v2     │   │ AWS SecMgr     │   │ GCP SecMgr   │   │ Azure KV     │
  │  GetMetadata()   │   │ DescribeSecret │   │ AccessVersion│   │ GetSecret    │
  │  .Version        │   │ .VersionId     │   │ .Name        │   │ .Properties  │
  └──────────────────┘   └────────────────┘   └──────────────┘   └──────────────┘
```

**Key Decisions:**
- Use `controller-runtime` (already in `go.mod` as an indirect dep via `k8s.io`). Needs explicit direct dependency.
- Controller watches `pods` with a `labelSelector` or `fieldSelector` for the webhook annotations.
- Rolling restart is done by patching `spec.template.metadata.annotations` on the owning `Deployment`/`StatefulSet` — triggering a standard Kubernetes rollout, not a force-delete.
- Version state tracked in pod annotation `vault.opstree.secret.manager/injected-version`.

---

## 5. [PROPOSED] Failure Handling Strategy

### Webhook Failure Modes

| Failure | failurePolicy: Fail | failurePolicy: Ignore |
|---|---|---|
| Webhook pod crashes | All pod creation blocked | Pod created WITHOUT secrets |
| Registry timeout | Pod creation blocked | Pod created, may fail at startup |
| Provider annotation missing | Error returned → pod blocked (current behavior) | Same |

**Recommendation**: 
- Default to `failurePolicy: Ignore` for non-security-critical workloads.
- Document a `failurePolicy: Fail` option for high-security clusters where un-injected pods are worse than scheduling failures.
- Use `namespaceSelector` to always exclude `kube-system`, `kube-public`, `cert-manager`.

### Webhook Timeout
- Default Kubernetes admission timeout is **10 seconds**. If registry lookup takes longer, use a context with 8s timeout to fail fast with a useful error, rather than a silent timeout.
- Image config cache significantly mitigates this for re-scheduled pods.

### Graceful Shutdown `[PROPOSED]`
- Current `http.ListenAndServeTLS` has no shutdown handler.
- Add `signal.NotifyContext` with `SIGTERM`/`SIGINT` and `http.Server.Shutdown(ctx)` with a 15s drain period.

---

## 6. [PROPOSED] Namespace Exclusion Architecture

```
SecretsMutator(ctx, obj)
    │
    ▼
isExcluded(namespace, annotations) ──► true → return (false, nil) immediately
    │
    false
    ▼
parseSecretManagerConfig(obj)
    ...
```

Exclusion sources (in priority order):
1. Pod annotation `k8s-vault-webhook/exclude: "true"` — per-pod opt-out.
2. `EXCLUDED_NAMESPACES` env var (comma-separated) — cluster-wide namespace blocklist.
3. `MutatingWebhookConfiguration.namespaceSelector` (recommended at Helm chart level).

---

## 7. Security Posture Notes

- The webhook binary itself requires only read access to `ConfigMaps` and `Secrets` (for `envFrom` resolution) and read access to `imagePullSecrets`.
- `[NEEDS VERIFICATION]`: What RBAC role is currently assigned to the webhook ServiceAccount in the Helm chart? This determines blast radius if the webhook is compromised.
- The `k8s-secret-injector` binary (separate project, referenced as `quay.io/opstree/k8s-secret-injector:4.0`) does the actual secret fetching. Its permissions derive from the Pod's ServiceAccount — correct by design.
- `VAULT_SKIP_VERIFY: "true"` is injected when no TLS secret is provided (line 48, `vault.go`). This disables TLS verification and should produce a warning log, not silently proceed.
