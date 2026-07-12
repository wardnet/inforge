package output

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

// Markdown renders a run summary as GitHub-flavoured markdown: a heading, a
// change-count table (including failures), and a per-resource failure list. The
// counts come from Printer.Changes() and the failures from Printer.Failures(),
// so deploy/preview render the same artifact they already streamed. It is
// suitable both as a standalone report file and for $GITHUB_STEP_SUMMARY.
//
// The table counts RESOURCES, one column each, and replacements need care
// because Pulumi describes them differently depending on which source the
// counts came from (see Printer.effectiveChanges):
//
//   - the per-resource event stream emits every physical step, so ONE replaced
//     resource shows up as three ops: `create-replacement`, `replace` and
//     `delete-replaced`;
//   - SummaryEvent.ResourceChanges counts only *logical* steps (Pulumi's
//     CreateStep/DeleteStep report Logical() == !replacing, ReplaceStep reports
//     true), so the same replacement arrives as `replace` ALONE.
//
// So `replaced` is the max of the two spellings — they are 1:1 whenever both are
// present, and either may be the only one there — and `delete-replaced` is NOT
// added to `deleted`: it is the tear-down half of a replacement, already counted
// in `replaced`, and folding it in would report N replacements as N deletions on
// top, scaring a PR reader with destruction that isn't happening. `deleted` is
// therefore exactly the resources that go away.
//
// Keying off any single op spelling is what produced the original bug: a
// replacement-only run rendered as all-zeroes.
func Markdown(title string, changes map[string]int, failures []Failure) string {
	created := changes[string(apitype.OpCreate)]
	updated := changes[string(apitype.OpUpdate)]
	deleted := changes[string(apitype.OpDelete)]
	replaced := max(
		changes[string(apitype.OpReplace)],
		changes[string(apitype.OpCreateReplacement)],
	)

	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", title)
	fmt.Fprintln(&b, "| ➕ created | ✏️ updated | 🗑️ deleted | ♻️ replaced | ❌ failed |")
	fmt.Fprintln(&b, "|---|---|---|---|---|")
	fmt.Fprintf(&b, "| %d | %d | %d | %d | %d |\n",
		created, updated, deleted, replaced, len(failures))

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
