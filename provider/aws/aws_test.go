package aws

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestAWSProvider_Enabled(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name:        "true annotation",
			annotations: map[string]string{AnnotationAWSSecretManagerEnabled: "true"},
			want:        true,
		},
		{
			name:        "false annotation",
			annotations: map[string]string{AnnotationAWSSecretManagerEnabled: "false"},
			want:        false,
		},
		{
			name:        "missing annotation",
			annotations: map[string]string{"other": "true"},
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := FromAnnotations(tt.annotations)
			if p.Enabled() != tt.want {
				t.Errorf("AWSProvider.Enabled() = %v, want %v", p.Enabled(), tt.want)
			}
		})
	}
}

func TestAWSProvider_Validate(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantErr     bool
	}{
		{
			name: "valid configuration",
			annotations: map[string]string{
				AnnotationAWSSecretManagerRegion:     "us-east-1",
				AnnotationAWSSecretManagerSecretName: "my-aws-secret",
			},
			wantErr: false,
		},
		{
			name: "missing secret name",
			annotations: map[string]string{
				AnnotationAWSSecretManagerRegion: "us-east-1",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := FromAnnotations(tt.annotations)
			ctx := context.Background()

			// Touch version method for coverage
			_, _ = p.GetCurrentVersion(ctx, tt.annotations)

			err := p.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("AWSProvider.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAWSProvider_MutateContainer(t *testing.T) {
	annotations := map[string]string{
		AnnotationAWSSecretManagerRegion:     "us-east-1",
		AnnotationAWSSecretManagerSecretName: "db-credentials",
		AnnotationAWSSecretManagerRoleARN:    "arn:aws:iam::123:role/read-secrets",
	}

	p := FromAnnotations(annotations)

	container := corev1.Container{
		Name:    "app",
		Command: []string{"node"},
		Args:    []string{"server.js"},
	}

	mutated := p.MutateContainer(container)

	expectedArgsCount := 6 // aws, --region=us-east-1, --secret-name=db-credentials, --role-arn=..., --, server.js
	if len(mutated.Args) != expectedArgsCount {
		t.Errorf("Expected %d args, got %d. Args: %v", expectedArgsCount, len(mutated.Args), mutated.Args)
	}

	if mutated.Args[0] != "aws" {
		t.Errorf("Expected first arg to be 'aws', got %s", mutated.Args[0])
	}
}
