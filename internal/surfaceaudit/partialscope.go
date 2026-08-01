package surfaceaudit

import (
	"go/ast"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Partial-scope surface
// ---------------------------------------------------------------------------

// PartialScopeSurface is every command body that reads a source which can come
// back incomplete, one site per consuming function.
//
// Rule (INVARIANTS.md H-10, D-7): a source a sweep could not read reaches the
// caller's answer — stated in the report body, or refused when the answer's
// medium cannot carry the statement. Never only a log line.
//
// # Why this surface exists
//
// D-7 was written twice already, and both times the rule was stated over the
// wrong set. First it was "a dashboard", and the fix for a silent dashboard left
// a silent entity registry one function beneath it. Then it was "a source of
// `ref validate`", and `ref scan` — reading the same walk through the same
// warn-only path — kept returning three of twenty-four references at exit 0,
// with the whole config half dropped at slog.Warn (#34). Neither miss left a
// trace anywhere: the set was in prose, in one command's doc comment.
//
// So the set is derived from the code that produces partial answers, in two
// passes, and a command that starts reading one of these sources is red until
// somebody says what it does about a short read:
//
//  1. every function in internal/cmd whose results include a scope type — the
//     dashboard walk's own bookkeeping (dashboardScanScope) and the whole-sweep
//     one (sweepScope);
//  2. every companion route whose response type declares a `Skipped` field —
//     the config half is ONE wire call over N files, so a 200 can be a partial
//     answer that looks complete.
//
// A site is any function in internal/cmd that calls one of those. Producers are
// sites too, on purpose: `countRenameReferences` returned the dashboard scope
// and threw the config half's `skipped` away, which is exactly the shape a
// "consumers only" rule would have waved through.
func PartialScopeSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}

	scopeProducers, skippedRoutes := partialReaders(files)

	s := Surface{
		Name: "partialscope",
		Rule: "a source that could not be read reaches the answer — stated in the body, or refused — never only a log line",
	}
	for _, f := range files {
		if !strings.HasPrefix(f.rel, "internal/cmd/") {
			continue
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			reads := calledNames(fn, scopeProducers, skippedRoutes)
			if len(reads) == 0 {
				continue
			}
			s.Sites = append(s.Sites, Site{
				Key:  f.rel + ":" + fn.Name.Name,
				File: f.rel,
				Line: f.fset.Position(fn.Pos()).Line,
				Note: "reads " + strings.Join(reads, ", ") + ", which can answer over less than it was asked",
			})
		}
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}

// partialReaders is the two derivation passes: the internal/cmd functions that
// hand a scope back, and the companion routes whose response can name files the
// walk did not read.
func partialReaders(files []srcFile) (scopeProducers, skippedRoutes map[string]bool) {
	scopeProducers, skippedRoutes = map[string]bool{}, map[string]bool{}
	responsesWithSkipped := map[string]bool{}
	for _, f := range files {
		switch {
		case strings.HasPrefix(f.rel, "internal/cmd/"):
			for _, decl := range f.ast.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && returnsScope(fn) {
					scopeProducers[fn.Name.Name] = true
				}
			}
		case strings.HasPrefix(f.rel, "internal/companion/"):
			collectSkippedResponses(f.ast, responsesWithSkipped)
		}
	}
	for _, f := range files {
		if !strings.HasPrefix(f.rel, "internal/companion/") {
			continue
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !returnsOneOf(fn, responsesWithSkipped) {
				continue
			}
			skippedRoutes[fn.Name.Name] = true
		}
	}
	return scopeProducers, skippedRoutes
}

// scopeTypes are the bookkeeping types a partial read is recorded in. They are
// named here rather than derived because they ARE the derivation's root: a type
// whose whole purpose is "what this read actually covered".
var scopeTypes = map[string]bool{"dashboardScanScope": true, "sweepScope": true}

// returnsScope reports whether fn hands a scope back to its caller.
func returnsScope(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, r := range fn.Type.Results.List {
		if scopeTypes[typeName(r.Type)] {
			return true
		}
	}
	return false
}

// collectSkippedResponses records every struct type declaring a Skipped field —
// the companion's way of saying "this answer covers fewer files than you asked
// about".
func collectSkippedResponses(f *ast.File, out map[string]bool) {
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == "Skipped" {
					out[ts.Name.Name] = true
				}
			}
		}
		return true
	})
}

// returnsOneOf reports whether fn returns (a pointer to) one of the named types.
func returnsOneOf(fn *ast.FuncDecl, types map[string]bool) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, r := range fn.Type.Results.List {
		if types[strings.TrimPrefix(typeName(r.Type), "*")] {
			return true
		}
	}
	return false
}

// calledNames returns the sorted, deduplicated names of the partial-capable
// reads fn performs, plain calls and method calls alike.
func calledNames(fn *ast.FuncDecl, funcs, methods map[string]bool) []string {
	seen := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if funcs[f.Name] && f.Name != fn.Name.Name {
				seen[f.Name] = true
			}
		case *ast.SelectorExpr:
			if methods[f.Sel.Name] {
				seen[f.Sel.Name] = true
			}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
