package main

import (
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/wardnet/inforge/providers/neon/cmd/pulumi-resource-neon/resources"
)

// newProvider constructs the pulumi-go-provider server that serves NeonProject
// and NeonDatabase resources.
func newProvider() (p.Provider, error) {
	return infer.NewProviderBuilder().
		WithResources(
			infer.Resource(&resources.NeonProject{}),
			infer.Resource(&resources.NeonDatabase{}),
		).
		Build()
}
