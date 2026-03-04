package gcp

import (
	"context"
	"fmt"
	"strconv"

	"k8s-vault-webhook/provider"

	corev1 "k8s.io/api/core/v1"
)

const (
	AnnotationGCPSecretManagerEnabled                        = "gcp.opstree.secret.manager/enabled"
	AnnotationGCPSecretManagerProjectID                      = "gcp.opstree.secret.manager/project-id"
	AnnotationGCPSecretManagerSecretName                     = "gcp.opstree.secret.manager/secret-name"
	AnnotationGCPSecretManagerSecretVersion                  = "gcp.opstree.secret.manager/secret-version"
	AnnotationGCPSecretManagerGCPServiceAccountKeySecretName = "gcp.opstree.secret.manager/gcp-service-account-key-secret-name"

	VolumeMountGoogleCloudKeyPath        = "/var/run/secret/cloud.google.com"
	VolumeMountGoogleCloudKeyName        = "google-cloud-key"
	GCPServiceAccountCredentialsFileName = "service-account.json"
)

// GCPProvider implements provider.SecretManager for GCP Secret Manager.
type GCPProvider struct {
	config GCPConfig
}

// GCPConfig holds configuration parsed from pod annotations.
type GCPConfig struct {
	Enabled                     bool
	ProjectID                   string
	SecretName                  string
	SecretVersion               string
	ServiceAccountKeySecretName string
}

// Compile-time interface check.
var _ provider.SecretManager = (*GCPProvider)(nil)

// FromAnnotations constructs a GCPProvider from pod annotations.
func FromAnnotations(annotations map[string]string) provider.SecretManager {
	enabled, _ := strconv.ParseBool(annotations[AnnotationGCPSecretManagerEnabled])
	return &GCPProvider{
		config: GCPConfig{
			Enabled:                     enabled,
			ProjectID:                   annotations[AnnotationGCPSecretManagerProjectID],
			SecretName:                  annotations[AnnotationGCPSecretManagerSecretName],
			SecretVersion:               annotations[AnnotationGCPSecretManagerSecretVersion],
			ServiceAccountKeySecretName: annotations[AnnotationGCPSecretManagerGCPServiceAccountKeySecretName],
		},
	}
}

func (p *GCPProvider) Name() string { return "gcp" }

func (p *GCPProvider) Enabled() bool { return p.config.Enabled }

func (p *GCPProvider) Validate() error {
	if p.config.ProjectID == "" {
		return fmt.Errorf("gcp provider: project ID is required (annotation: %s)", AnnotationGCPSecretManagerProjectID)
	}
	if p.config.SecretName == "" {
		return fmt.Errorf("gcp provider: secret name is required (annotation: %s)", AnnotationGCPSecretManagerSecretName)
	}
	return nil
}

func (p *GCPProvider) MutateContainer(container corev1.Container) corev1.Container {
	// Mount google service account key if given
	if p.config.ServiceAccountKeySecretName != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      VolumeMountGoogleCloudKeyName,
			MountPath: VolumeMountGoogleCloudKeyPath,
		})
	}

	// Set args
	args := []string{"gcp"}
	args = append(args, fmt.Sprintf("--project-id=%s", p.config.ProjectID))

	if p.config.SecretName != "" {
		args = append(args, fmt.Sprintf("--secret-name=%s", p.config.SecretName))
	}
	if p.config.SecretVersion != "" {
		args = append(args, fmt.Sprintf("--secret-version=%s", p.config.SecretVersion))
	}
	if p.config.ServiceAccountKeySecretName != "" {
		args = append(args, fmt.Sprintf("--google-application-credentials=%s", fmt.Sprintf("%s/%s", VolumeMountGoogleCloudKeyPath, GCPServiceAccountCredentialsFileName)))
	}

	args = append(args, "--")
	args = append(args, container.Args...)
	container.Args = args

	return container
}

func (p *GCPProvider) ExtraVolumes() []corev1.Volume {
	if p.config.ServiceAccountKeySecretName != "" {
		return []corev1.Volume{
			{
				Name: VolumeMountGoogleCloudKeyName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: p.config.ServiceAccountKeySecretName,
					},
				},
			},
		}
	}
	return nil
}

func (p *GCPProvider) GetCurrentVersion(ctx context.Context, annotations map[string]string) (string, error) {
	// TODO: Implement actual GCP Secret Manager API hit to fetch version
	return "1", nil
}
