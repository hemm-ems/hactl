package surfaceaudit

import (
	"go/ast"
	"go/printer"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Bool-cell surface
// ---------------------------------------------------------------------------

// boolRenderers are the functions that turn a bool into a table cell. Naming
// them, rather than following the type, keeps this a syntactic rule — the same
// choice the truncation surface makes about its marker.
var boolRenderers = map[string]bool{
	"boolCell":   true, // "yes" / ""
	"yesNo":      true, // "yes" / "no"
	"FormatBool": true, // strconv: "true" / "false"
}

// boolWords are the strings a hand-rolled rendering uses. They matter because
// the vocabulary above is a list, and a list is exactly what a site written
// from memory does not join: `req := "no"; if f.Required { req = "yes" }` is a
// bool rendered into a cell by any reading, and it calls nothing.
var boolWords = map[string]bool{"yes": true, "no": true, "true": true, "false": true}

// boolRendererCall reports whether e calls one of the renderers, and returns
// the source of its argument for the site key.
func boolRendererCall(e ast.Expr, fset *token.FileSet) (arg string, ok bool) {
	call, isCall := e.(*ast.CallExpr)
	if !isCall || len(call.Args) != 1 {
		return "", false
	}
	var name string
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		name = fn.Name
	case *ast.SelectorExpr:
		name = fn.Sel.Name
	default:
		return "", false
	}
	if !boolRenderers[name] {
		return "", false
	}
	return exprSource(call.Args[0], fset), true
}

// exprSource renders an expression back to source text for a site key. It is
// deliberately the text rather than a synthesised name: `d.RequireAdmin` is
// what a reader dispositioning the manifest has to find in the file.
func exprSource(e ast.Expr, fset *token.FileSet) string {
	var buf strings.Builder
	if err := printer.Fprint(&buf, fset, e); err != nil {
		return "?"
	}
	return buf.String()
}

// handRolledBoolWords reports the identifiers in fn that are assigned two
// different boolWords — the shape a rendering takes when it does not call one
// of the renderers above.
func handRolledBoolWords(fn *ast.FuncDecl) []string {
	assigned := map[string]map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		target, isIdent := assign.Lhs[0].(*ast.Ident)
		if !isIdent {
			return true
		}
		lit, isLit := assign.Rhs[0].(*ast.BasicLit)
		if !isLit || lit.Kind != token.STRING {
			return true
		}
		word, err := strconv.Unquote(lit.Value)
		if err != nil || !boolWords[word] {
			return true
		}
		if assigned[target.Name] == nil {
			assigned[target.Name] = map[string]bool{}
		}
		assigned[target.Name][word] = true
		return true
	})
	var names []string
	for name, words := range assigned {
		if len(words) > 1 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// BoolCellSurface is every place a boolean becomes a cell of a text table.
//
// Rule (INVARIANTS.md H-10): a boolean a table renders for a person reaches
// `--json` as a JSON boolean, which means the cell is paired with
// format.Table.SetMachine carrying the bool itself.
//
// A cell is a string because a text table is made of strings, and `--json`
// re-uses the cells. So `dash ls --json` answered `"admin": "false"` — a
// non-empty string, and therefore true to the `if row["admin"]` a consumer
// writes (finding #59). `format.Table.SetMachine` exists for exactly this and
// two commands already used it, so the defect was never "dash ls forgot": it
// was that nothing could say which commands had not. Four sites had not.
//
// The surface deliberately includes sites that are correct today. A census of
// "every bool that becomes a cell" is the only version that catches the fifth
// site on the day it is written; a census of "every bool that becomes a cell
// WRONGLY" is a list of known bugs, which is what the manifest's dispositions
// already are.
func BoolCellSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name: "boolcell",
		Rule: "a boolean rendered into a table cell reaches --json as a JSON boolean (SetMachine), never as its human wording",
	}
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// The renderers themselves are the implementation of the rule, not
			// sites of it: they are where "yes" and "no" are allowed to be.
			if boolRenderers[fn.Name.Name] {
				continue
			}
			seen := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				expr, isExpr := n.(ast.Expr)
				if !isExpr {
					return true
				}
				arg, isCall := boolRendererCall(expr, f.fset)
				if !isCall || seen[arg] {
					return true
				}
				seen[arg] = true
				s.Sites = append(s.Sites, Site{
					Key:  f.rel + ":" + fn.Name.Name + ":" + arg,
					File: f.rel,
					Line: f.fset.Position(expr.Pos()).Line,
					Note: "renders a bool into a cell",
				})
				return true
			})
			for _, name := range handRolledBoolWords(fn) {
				s.Sites = append(s.Sites, Site{
					Key:  f.rel + ":" + fn.Name.Name + ":" + name,
					File: f.rel,
					Line: f.fset.Position(fn.Pos()).Line,
					Note: "builds a bool's wording by hand",
				})
			}
		}
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}
