package yamldoc_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// decodeCall matches a direct YAML decode: yaml.Unmarshal(...) or yaml.NewDecoder(...).
// yaml.Marshal is deliberately NOT matched — WRITING a file is not reading one, and the
// stores, descriptors and manifests inforge writes are free to marshal.
var decodeCall = regexp.MustCompile(`yaml\.Unmarshal\(|yaml\.NewDecoder\(`)

// TestYamldocIsTheOnlyReader fails if any file outside this package decodes YAML
// directly. There is ONE reader, and this is what keeps it that way.
//
// It is here because the claim was once made and not kept: the reader shipped for two
// config files while sixteen other read sites still called yaml.Unmarshal, and the
// package doc said otherwise. A sentence in a doc comment is not a guarantee. This is.
//
// Adding a new YAML file to inforge means calling yamldoc.Read/Parse and choosing what
// its leaves mean — Decode (literal), DecodeStrict (literal, unknown keys rejected), or
// DecodeResolved (resolved through a chain). That choice is the point: it is where a
// caller says what a file's references may reach.
func TestYamldocIsTheOnlyReader(t *testing.T) {
	root := filepath.Join("..", "..") // repo root, from internal/yamldoc

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "website", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// This package IS the reader.
		if strings.Contains(filepath.ToSlash(path), "internal/yamldoc/") {
			return nil
		}
		b, err := os.ReadFile(path) // #nosec G304 -- walking our own source tree
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if decodeCall.MatchString(line) {
				offenders = append(offenders, filepathLine(path, i+1, line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("YAML is decoded outside internal/yamldoc — every read goes through the one reader:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

func filepathLine(path string, line int, src string) string {
	return filepath.ToSlash(path) + ":" + itoa(line) + ": " + strings.TrimSpace(src)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
