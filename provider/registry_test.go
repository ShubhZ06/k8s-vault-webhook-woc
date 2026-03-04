package provider

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// mockProvider is a helper to test the Registry builder
type mockProvider struct {
	enabled bool
	name    string
}

func (m *mockProvider) Name() string                                        { return m.name }
func (m *mockProvider) Enabled() bool                                       { return m.enabled }
func (m *mockProvider) Validate() error                                     { return nil }
func (m *mockProvider) MutateContainer(c corev1.Container) corev1.Container { return c }
func (m *mockProvider) ExtraVolumes() []corev1.Volume                       { return nil }
func (m *mockProvider) GetCurrentVersion(ctx context.Context, annotations map[string]string) (string, error) {
	return "", nil
}

func TestRegistry_BuildEnabledProviders(t *testing.T) {
	// Setup a registry with dummy factories
	r := NewRegistry(
		func(a map[string]string) SecretManager {
			return &mockProvider{name: "prov1", enabled: a["prov1"] == "true"}
		},
		func(a map[string]string) SecretManager {
			return &mockProvider{name: "prov2", enabled: a["prov2"] == "true"}
		},
	)

	tests := []struct {
		name        string
		annotations map[string]string
		wantCount   int
		wantNames   []string
	}{
		{
			name:        "none enabled",
			annotations: map[string]string{"prov1": "false", "prov2": "false"},
			wantCount:   0,
			wantNames:   nil,
		},
		{
			name:        "one enabled",
			annotations: map[string]string{"prov1": "true", "prov2": "false"},
			wantCount:   1,
			wantNames:   []string{"prov1"},
		},
		{
			name:        "both enabled",
			annotations: map[string]string{"prov1": "true", "prov2": "true"},
			wantCount:   2,
			wantNames:   []string{"prov1", "prov2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.BuildEnabledProviders(tt.annotations)
			if len(got) != tt.wantCount {
				t.Errorf("BuildEnabledProviders() returned %v providers, want %v", len(got), tt.wantCount)
			}

			for i, p := range got {
				if p.Name() != tt.wantNames[i] {
					t.Errorf("Provider %d name = %s, want %s", i, p.Name(), tt.wantNames[i])
				}
			}
		})
	}
}
