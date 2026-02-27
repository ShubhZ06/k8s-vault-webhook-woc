# CRD_DESIGN.md — VaultSecretPolicy

> **Purpose**: Define the `VaultSecretPolicy` Custom Resource Definition (CRD) schema, controller responsibilities, and how it integrates with the webhook's mutation pipeline.

> **Status**: This is a PROPOSED design. No CRD code exists in the repository.

---

## 1. Goal

Allow cluster administrators to define fine-grained policies controlling which Kubernetes namespaces and/or ServiceAccounts are permitted to use which secret paths from which external providers. This replaces the current implicit "anyone with the right annotations can fetch any secret" model.

---

## 2. CRD: `VaultSecretPolicy`

### 2a. API Group and Version

```
Group:   policy.opstree.secret.manager
Version: v1alpha1
Kind:    VaultSecretPolicy
Scope:   Cluster (applies cluster-wide; namespace-scoped policies can be a v1beta1 addition)
```

### 2b. High-Level Schema (Pseudo Go Struct with kubebuilder markers)

```go
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

type VaultSecretPolicy struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   VaultSecretPolicySpec   `json:"spec"`
    Status VaultSecretPolicyStatus `json:"status,omitempty"`
}

type VaultSecretPolicySpec struct {
    // Provider specifies which external secret manager this policy applies to.
    // +kubebuilder:validation:Enum=vault;aws;azure;gcp
    Provider string `json:"provider"`

    // AllowedNamespaces lists namespaces permitted to use this policy.
    // Use ["*"] to allow all namespaces (cluster admin use only).
    // +kubebuilder:validation:MinItems=1
    AllowedNamespaces []string `json:"allowedNamespaces"`

    // AllowedServiceAccounts lists ServiceAccount names permitted within the allowed namespaces.
    // Use ["*"] to allow all service accounts in the listed namespaces.
    // Format: "namespace/serviceaccount" or just "serviceaccount" (applies to all allowed namespaces)
    // +optional
    AllowedServiceAccounts []string `json:"allowedServiceAccounts,omitempty"`

    // AllowedSecretPaths lists secret paths or path prefixes the permitted subjects can access.
    // Supports exact match and prefix glob: e.g., "secrets/app/*" matches any path under secrets/app/
    // Use ["*"] to allow access to all paths (not recommended in production).
    // +kubebuilder:validation:MinItems=1
    AllowedSecretPaths []string `json:"allowedSecretPaths"`

    // ProviderConfig holds provider-specific configuration requirements.
    // +optional
    ProviderConfig *ProviderPolicyConfig `json:"providerConfig,omitempty"`
}

type ProviderPolicyConfig struct {
    // VaultRole if set, restricts which Vault role is allowed with this policy.
    // +optional
    VaultRole string `json:"vaultRole,omitempty"`

    // AWSRegion if set, restricts which AWS region's secrets can be accessed.
    // +optional
    AWSRegion string `json:"awsRegion,omitempty"`

    // GCPProjectID if set, restricts which GCP project's secrets can be accessed.
    // +optional
    GCPProjectID string `json:"gcpProjectID,omitempty"`
}

type VaultSecretPolicyStatus struct {
    // Conditions represent the latest available observations of the policy state.
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // LastAppliedGeneration tracks the generation of this policy last synced by the controller.
    LastAppliedGeneration int64 `json:"lastAppliedGeneration,omitempty"`
}
```

---

## 3. Example Policy YAMLs

### 3a. Allow Production Namespace to Access Specific Vault Paths

```yaml
apiVersion: policy.opstree.secret.manager/v1alpha1
kind: VaultSecretPolicy
metadata:
  name: production-vault-policy
spec:
  provider: vault
  allowedNamespaces:
    - production
  allowedServiceAccounts:
    - myapp-sa
    - payment-service-sa
  allowedSecretPaths:
    - "secrets/v2/production/myapp/*"
    - "secrets/v2/production/payment-service/db-password"
  providerConfig:
    vaultRole: "production-role"
```

### 3b. Allow All Namespaces to Use AWS Secrets in us-east-1 (Read-Only Legacy)

```yaml
apiVersion: policy.opstree.secret.manager/v1alpha1
kind: VaultSecretPolicy
metadata:
  name: aws-us-east-1-readonly
spec:
  provider: aws
  allowedNamespaces:
    - "*"
  allowedSecretPaths:
    - "*"
  providerConfig:
    awsRegion: "us-east-1"
```

### 3c. Deny All (Default — No Policy = No Access)

No explicit "deny" policy needed. The enforcement model is **default-deny**: if no `VaultSecretPolicy` grants access to the pod's namespace + ServiceAccount + secret path, the mutation is denied.

---

## 4. Controller Responsibilities

```
VaultSecretPolicyController (controller-runtime Reconciler)

On VaultSecretPolicy CREATE/UPDATE/DELETE:
    1. Validate spec fields (AllowedSecretPaths glob syntax, provider enum).
    2. Build/update the in-memory policy allow-list (PolicyCache).
    3. Update Status.Conditions with Ready=True/False.

PolicyCache (in-memory, rebuilt on controller start + on every policy event):
    Map: (namespace, serviceaccount) → []AllowedPolicy

On controller crash/restart:
    List all VaultSecretPolicy objects → rebuild PolicyCache from scratch.
    Cache rebuild is guaranteed before webhook starts serving (startup probe).
```

---

## 5. Webhook Integration

In `SecretsMutator`, after namespace exclusion check:

```
SecretsMutator(ctx, obj)
    │
    ├── isExcluded()? → return (false, nil)
    │
    ▼
    policyCache.IsAllowed(namespace, serviceAccount, provider, secretPath)
    │
    ├── false → return (true, ErrPolicyDenied{...}) 
    │   (stop=true → pod creation rejected with descriptive message)
    │
    └── true → proceed with mutatePod()
```

PolicyCache lookup is an in-memory operation (no network call) — zero admission latency impact.

---

## 6. Tooling Requirements

| Tool | Purpose |
|---|---|
| `controller-gen` | Generate CRD YAML from Go struct markers, and deepcopy methods |
| `kustomize` | Manage CRD base + overlay configurations |
| `envtest` | Integration testing with a real API server |
| `go generate ./...` | Trigger controller-gen from Makefile |

`Makefile` targets to add:
```makefile
generate:  ## Generate CRD manifests and deepcopy methods
    controller-gen crd:trivialVersions=true rbac:roleName=k8s-vault-webhook object paths="./..." output:crd:artifacts:config=config/crd/bases

install-crds:  ## Install CRDs into cluster
    kubectl apply -f config/crd/bases/
```

---

## 7. RBAC for the Webhook (Extended)

With a policy controller, the webhook's `ClusterRole` needs:

```yaml
rules:
  # Existing: read ConfigMaps and Secrets for envFrom resolution
  - apiGroups: [""]
    resources: ["configmaps", "secrets"]
    verbs: ["get"]
  # New: read VaultSecretPolicy objects
  - apiGroups: ["policy.opstree.secret.manager"]
    resources: ["vaultsecretpolicies"]
    verbs: ["get", "list", "watch"]
  # New: update VaultSecretPolicy status
  - apiGroups: ["policy.opstree.secret.manager"]
    resources: ["vaultsecretpolicies/status"]
    verbs: ["update", "patch"]
```

---

## 8. Migration / Backward Compatibility

- The policy controller is optional: if `ENABLE_POLICY_ENFORCEMENT=false` (env var), the webhook skips the policy check entirely — existing behavior preserved.
- CRD installation is a separate Helm chart toggle: `policyController.enabled: false` by default.
- Existing pods that were running before CRD installation are not restarted.
