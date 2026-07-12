package output

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarkdownCounts(t *testing.T) {
	md := Markdown("inforge deploy — prd", map[string]int{"create": 14, "update": 2}, nil)
	assert.Contains(t, md, "## inforge deploy — prd")
	assert.Contains(t, md, "| 14 | 2 | 0 | 0 | 0 |")
	assert.NotContains(t, md, "### Failed")
}

// TestMarkdownCountsReplacements: a replacement-only run must not report
// all-zeroes, in BOTH shapes the counts can arrive in — the per-resource event
// stream (every physical step: create-replacement + replace + delete-replaced)
// and the SummaryEvent fallback (logical steps only, i.e. `replace` alone,
// which is what a preview that emits no ResOutputsEvents leaves us with). Three
// replaced resources must read as 3 replaced — not as 3 deleted on top, and not
// as no changes at all.
func TestMarkdownCountsReplacements(t *testing.T) {
	for name, changes := range map[string]map[string]int{
		"event stream (physical steps)": {"create-replacement": 3, "replace": 3, "delete-replaced": 3},
		"summary fallback (logical)":    {"replace": 3},
	} {
		t.Run(name, func(t *testing.T) {
			md := Markdown("inforge deploy — prd", changes, nil)
			assert.Contains(t, md, "| 0 | 0 | 0 | 3 | 0 |",
				"3 replaced resources are 3 replacements — not deletions, not nothing")
		})
	}
}

// TestMarkdownDeletedExcludesReplacements: the 🗑️ deleted column counts only
// resources that actually go away. A run that deletes one resource and replaces
// three must not read as four deletions.
func TestMarkdownDeletedExcludesReplacements(t *testing.T) {
	md := Markdown("inforge deploy — prd",
		map[string]int{"delete": 1, "create-replacement": 3, "replace": 3, "delete-replaced": 3},
		nil,
	)
	assert.Contains(t, md, "| 0 | 0 | 1 | 3 | 0 |")
}

func TestMarkdownFailures(t *testing.T) {
	md := Markdown("inforge deploy — prd",
		map[string]int{"create": 14},
		[]Failure{{Type: "infisical:InfisicalIdentity", Name: "wardnet-prd-use1-identity-bridge", Message: "HTTP 404"}},
	)
	assert.Contains(t, md, "| 14 | 0 | 0 | 0 | 1 |")
	assert.Contains(t, md, "### Failed")
	assert.Contains(t, md, "`infisical:InfisicalIdentity` wardnet-prd-use1-identity-bridge — HTTP 404")
	assert.Contains(t, md, "were skipped", "the abort note explains absence != applied")
	// A failure with no captured message still renders without a dangling dash.
	md2 := Markdown("t", nil, []Failure{{Type: "x:Y", Name: "n"}})
	assert.Contains(t, md2, "- `x:Y` n\n")
	assert.False(t, strings.Contains(md2, "n — \n"))
}
