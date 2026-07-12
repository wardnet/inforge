package schemas

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"testing"
)

// TestEmbeddedSchemasAreStrictJSON asserts every embedded *.json schema is
// strictly valid JSON — no trailing tokens. The jsonschema compiler inforge
// uses tolerates trailing garbage (it decodes the first value and ignores the
// rest), so a stray brace slipped past compileSchemas() silently; a stricter
// consumer (jq, a jsonschema major bump, a CI lint) would then break
// validation for EVERY project. json.Valid rejects trailing data, so this
// pins the invariant at the source.
func TestEmbeddedSchemasAreStrictJSON(t *testing.T) {
	entries, err := fs.Glob(FS, "*.json")
	if err != nil {
		t.Fatalf("glob embedded schemas: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no embedded *.json schemas found")
	}
	for _, name := range entries {
		t.Run(filepath.Base(name), func(t *testing.T) {
			b, err := FS.ReadFile(name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			if !json.Valid(b) {
				t.Errorf("%s is not strictly valid JSON (trailing tokens or malformed)", name)
			}
		})
	}
}
