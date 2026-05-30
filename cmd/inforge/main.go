// Command inforge is the entrypoint for the inforge CLI.
//
// This phase wires the `validate <env>` subcommand fully; `apply`/`preview`
// land with the compute provider.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/validate"
)

// version is the build version, overridden at release time via -ldflags
// "-X main.version=<tag>". It defaults to "dev" for local builds.
var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var dir string
	var config string

	root := &cobra.Command{
		Use:     "inforge",
		Short:   "inforge turns declarative infrastructure definitions into deployments",
		Version: version,
	}
	root.PersistentFlags().StringVarP(&dir, "dir", "d", "./resources", "resources directory")
	root.PersistentFlags().StringVarP(&config, "config", "c", "./inforge.yaml", "project config file")

	root.AddCommand(&cobra.Command{
		Use:   "validate <env>",
		Short: "Validate the resource definitions for an environment",
		Args:  cobra.ExactArgs(1),
		// Errors are reported per-file by validate; keep usage/errors quiet.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			env := args[0]
			if err := validate.ValidateResources(env, dir); err != nil {
				fmt.Println("\nvalidation failed")
				return err
			}
			fmt.Println("\nall resource files are valid")
			return nil
		},
	})

	return root
}
