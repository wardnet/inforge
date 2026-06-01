// Command inforge is the entrypoint for the inforge CLI.
package main

import (
	"os"

	"github.com/spf13/cobra"
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
	var cfg string

	root := &cobra.Command{
		Use:   "inforge",
		Short: "inforge turns declarative infrastructure definitions into deployments",
	}
	root.PersistentFlags().StringVarP(&dir, "dir", "d", "./resources", "resources directory")
	root.PersistentFlags().StringVarP(&cfg, "config", "c", "./inforge.yaml", "project config file")

	root.AddCommand(
		newValidateCmd(&dir),
		newVersionCmd(),
		newMatrixCmd(),
		newPluginsCmd(),
		newPreviewCmd(&cfg),
		newDeployCmd(&cfg),
	)

	return root
}
