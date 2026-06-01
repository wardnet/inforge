package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the inforge version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("inforge v%s\n", version)
		},
	}
}
