package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoStringLiteralCitesADecisionID (D-124): a decision id belongs in a code
// comment, a test, docs/decisions.md or the manual's reference — never in
// anything a command renders. TestRenderedPolicyOutputCitesNoDecisionID
// samples the rendered output; this gate closes the rest of the surface by
// refusing a decision id in any string literal of the shipped code, since any
// literal may end up on stdout, stderr, in a JSON value or the audit log.
//
// internal/surfaceaudit is exempt: its notes are the dev-only surface audit's
// report, never a command's output.
func TestNoStringLiteralCitesADecisionID(t *testing.T) {
	id := regexp.MustCompile(`\bD-[0-9]+\b`)
	exempt := map[string]bool{filepath.Join("..", "surfaceaudit"): true}
	var scanned int
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && exempt[path]:
				return filepath.SkipDir
			case d.IsDir(), !strings.HasSuffix(path, ".go"), strings.HasSuffix(path, "_test.go"):
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if m := id.FindString(lit.Value); m != "" {
						t.Errorf("%s: a string literal cites %s — user-facing text names no decision id (D-124); keep it in a comment", fset.Position(lit.Pos()), m)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("scanned %d Go files; the walk has stopped finding the code", scanned)
	}
}
