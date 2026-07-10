package program

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoteCommandsWithDeleteSetDeleteBeforeReplace guards a class of bug that
// once left three services' systemd units permanently missing in production
// despite a "successful" `inforge deploy`: a remote.Command with both a Delete
// script and no DeleteBeforeReplace resource option is unsafe under Pulumi's
// default create-before-delete replace order — the new resource's Create runs
// (e.g. writing + enabling a unit), then the OLD resource's Delete runs right
// after (e.g. disabling + rm -f'ing that same unit path), silently tearing
// down what Create just built. DeleteBeforeReplace flips that order so a
// replace always leaves a freshly created resource behind. This test parses
// every remote.NewCommand(...) call in this package and fails if one sets
// Delete without also passing pulumi.DeleteBeforeReplace(true).
func TestRemoteCommandsWithDeleteSetDeleteBeforeReplace(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		if filepath.Base(path) == filepath.Base(currentTestFile()) {
			continue // this file itself declares no remote.NewCommand calls, but skip defensively
		}
		src, err := os.ReadFile(path) // #nosec G304 -- path comes from filepath.Glob("*.go") in this package's own directory, not external input
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isRemoteNewCommandCall(call) {
				return true
			}
			// remote.NewCommand(ctx, name, args, opts...): the CommandArgs
			// struct literal is index 2, trailing resource options start at 3.
			if len(call.Args) < 3 {
				return true
			}
			hasDelete := compositeLitHasKey(call.Args[2], "Delete")
			if !hasDelete {
				return true
			}
			if !anyArgIsDeleteBeforeReplaceTrue(call.Args[3:]) {
				pos := fset.Position(call.Pos())
				t.Errorf("%s: remote.NewCommand call sets Delete but not pulumi.DeleteBeforeReplace(true) — "+
					"a replace's default create-before-delete order would tear down what Create just built "+
					"(see this test's doc comment for the production incident this guards against)", pos)
			}
			return true
		})
	}
}

func currentTestFile() string { return "deletebeforereplace_test.go" }

// isRemoteNewCommandCall reports whether call is `remote.NewCommand(...)`.
func isRemoteNewCommandCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "NewCommand" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "remote"
}

// compositeLitHasKey reports whether expr is a (possibly &-prefixed) composite
// literal with a top-level key matching name.
func compositeLitHasKey(expr ast.Expr, name string) bool {
	if unary, ok := expr.(*ast.UnaryExpr); ok {
		expr = unary.X
	}
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
			return true
		}
	}
	return false
}

// anyArgIsDeleteBeforeReplaceTrue reports whether args contains a call to
// pulumi.DeleteBeforeReplace(true).
func anyArgIsDeleteBeforeReplaceTrue(args []ast.Expr) bool {
	for _, arg := range args {
		call, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "DeleteBeforeReplace" {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "pulumi" {
			continue
		}
		if len(call.Args) == 1 {
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "true" {
				return true
			}
		}
	}
	return false
}
