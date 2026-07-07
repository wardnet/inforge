package main

import (
	"errors"
	"fmt"
	"os"

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
			// Provider defaults from inforge.yaml are optional: a missing file means
			// no defaults (zero). A file that exists but fails to parse is a real
			// error — propagate it so validate gives the same signal as deploy.
			var defaults types.ProviderDefaults
			var opts []validate.ValidateOption
			if _, err := os.Stat(*configPath); err == nil {
				projCfg, err := loadProjectConfig(*configPath)
				if err != nil {
					return err
				}
				defaults = projCfg.Providers
				opts = append(opts, validate.WithBackupsBucket(projCfg.Backups.configured()))
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("stat %s: %w", *configPath, err)
			}
			if err := validate.ValidateResources(args[0], *dir, defaults, opts...); err != nil {
				return err
			}
			fmt.Println("\nall resource files are valid")
			return nil
		},
	}
}
