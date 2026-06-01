package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newMatrixCmd() *cobra.Command {
	var base, head string

	cmd := &cobra.Command{
		Use:           "matrix",
		Short:         "Compute the GHA environment matrix from a git diff",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			out, err := exec.Command("git", "diff", "--name-only", base+"..."+head).Output()
			if err != nil {
				return fmt.Errorf("git diff: %w", err)
			}

			envs := map[string]bool{}
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				parts := strings.SplitN(line, "/", 3)
				if len(parts) >= 2 && parts[0] == "resources" && parts[1] != "" {
					envs[parts[1]] = true
				}
			}

			sorted := make([]string, 0, len(envs))
			for e := range envs {
				sorted = append(sorted, e)
			}
			sort.Strings(sorted)

			type entry struct {
				Environment string `json:"environment"`
				StackConfig string `json:"stack_config"`
			}
			entries := make([]entry, 0, len(sorted))
			for _, e := range sorted {
				entries = append(entries, entry{
					Environment: e,
					StackConfig: "inforge." + e + ".yaml",
				})
			}

			data, err := json.Marshal(entries)
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		},
	}

	cmd.Flags().StringVar(&base, "base", "", "base git ref (required)")
	cmd.Flags().StringVar(&head, "head", "HEAD", "head git ref")
	if err := cmd.MarkFlagRequired("base"); err != nil {
		panic(err)
	}
	return cmd
}
