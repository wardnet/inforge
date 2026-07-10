package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wardnet/inforge/internal/output"
)

// writeReport renders the run's markdown report and writes it to reportPath
// (defaulting to a temp file when empty), prints that path to stderr, and — when
// running under a CI that sets $GITHUB_STEP_SUMMARY — appends it to the job
// summary. The report is always produced, on success or failure, so CI can
// surface it however it likes (e.g. `gh pr comment --body-file <path>`).
//
// The CLI deliberately stops at "produce a file + optional step summary": it
// never calls the GitHub API. Posting a PR comment stays in the consumer's
// workflow, so the binary carries no platform coupling.
func writeReport(command, stackName string, p *output.Printer, reportPath string) {
	md := output.Markdown(fmt.Sprintf("inforge %s — %s", command, stackName), p.Changes(), p.Failures())

	if reportPath == "" {
		reportPath = filepath.Join(os.TempDir(), fmt.Sprintf("inforge-%s-%s.md", command, stackName))
	}
	// reportPath defaults to a fixed os.TempDir() name and is otherwise the
	// operator-supplied --report flag; 0644 is intentional, this file is meant to
	// be read by other tools/steps afterward (see doc comment above).
	if err := os.WriteFile(reportPath, []byte(md), 0o644); err != nil { // #nosec G306 -- 0644 is intentional; this file is meant to be read by other tools/steps afterward
		_, _ = fmt.Fprintf(os.Stderr, "warning: could not write report to %s: %v\n", reportPath, err)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "report: %s\n", reportPath)

	if summary := os.Getenv("GITHUB_STEP_SUMMARY"); summary != "" {
		// #nosec G304,G703,G302 -- summary path is set by the trusted CI runner via
		// $GITHUB_STEP_SUMMARY, not attacker input; 0644 matches other steps appending
		// to the same shared summary file.
		f, err := os.OpenFile(summary, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		_, _ = f.WriteString(md + "\n")
	}
}
