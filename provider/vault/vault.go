package vault

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s-vault-webhook/provider"

	corev1 "k8s.io/api/core/v1"
)

const (
	AnnotationVaultEnabled              = "vault.opstree.secret.manager/enabled"
	AnnotationVaultService              = "vault.opstree.secret.manager/service"
	AnnotationVaultAuthPath             = "vault.opstree.secret.manager/auth-path"
	AnnotationVaultSecretPath           = "vault.opstree.secret.manager/path"
	AnnotationVaultRole                 = "vault.opstree.secret.manager/role"
	AnnotationVaultTLSSecret            = "vault.opstree.secret.manager/tls-secret"
	AnnotationVaultCACert               = "vault.opstree.secret.manager/ca-cert"
	AnnotationVaultK8sTokenPath         = "vault.opstree.secret.manager/k8s-token-path"
	AnnotationVaultUseSecretNamesAsKeys = "vault.opstree.secret.manager/use-secret-names-as-keys"
	AnnotationVaultSecretVersion        = "vault.opstree.secret.manager/secret-version"
	AnnotationVaultMultiSecretPrefix    = "vault.opstree.secret.manager/secret-config-"

	VaultTLSMountPath  = "/etc/tls/"
	VaultTLSVolumeName = "vault-tls"
)

// VaultProvider implements provider.SecretManager for HashiCorp Vault.
type VaultProvider struct {
	config VaultConfig
}

// VaultConfig holds configuration parsed from pod annotations.
type VaultConfig struct {
	Enabled              bool
	Addr                 string
	TLSSecretName        string
	VaultCACert          string
	Path                 string
	Role                 string
	TokenPath            string
	AuthPath             string
	Backend              string
	KubernetesBackend    string
	UseSecretNamesAsKeys bool
	Version              string
	SecretConfigs        []string
}

// Compile-time interface check.
var _ provider.SecretManager = (*VaultProvider)(nil)

// FromAnnotations constructs a VaultProvider from pod annotations.
func FromAnnotations(annotations map[string]string) provider.SecretManager {
	enabled, _ := strconv.ParseBool(annotations[AnnotationVaultEnabled])

	secretConfigs := []string{}
	keys := filterAndSortSecretConfigs(annotations, AnnotationVaultMultiSecretPrefix)
	for _, k := range keys {
		secretConfigs = append(secretConfigs, annotations[k])
	}

	return &VaultProvider{
		config: VaultConfig{
			Enabled:              enabled,
			Addr:                 annotations[AnnotationVaultService],
			Path:                 annotations[AnnotationVaultSecretPath],
			Role:                 annotations[AnnotationVaultRole],
			TLSSecretName:        annotations[AnnotationVaultTLSSecret],
			VaultCACert:          annotations[AnnotationVaultCACert],
			TokenPath:            annotations[AnnotationVaultK8sTokenPath],
			AuthPath:             annotations[AnnotationVaultAuthPath],
			Backend:              annotations[AnnotationVaultAuthPath],
			KubernetesBackend:    annotations[AnnotationVaultAuthPath],
			UseSecretNamesAsKeys: func() bool { v, _ := strconv.ParseBool(annotations[AnnotationVaultUseSecretNamesAsKeys]); return v }(),
			Version:              annotations[AnnotationVaultSecretVersion],
			SecretConfigs:        secretConfigs,
		},
	}
}

func (p *VaultProvider) Name() string { return "vault" }

func (p *VaultProvider) Enabled() bool { return p.config.Enabled }

func (p *VaultProvider) Validate() error {
	if p.config.Addr == "" {
		return fmt.Errorf("vault provider: service address is required (annotation: %s)", AnnotationVaultService)
	}
	if p.config.Path == "" && len(p.config.SecretConfigs) == 0 {
		return fmt.Errorf("vault provider: secret path or secret-config annotations required (annotation: %s or %s)", AnnotationVaultSecretPath, AnnotationVaultMultiSecretPrefix)
	}
	if p.config.Role == "" {
		return fmt.Errorf("vault provider: role is required (annotation: %s)", AnnotationVaultRole)
	}
	if p.config.TLSSecretName != "" && p.config.VaultCACert == "" {
		return fmt.Errorf("vault provider: CA cert filename required when TLS secret is set (annotation: %s)", AnnotationVaultCACert)
	}
	return nil
}

func (p *VaultProvider) MutateContainer(container corev1.Container) corev1.Container {
	// Set env vars
	container.Env = append(container.Env, corev1.EnvVar{
		Name:  "VAULT_ADDR",
		Value: p.config.Addr,
	})

	// Handle TLS
	if p.config.TLSSecretName != "" {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "VAULT_CACERT",
			Value: fmt.Sprintf("%s%s", VaultTLSMountPath, p.config.VaultCACert),
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      VaultTLSVolumeName,
			MountPath: VaultTLSMountPath,
		})
	} else {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "VAULT_SKIP_VERIFY",
			Value: "true",
		})
	}

	// Set args
	args := []string{"vault"}
	args = append(args, fmt.Sprintf("--role=%s", p.config.Role))

	if p.config.KubernetesBackend != "" {
		args = append(args, fmt.Sprintf("--kubernetes-backend=%s", p.config.KubernetesBackend))
	}
	if p.config.TokenPath != "" {
		args = append(args, fmt.Sprintf("--token-path=%s", p.config.TokenPath))
	}
	for _, s := range p.config.SecretConfigs {
		args = append(args, fmt.Sprintf("--secret-config=%s", s))
	}
	if p.config.Path != "" {
		args = append(args, fmt.Sprintf("--path=%s", p.config.Path))
	}
	if p.config.UseSecretNamesAsKeys {
		args = append(args, "--names-as-keys")
	}
	if p.config.Version != "" {
		args = append(args, fmt.Sprintf("--version=%s", p.config.Version))
	}

	args = append(args, "--")
	args = append(args, container.Args...)
	container.Args = args

	return container
}

func (p *VaultProvider) ExtraVolumes() []corev1.Volume {
	if p.config.TLSSecretName != "" {
		return []corev1.Volume{
			{
				Name: VaultTLSVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: p.config.TLSSecretName,
					},
				},
			},
		}
	}
	return nil
}

// filterAndSortSecretConfigs filters annotations by prefix and sorts them numerically.
func filterAndSortSecretConfigs(m map[string]string, delimiter string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.HasPrefix(k, delimiter) {
			keys = append(keys, k)
		}
	}
	if len(keys) > 0 {
		sort.Slice(keys, func(i, j int) bool {
			numA, _ := strconv.Atoi(strings.Split(keys[i], delimiter)[1])
			numB, _ := strconv.Atoi(strings.SplitAfter(keys[j], delimiter)[1])
			return numA < numB
		})
	}
	return keys
}
