package output

import (
	"fmt"
	"strings"
)

// Markdown renders a run summary as GitHub-flavoured markdown: a heading, a
// change-count table (including failures), and a per-resource failure list. The
// counts come from Printer.Changes() and the failures from Printer.Failures(),
// so deploy/preview render the same artifact they already streamed. It is
// suitable both as a standalone report file and for $GITHUB_STEP_SUMMARY.
func Markdown(title string, changes map[string]int, failures []Failure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", title)
	fmt.Fprintln(&b, "| ➕ created | ✏️ updated | 🗑️ deleted | ❌ failed |")
	fmt.Fprintln(&b, "|---|---|---|---|")
	fmt.Fprintf(&b, "| %d | %d | %d | %d |\n",
		changes["create"], changes["update"], changes["delete"], len(failures))

	if len(failures) > 0 {
		fmt.Fprint(&b, "\n### Failed\n\n")
		for _, f := range failures {
			line := fmt.Sprintf("- `%s` %s", f.Type, f.Name)
			if f.Message != "" {
				line += " — " + f.Message
			}
			fmt.Fprintln(&b, line)
		}
		fmt.Fprint(&b, "\n> Resources depending on the failures above were skipped "+
			"(not created or updated). A resource missing from the counts was not "+
			"necessarily applied — fix the errors and re-run.\n")
	}
	return b.String()
}
