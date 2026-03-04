package aws

import (
	"context"
	"fmt"
	"strconv"

	"k8s-vault-webhook/provider"

	corev1 "k8s.io/api/core/v1"
)

const (
	AnnotationAWSSecretManagerEnabled         = "aws.opstree.secret.manager/enabled"
	AnnotationAWSSecretManagerRegion          = "aws.opstree.secret.manager/region"
	AnnotationAWSSecretManagerRoleARN         = "aws.opstree.secret.manager/role-arn"
	AnnotationAWSSecretManagerSecretName      = "aws.opstree.secret.manager/secret-name"
	AnnotationAWSSecretManagerPreviousVersion = "aws.opstree.secret.manager/previous-version"
)

// AWSProvider implements provider.SecretManager for AWS Secrets Manager.
type AWSProvider struct {
	config AWSConfig
}

// AWSConfig holds configuration parsed from pod annotations.
type AWSConfig struct {
	Enabled         bool
	Region          string
	SecretName      string
	PreviousVersion string
	RoleARN         string
}

// Compile-time interface check.
var _ provider.SecretManager = (*AWSProvider)(nil)

// FromAnnotations constructs an AWSProvider from pod annotations.
func FromAnnotations(annotations map[string]string) provider.SecretManager {
	enabled, _ := strconv.ParseBool(annotations[AnnotationAWSSecretManagerEnabled])
	return &AWSProvider{
		config: AWSConfig{
			Enabled:         enabled,
			Region:          annotations[AnnotationAWSSecretManagerRegion],
			SecretName:      annotations[AnnotationAWSSecretManagerSecretName],
			PreviousVersion: annotations[AnnotationAWSSecretManagerPreviousVersion],
			RoleARN:         annotations[AnnotationAWSSecretManagerRoleARN],
		},
	}
}

func (p *AWSProvider) Name() string { return "aws" }

func (p *AWSProvider) Enabled() bool { return p.config.Enabled }

func (p *AWSProvider) Validate() error {
	if p.config.SecretName == "" {
		return fmt.Errorf("aws provider: secret name is required (annotation: %s)", AnnotationAWSSecretManagerSecretName)
	}
	return nil
}

func (p *AWSProvider) MutateContainer(container corev1.Container) corev1.Container {
	args := []string{"aws"}
	args = append(args, fmt.Sprintf("--region=%s", p.config.Region))

	if p.config.SecretName != "" {
		args = append(args, fmt.Sprintf("--secret-name=%s", p.config.SecretName))
	}
	if p.config.RoleARN != "" {
		args = append(args, fmt.Sprintf("--role-arn=%s", p.config.RoleARN))
	}
	if p.config.PreviousVersion != "" {
		args = append(args, fmt.Sprintf("--previous-version=%s", p.config.PreviousVersion))
	}

	args = append(args, "--")
	args = append(args, container.Args...)
	container.Args = args

	return container
}

func (p *AWSProvider) ExtraVolumes() []corev1.Volume {
	return nil
}

func (p *AWSProvider) GetCurrentVersion(ctx context.Context, annotations map[string]string) (string, error) {
	// TODO: Implement actual AWS Secrets Manager API hit to fetch AWSCURRENT version ID
	return "AWSCURRENT", nil
}
