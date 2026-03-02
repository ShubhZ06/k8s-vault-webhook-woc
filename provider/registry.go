package provider

// ProviderFactory is a function that builds a SecretManager from pod annotations.
type ProviderFactory func(annotations map[string]string) SecretManager

// Registry holds the list of registered provider factories.
type Registry struct {
	factories []ProviderFactory
}

// NewRegistry creates a new provider registry with the given factories.
func NewRegistry(factories ...ProviderFactory) *Registry {
	return &Registry{factories: factories}
}

// BuildEnabledProviders iterates through all registered factories and returns
// only the providers that are enabled according to the pod's annotations.
func (r *Registry) BuildEnabledProviders(annotations map[string]string) []SecretManager {
	var enabled []SecretManager
	for _, factory := range r.factories {
		p := factory(annotations)
		if p.Enabled() {
			enabled = append(enabled, p)
		}
	}
	return enabled
}
