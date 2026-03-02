package main

import (
	"k8s-vault-webhook/provider"
	awsprovider "k8s-vault-webhook/provider/aws"
	azureprovider "k8s-vault-webhook/provider/azure"
	gcpprovider "k8s-vault-webhook/provider/gcp"
	vaultprovider "k8s-vault-webhook/provider/vault"
	"k8s-vault-webhook/registry"

	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

type mutatingWebhook struct {
	k8sClient        kubernetes.Interface
	registry         registry.ImageRegistry
	logger           log.FieldLogger
	providerRegistry *provider.Registry
}

// newProviderRegistry creates a provider registry with all known provider factories.
func newProviderRegistry() *provider.Registry {
	return provider.NewRegistry(
		vaultprovider.FromAnnotations,
		awsprovider.FromAnnotations,
		azureprovider.FromAnnotations,
		gcpprovider.FromAnnotations,
	)
}
