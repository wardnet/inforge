// Command inforge is the entrypoint for the inforge CLI.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is the build version, overridden at release time via -ldflags
// "-X main.version=<tag>". It defaults to "dev" for local builds.
var version = "dev"

// defaultResourcesDir is the root --dir default: the resources tree every command
// (and program.Run, via the `dir` stack config key) reads when none is given.
const defaultResourcesDir = "./resources"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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
	root.PersistentFlags().StringVarP(&dir, "dir", "d", defaultResourcesDir, "resources directory")
	root.PersistentFlags().StringVarP(&cfg, "config", "c", "./inforge.yaml", "project config file")

	// Nudge about newer releases after any command except update itself.
	root.PersistentPostRun = func(cmd *cobra.Command, _ []string) {
		if cmd.Name() != "update" {
			maybeNudgeUpdate()
		}
	}

	root.AddCommand(
		newValidateCmd(&dir, &cfg),
		newVersionCmd(),
		newUpdateCmd(),
		newMatrixCmd(),
		newPluginsCmd(),
		newPreviewCmd(&cfg, &dir),
		newDeployCmd(&cfg, &dir),
		newStackCmd(&cfg),
		newReleasesCmd(&cfg, &dir),
		newReleaseCmd(&cfg),
		newSecretCmd(&dir),
		newPkiCmd(&dir),
		newServiceCmd(&cfg),
		newEphemeralCmd(&cfg, &dir),
		newDBCmd(&cfg),
	)

	return root
}
