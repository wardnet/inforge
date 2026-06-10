package output

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarkdownCounts(t *testing.T) {
	md := Markdown("inforge deploy — prd", map[string]int{"create": 14, "update": 2}, nil)
	assert.Contains(t, md, "## inforge deploy — prd")
	assert.Contains(t, md, "| 14 | 2 | 0 | 0 |")
	assert.NotContains(t, md, "### Failed")
}

func TestMarkdownFailures(t *testing.T) {
	md := Markdown("inforge deploy — prd",
		map[string]int{"create": 14},
		[]Failure{{Type: "infisical:InfisicalIdentity", Name: "wardnet-prd-use1-identity-bridge", Message: "HTTP 404"}},
	)
	assert.Contains(t, md, "| 14 | 0 | 0 | 1 |")
	assert.Contains(t, md, "### Failed")
	assert.Contains(t, md, "`infisical:InfisicalIdentity` wardnet-prd-use1-identity-bridge — HTTP 404")
	assert.Contains(t, md, "were skipped", "the abort note explains absence != applied")
	// A failure with no captured message still renders without a dangling dash.
	md2 := Markdown("t", nil, []Failure{{Type: "x:Y", Name: "n"}})
	assert.Contains(t, md2, "- `x:Y` n\n")
	assert.False(t, strings.Contains(md2, "n — \n"))
}
