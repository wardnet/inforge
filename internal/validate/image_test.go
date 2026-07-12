package validate

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/schemas"
)

// computeImageEnum reads the accepted canonical images straight out of
// schemas/compute.json — the single source of truth for the set (the retired
// internal/images package duplicated it while nothing imported it; the Hetzner
// provider holds no static image map either, it resolves each canonical image
// through the region's `images:` config and errors when one is unmapped, see
// providers/hetzner: "no image for").
func computeImageEnum(t *testing.T) []string {
	t.Helper()
	b, err := schemas.FS.ReadFile("compute.json")
	require.NoError(t, err)
	var doc struct {
		Properties struct {
			Image struct {
				Enum []string `json:"enum"`
			} `json:"image"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(b, &doc))
	require.NotEmpty(t, doc.Properties.Image.Enum, "compute.json must enumerate the canonical images")
	return doc.Properties.Image.Enum
}

// TestComputeImageEnumIsEnforced: every image the schema enumerates is accepted
// and anything else is rejected, so the enum is the real gate on the value that
// reaches the provider's per-region image lookup.
func TestComputeImageEnumIsEnforced(t *testing.T) {
	schemaSet, err := compileSchemas()
	require.NoError(t, err)
	compute := schemaSet["compute"]
	require.NotNil(t, compute)

	spec := func(image string) map[string]any {
		return map[string]any{
			"name":      "edge",
			"container": "edge",
			"network":   "ingress",
			"size":      "SMALL",
			"image":     image,
		}
	}

	for _, img := range computeImageEnum(t) {
		assert.Empty(t, schemaErrors(compute, spec(img)), "%s is a canonical image and must validate", img)
	}
	assert.NotEmpty(t, schemaErrors(compute, spec("windows-2022")), "a non-canonical image must be rejected")
	assert.NotEmpty(t, schemaErrors(compute, spec("")), "an empty image must be rejected")
}
