package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/validate"
)

func newValidateCmd(dir, configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "validate <env>",
		Short:         "Validate the resource definitions for an environment",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			// Provider defaults from inforge.yaml are optional; a missing or unconfigured
			// project config is treated as zero defaults (no spec-level defaults apply).
			var defaults types.ProviderDefaults
			if projCfg, err := loadProjectConfig(*configPath); err == nil {
				defaults = projCfg.Providers
			}
			if err := validate.ValidateResources(args[0], *dir, defaults); err != nil {
				return err
			}
			fmt.Println("\nall resource files are valid")
			return nil
		},
	}
}
