package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/validate"
)

func newValidateCmd(dir *string) *cobra.Command {
	return &cobra.Command{
		Use:           "validate <env>",
		Short:         "Validate the resource definitions for an environment",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := validate.ValidateResources(args[0], *dir); err != nil {
				fmt.Println("\nvalidation failed")
				return err
			}
			fmt.Println("\nall resource files are valid")
			return nil
		},
	}
}
