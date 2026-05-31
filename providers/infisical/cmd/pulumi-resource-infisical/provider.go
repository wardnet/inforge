package main

import (
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/wardnet/inforge/providers/infisical/cmd/pulumi-resource-infisical/resources"
)

// newProvider constructs the pulumi-go-provider server that serves
// InfisicalWorkspace and InfisicalSecretsBatch resources.
func newProvider() (p.Provider, error) {
	return infer.NewProviderBuilder().
		WithResources(
			infer.Resource(&resources.InfisicalWorkspace{}),
			infer.Resource(&resources.InfisicalSecretsBatch{}),
		).
		Build()
}
