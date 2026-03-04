package vault

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestVaultProvider_Enabled(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name:        "true annotation",
			annotations: map[string]string{AnnotationVaultEnabled: "true"},
			want:        true,
		},
		{
			name:        "false annotation",
			annotations: map[string]string{AnnotationVaultEnabled: "false"},
			want:        false,
		},
		{
			name:        "missing annotation",
			annotations: map[string]string{"other-annotation": "true"},
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := FromAnnotations(tt.annotations)
			if p.Enabled() != tt.want {
				t.Errorf("VaultProvider.Enabled() = %v, want %v", p.Enabled(), tt.want)
			}
		})
	}
}

func TestVaultProvider_Validate(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantErr     bool
	}{
		{
			name: "valid minimal configuration",
			annotations: map[string]string{
				AnnotationVaultService:    "http://vault:8200",
				AnnotationVaultSecretPath: "secret/data/my-secret",
				AnnotationVaultRole:       "my-role",
			},
			wantErr: false,
		},
		{
			name: "valid minimal with multi-secret",
			annotations: map[string]string{
				AnnotationVaultService:                 "http://vault:8200",
				AnnotationVaultMultiSecretPrefix + "1": "secret/data/one",
				AnnotationVaultRole:                    "my-role",
			},
			wantErr: false,
		},
		{
			name: "missing service",
			annotations: map[string]string{
				AnnotationVaultSecretPath: "secret/data/my-secret",
				AnnotationVaultRole:       "my-role",
			},
			wantErr: true,
		},
		{
			name: "missing path and multi-secret",
			annotations: map[string]string{
				AnnotationVaultService: "http://vault:8200",
				AnnotationVaultRole:    "my-role",
			},
			wantErr: true,
		},
		{
			name: "missing role",
			annotations: map[string]string{
				AnnotationVaultService:    "http://vault:8200",
				AnnotationVaultSecretPath: "secret/data/my-secret",
			},
			wantErr: true,
		},
		{
			name: "tls setup missing ca cert",
			annotations: map[string]string{
				AnnotationVaultService:    "https://vault:8200",
				AnnotationVaultSecretPath: "secret/data/my-secret",
				AnnotationVaultRole:       "my-role",
				AnnotationVaultTLSSecret:  "vault-tls",
			},
			wantErr: true, // Requires CA cert when TLS secret is provided
		},
		{
			name: "valid tls setup",
			annotations: map[string]string{
				AnnotationVaultService:    "https://vault:8200",
				AnnotationVaultSecretPath: "secret/data/my-secret",
				AnnotationVaultRole:       "my-role",
				AnnotationVaultTLSSecret:  "vault-tls",
				AnnotationVaultCACert:     "ca.crt",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := FromAnnotations(tt.annotations)
			ctx := context.Background()

			// Just a dummy check to verify GetCurrentVersion coverage
			_, _ = p.GetCurrentVersion(ctx, tt.annotations)

			err := p.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("VaultProvider.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestVaultProvider_MutateContainer(t *testing.T) {
	annotations := map[string]string{
		AnnotationVaultService:    "http://vault:8200",
		AnnotationVaultSecretPath: "secret/data/my-secret",
		AnnotationVaultRole:       "my-role",
	}

	p := FromAnnotations(annotations)

	container := corev1.Container{
		Name:    "app",
		Command: []string{"/bin/sh"},
		Args:    []string{"-c", "echo hello"},
	}

	mutated := p.MutateContainer(container)

	expectedArgsCount := 6 // vault, --role=my-role, --path=secret/data/my-secret, --, -c, echo hello
	if len(mutated.Args) != expectedArgsCount {
		t.Errorf("Expected %d args, got %d", expectedArgsCount, len(mutated.Args))
	}

	if mutated.Args[0] != "vault" {
		t.Errorf("Expected first arg to be 'vault', got %s", mutated.Args[0])
	}

	// Ensure original args are preserved at the end
	if mutated.Args[len(mutated.Args)-2] != "-c" || mutated.Args[len(mutated.Args)-1] != "echo hello" {
		t.Errorf("Original container args were not preserved correctly")
	}

	// Verify env vars
	envFound := false
	for _, env := range mutated.Env {
		if env.Name == "VAULT_SKIP_VERIFY" && env.Value == "true" {
			envFound = true
			break
		}
	}
	if !envFound {
		t.Errorf("Expected VAULT_SKIP_VERIFY=true environment variable in non-TLS setup")
	}
}
