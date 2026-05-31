// Command pulumi-resource-infisical is the Pulumi resource provider plugin for
// Infisical. It is installed automatically by `inforge plugins install` and is
// never invoked directly by users.
package main

import (
	"context"
	"fmt"
	"os"

	p "github.com/pulumi/pulumi-go-provider"
)

// version is overridden at release time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	prov, err := newProvider()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := p.RunProvider(context.Background(), "infisical", version, prov); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
