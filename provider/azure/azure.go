package azure

import (
	"fmt"
	"strconv"

	"k8s-vault-webhook/provider"

	corev1 "k8s.io/api/core/v1"
)

const (
	AnnotationAzureKeyVaultEnabled = "azure.opstree.secret.manager/enabled"
	AnnotationAzureKeyVaultName    = "azure.opstree.secret.manager/vault-name"
)

// AzureProvider implements provider.SecretManager for Azure Key Vault.
type AzureProvider struct {
	config AzureConfig
}

// AzureConfig holds configuration parsed from pod annotations.
type AzureConfig struct {
	Enabled           bool
	AzureKeyVaultName string
}

// Compile-time interface check.
var _ provider.SecretManager = (*AzureProvider)(nil)

// FromAnnotations constructs an AzureProvider from pod annotations.
func FromAnnotations(annotations map[string]string) provider.SecretManager {
	enabled, _ := strconv.ParseBool(annotations[AnnotationAzureKeyVaultEnabled])
	return &AzureProvider{
		config: AzureConfig{
			Enabled:           enabled,
			AzureKeyVaultName: annotations[AnnotationAzureKeyVaultName],
		},
	}
}

func (p *AzureProvider) Name() string { return "azure" }

func (p *AzureProvider) Enabled() bool { return p.config.Enabled }

func (p *AzureProvider) Validate() error {
	if p.config.AzureKeyVaultName == "" {
		return fmt.Errorf("azure provider: key vault name is required (annotation: %s)", AnnotationAzureKeyVaultName)
	}
	return nil
}

func (p *AzureProvider) MutateContainer(container corev1.Container) corev1.Container {
	args := []string{"azure"}
	args = append(args, fmt.Sprintf("--azure-vault-name=%s", p.config.AzureKeyVaultName))

	args = append(args, "--")
	args = append(args, container.Args...)
	container.Args = args

	return container
}

func (p *AzureProvider) ExtraVolumes() []corev1.Volume {
	return nil
}
