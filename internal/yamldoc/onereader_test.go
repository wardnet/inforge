package yamldoc_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// yamlPkg is the YAML library. Importing it is fine — the stores and descriptors
// MARSHAL through it, and writing a file is not reading one. Calling one of the
// decode entry points below outside this package is not.
const yamlPkg = "gopkg.in/yaml.v3"

var decodeFuncs = map[string]bool{"Unmarshal": true, "NewDecoder": true}

// TestYamldocIsTheOnlyReader fails if any file outside this package decodes YAML
// directly. There is ONE reader, and this is what keeps it that way.
//
// It exists because the claim was once made and not kept: the reader shipped for two
// config files while sixteen other read sites still called yaml.Unmarshal, and the
// package doc said otherwise. A sentence in a doc comment is not a guarantee.
//
// It resolves the import's LOCAL name from the AST rather than grepping for the text
// "yaml.Unmarshal", because an aliased import (`import y "gopkg.in/yaml.v3"`) sails
// straight past a textual match — and a guard with a hole in it is the same worthless
// assurance as the doc comment it replaced.
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Tests may unmarshal YAML they GENERATED — asserting on a rendered otelcol
		// config or a release manifest is verifying output, not reading a config file.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// This package IS the reader.
		if strings.Contains(filepath.ToSlash(path), "internal/yamldoc/") {
			return nil
		}
		offenders = append(offenders, decodesYAML(t, path)...)
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

// decodesYAML returns every call in path that decodes YAML directly, whatever local
// name the import was given.
func decodesYAML(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// The local name the file gave the YAML package: its alias, or the package name.
	local := ""
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != yamlPkg {
			continue
		}
		local = "yaml"
		if imp.Name != nil {
			local = imp.Name.Name
		}
	}
	if local == "" || local == "_" {
		return nil
	}
	if local == "." {
		// A dot-import would put Unmarshal in scope unqualified. Nothing does this, and
		// nothing should: it would defeat any check that looks for a qualifier.
		return []string{fmt.Sprintf("%s: dot-imports %s", filepath.ToSlash(path), yamlPkg)}
	}

	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != local || !decodeFuncs[sel.Sel.Name] {
			return true
		}
		pos := fset.Position(call.Pos())
		out = append(out, fmt.Sprintf("%s:%d: %s.%s(...)",
			filepath.ToSlash(path), pos.Line, local, sel.Sel.Name))
		return true
	})
	return out
}
