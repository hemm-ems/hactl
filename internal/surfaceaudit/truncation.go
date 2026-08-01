package surfaceaudit

import (
	"go/ast"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Truncation surface
// ---------------------------------------------------------------------------

// truncationMarkerNames are the identifiers a shortening appends when it does
// not spell the marker out. format.TruncationMarker is the only one; naming it
// rather than following the type keeps the extractor a syntactic rule.
var truncationMarkerNames = map[string]bool{"TruncationMarker": true}

// isTruncationMarker reports whether e is the thing a shortening appends: a
// literal made only of dots or ellipses, or the marker constant.
//
// Both spellings have to count. "..." is what every site in this repository
// used before format.Clip existed, and it is what a new site written from
// memory will use.
func isTruncationMarker(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return false
		}
		// A single "." is a separator — `domain + "." + itemID` builds an
		// entity_id in fifteen places and shortens nothing. Two or more is an
		// ellipsis.
		return s == "…" || (len(s) >= 2 && strings.Trim(s, ".") == "")
	case *ast.Ident:
		return truncationMarkerNames[v.Name]
	case *ast.SelectorExpr:
		return truncationMarkerNames[v.Sel.Name]
	}
	return false
}

// shortensAString reports whether e cuts a value short and says so — the shape
// `<anything> + <marker>`.
func shortensAString(e ast.Expr) bool {
	bin, ok := e.(*ast.BinaryExpr)
	return ok && bin.Op == token.ADD && isTruncationMarker(bin.Y)
}

// TruncationSurface is every function that shortens a string for a reader.
//
// Rule (INVARIANTS.md H-10): a value shortened to fit a display is shortened by
// the renderer that knows who is reading, never by the code that assembles the
// value. Everything else — `--json`, `--full`, `--tokensmax 0`, `log show <id>`
// — is downstream of the cut and cannot undo it.
//
// This is the surface finding #14 needed and did not have. Six sites in five
// files each did their own `if len(x) > N { x = x[:N-3] + "..." }` while
// building a table row, so `log --json --full --tokensmax 0` answered messages
// of exactly 60 characters for entries whose real text was a multi-kilobyte
// traceback, and `ent ls --json` answered `"2026-07-31T03:13:..."` for 76 of
// the reference instance's entities while `ent show --json` answered the whole
// instant. The report named one of the six.
//
// Sites are keyed by the enclosing function, and routing through format.Clip is
// how a site leaves the surface: a shortening that has one implementation has
// one place to be wrong, which is the same reduction the clock surface made
// when five renderers became three.
func TruncationSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name: "truncation",
		Rule: "a value shortened to fit a display is shortened by the renderer, never on the way in",
	}
	for _, f := range files {
		seen := map[string]bool{}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			expr, ok := n.(ast.Expr)
			if !ok || !shortensAString(expr) {
				return true
			}
			fn := enclosingFunc(f.ast, expr.Pos())
			if fn == "" || seen[fn] {
				return true
			}
			seen[fn] = true
			s.Sites = append(s.Sites, Site{
				Key:  f.rel + ":" + fn,
				File: f.rel,
				Line: f.fset.Position(expr.Pos()).Line,
				Note: "shortens a string and marks the cut",
			})
			return true
		})
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}
