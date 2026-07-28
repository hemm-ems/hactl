// Package testaudit classifies the repository's own test functions by what they
// prove, so that "the command did not crash" can never again be mistaken for
// "the command answered correctly".
//
// The failure this defends against is documented as escape mechanism M3 in
// TEST-CONCEPT.md: a stubbed automation write returned nil, and both tiers
// stayed green because every test that touched it only checked that the command
// exited 0. An audit of 2026-07-23 still found twelve integration tests whose
// entire body was `out := runHactl(t, …); _ = out`. Those tests ran a real
// command against a real Home Assistant and then threw the answer away.
//
// The classifier is deliberately structural, not lexical: `_ = out` is only the
// most visible spelling of the problem, and a gate that greps for it would be
// satisfied by renaming the variable. What it looks for instead is a *failure
// site that is reached by inspecting something other than an error*.
//
// # What counts as an assertion
//
// A test asserts if, anywhere in its body (subtest closures included), it can
// reach a `t.Error*`/`t.Fatal*` call whose nearest enclosing condition observes
// a value:
//
//   - `if out != want { t.Errorf(…) }`      — observational, counts.
//   - `if !strings.Contains(out, x) { … }`  — observational, counts.
//   - `if len(rows) == 0 { t.Fatal(…) }`    — observational, counts.
//   - `if err == nil { t.Fatal("want error") }` — observational: the command
//     was required to refuse, and that is a behavioural claim.
//   - `if err != nil { t.Fatalf(…) }`       — NOT an assertion. This is the
//     liveness check M3 is named after; every command that runs at all passes
//     it. `t.Skip` in any form is not an assertion either (TC-8: a skip is a
//     silent pass).
//
// A call to a same-package helper counts when that helper is itself
// assertion-bearing — `assertContains(t, out, "dry-run")` is an assertion no
// matter that the `t.Errorf` lives one frame down. Crucially, a helper only
// qualifies if it *returns nothing*. That single rule is what separates an
// assertion helper from an invocation helper: `runHactl` returns the output it
// captured, so its internal `t.Fatalf(… failed …)` and its degeneracy scan are
// ambient — they apply identically to every test in the tier and therefore
// distinguish none of them. Descending into it would mark every integration
// test as asserting and the gate would prove nothing. (This comment used to
// state that tier's size as a number, which drifted stale; the tier tally this
// package already computes is the one place that count lives — `make
// testcount`, TC-7.)
//
// # What it deliberately does not catch
//
// It does not judge whether the asserted value is *worth* asserting. In
// particular it cannot see that a test asserted a value it had itself just
// supplied ("write X, read back X through the same tool"), because that is a
// dataflow property across a process boundary, not a syntactic one. That class
// is covered by a different rule: H-12 requires every write family to read back
// from Home Assistant directly and to assert a witness field the command never
// mentioned. This gate is the floor beneath H-12, not a substitute for it.
//
// It also says nothing about assertion strength: one `assertContains` on a
// header row passes. The floor is "this test can go red for a reason other than
// the process dying", which is the property twelve tests did not have.
package testaudit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExemptDirective marks a test function as knowingly assertion-free. It must be
// followed by prose explaining why, in the doc comment of the test it exempts:
//
//	//test:no-assert <reason>
//
// The reason is mandatory and is printed by the gate, so an exemption is a
// visible decision with an author rather than a silent hole. This mirrors how
// the repo already handles `//nolint:gosmopolitan` suppressions.
const ExemptDirective = "//test:no-assert"

// minExemptReason is the shortest reason the gate will accept. It exists to
// stop `//test:no-assert n/a` from becoming the idiom.
const minExemptReason = 20

// Test is one test function and what the classifier concluded about it.
type Test struct {
	// Dir is the package directory, relative to the repository root.
	Dir string
	// File is the file path, relative to the repository root.
	File string
	// Name is the test function's name.
	Name string
	// Line is the line the function is declared on.
	Line int
	// BuildTags are the //go:build constraints on File, verbatim, or "" for an
	// untagged file. Reported so the gate's tally can be read per tier.
	BuildTags string
	// Asserts is true when the body can reach an observational failure site.
	Asserts bool
	// ExemptReason is the prose after ExemptDirective, or "" when absent.
	ExemptReason string
}

// Tier names the test tier File belongs to, derived from its build tags: the
// Docker tiers are exactly the tagged ones, and an untagged file is unit.
func (t Test) Tier() string {
	switch {
	case strings.Contains(t.BuildTags, "companion_discovery"):
		return "discovery"
	case strings.Contains(t.BuildTags, "companion"):
		return "companion"
	case strings.Contains(t.BuildTags, "integration"):
		return "integration"
	default:
		return "unit"
	}
}

// skipDirs are directories that hold no Go we should classify.
var skipDirs = map[string]bool{
	".git":     true,
	"testdata": true,
	"vendor":   true,
	"docs":     true,
}

// ScanRepo classifies every test function under root. Files are parsed from
// disk rather than loaded through go/packages so that build-tag-gated tiers are
// covered too: the integration, companion and companion_discovery tests are
// exactly the ones that talk to a real wire, and a gate that could only see
// what the untagged build compiles would be blind to them.
func ScanRepo(root string) ([]Test, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", root, err)
	}

	var all []Test
	for _, dir := range dirs {
		tests, dirErr := scanDir(root, dir)
		if dirErr != nil {
			return nil, dirErr
		}
		all = append(all, tests...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].Line < all[j].Line
	})
	return all, nil
}

// scanDir classifies the test functions of one package directory. Helper
// resolution is per-directory because that is the scope a test can call into
// without qualification.
func scanDir(root, dir string) ([]Test, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, parseErr)
		}
		files[path] = f
	}
	if len(files) == 0 {
		return nil, nil
	}

	pkg := &pkgScope{fset: fset, files: files, verdict: map[string]helperVerdict{}}

	rel := func(path string) string {
		r, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return path
		}
		return filepath.ToSlash(r)
	}

	var tests []Test
	for path, f := range files {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		tags := buildTags(f)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isTestFunc(fn) {
				continue
			}
			tests = append(tests, Test{
				Dir:          rel(dir),
				File:         rel(path),
				Name:         fn.Name.Name,
				Line:         fset.Position(fn.Pos()).Line,
				BuildTags:    tags,
				Asserts:      pkg.funcAsserts(fn.Type, fn.Body),
				ExemptReason: exemptReason(fn.Doc),
			})
		}
	}
	return tests, nil
}

// isTestFunc reports whether fn is a test the `go test` runner will call.
// TestMain is excluded: it is the tier's harness, not a test, and it has no
// answer of its own to assert on.
func isTestFunc(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || fn.Body == nil {
		return false
	}
	name := fn.Name.Name
	if !strings.HasPrefix(name, "Test") || name == "TestMain" {
		return false
	}
	if len(name) > len("Test") {
		r := rune(name[len("Test")])
		if r >= 'a' && r <= 'z' {
			return false
		}
	}
	return len(fn.Type.Params.List) == 1 && isTestingT(fn.Type.Params.List[0].Type)
}

// isTestingT reports whether expr is *testing.T, *testing.B, *testing.F or
// testing.TB.
func isTestingT(expr ast.Expr) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "testing" {
		return false
	}
	switch sel.Sel.Name {
	case "T", "B", "F", "TB":
		return true
	}
	return false
}

// buildTags returns the //go:build line of a file verbatim, minus the marker.
func buildTags(f *ast.File) string {
	for _, group := range f.Comments {
		for _, c := range group.List {
			if after, ok := strings.CutPrefix(c.Text, "//go:build "); ok {
				return strings.TrimSpace(after)
			}
		}
		// Build constraints precede the package clause; anything after it is
		// ordinary prose.
		if group.Pos() > f.Package {
			break
		}
	}
	return ""
}

// exemptReason returns the prose following ExemptDirective in a doc comment.
func exemptReason(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	for _, c := range doc.List {
		text := "//" + strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		rest, ok := strings.CutPrefix(text, ExemptDirective)
		if !ok {
			continue
		}
		// The directive must end where it ends: `//test:no-assertions-needed`
		// is a different comment, not this one with a reason attached.
		if rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

// ExemptReasonTooShort reports whether an exemption's prose is too thin to be a
// decision anyone can review.
func ExemptReasonTooShort(reason string) bool {
	return len(reason) < minExemptReason
}

// ---------------------------------------------------------------------------
// classification
// ---------------------------------------------------------------------------

// guard describes the nearest enclosing condition of a failure site.
type guard int

const (
	// guardNone: no enclosing condition. A bare t.Fatal at the tail of a
	// search loop is the canonical shape, and it is observational — the loop
	// did the observing.
	guardNone guard = iota
	// guardErrOnly: the condition inspects nothing but "an error happened".
	guardErrOnly
	// guardObservational: the condition inspects a value.
	guardObservational
)

type helperVerdict struct {
	asserts bool
	done    bool
}

// pkgScope resolves helper calls within one package directory.
type pkgScope struct {
	fset    *token.FileSet
	files   map[string]*ast.File
	verdict map[string]helperVerdict
}

// funcAsserts reports whether the body can reach an observational failure site,
// directly or through an assertion helper of the same package.
func (p *pkgScope) funcAsserts(sig *ast.FuncType, body *ast.BlockStmt) bool {
	s := &scan{pkg: p, tNames: paramNames(sig)}
	s.stmt(body, guardNone)
	return s.asserts
}

// paramNames returns the names bound to *testing.T-ish parameters.
func paramNames(sig *ast.FuncType) map[string]bool {
	names := map[string]bool{}
	if sig == nil || sig.Params == nil {
		return names
	}
	for _, field := range sig.Params.List {
		if !isTestingT(field.Type) {
			continue
		}
		for _, id := range field.Names {
			names[id.Name] = true
		}
	}
	return names
}

// assertionHelper reports whether a same-package function called by name is an
// assertion helper: it takes a *testing.T, returns nothing, and can reach an
// observational failure site.
//
// The "returns nothing" rule is the load-bearing half. A helper that hands a
// value back is an invocation helper — runHactl, runHactlJSON, getOracleHA —
// and the checks it makes on the way (the command exited 0; its output carries
// no degeneracy marker) are ambient properties of the tier, true for every test
// that calls it. Counting them would mark every test in the tier as asserting.
func (p *pkgScope) assertionHelper(name string) bool {
	if v, ok := p.verdict[name]; ok {
		return v.asserts // false while in progress: breaks recursion cycles
	}
	fn := p.lookup(name)
	if fn == nil || fn.Body == nil {
		p.verdict[name] = helperVerdict{done: true}
		return false
	}
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		p.verdict[name] = helperVerdict{done: true}
		return false
	}
	if len(paramNames(fn.Type)) == 0 {
		p.verdict[name] = helperVerdict{done: true}
		return false
	}
	p.verdict[name] = helperVerdict{} // in progress
	asserts := p.funcAsserts(fn.Type, fn.Body)
	p.verdict[name] = helperVerdict{asserts: asserts, done: true}
	return asserts
}

// lookup finds a package-level function declaration by name.
func (p *pkgScope) lookup(name string) *ast.FuncDecl {
	for _, f := range p.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == name {
				return fn
			}
		}
	}
	return nil
}

// scan walks one function body tracking the nearest enclosing guard.
type scan struct {
	pkg     *pkgScope
	tNames  map[string]bool
	asserts bool
}

func (s *scan) stmt(n ast.Stmt, g guard) {
	if n == nil || s.asserts {
		return
	}
	switch st := n.(type) {
	case *ast.BlockStmt:
		for _, inner := range st.List {
			s.stmt(inner, g)
		}
	case *ast.IfStmt:
		s.stmt(st.Init, g)
		cg := classifyCond(st.Cond)
		s.expr(st.Cond, g)
		s.stmt(st.Body, cg)
		// The else branch is guarded by the negation, which is error-only
		// exactly when the condition is.
		s.stmt(st.Else, cg)
	case *ast.ForStmt:
		s.stmt(st.Init, g)
		s.expr(st.Cond, g)
		s.stmt(st.Post, g)
		s.stmt(st.Body, g)
	case *ast.RangeStmt:
		s.expr(st.X, g)
		s.stmt(st.Body, g)
	case *ast.SwitchStmt:
		s.switchStmt(st, g)
	case *ast.TypeSwitchStmt:
		s.stmt(st.Init, g)
		s.stmt(st.Assign, g)
		s.stmt(st.Body, guardObservational)
	case *ast.SelectStmt:
		s.stmt(st.Body, g)
	case *ast.CaseClause:
		for _, inner := range st.Body {
			s.stmt(inner, g)
		}
	case *ast.CommClause:
		s.stmt(st.Comm, g)
		for _, inner := range st.Body {
			s.stmt(inner, g)
		}
	case *ast.LabeledStmt:
		s.stmt(st.Stmt, g)
	default:
		// Every other statement kind — expressions, assignments, defers, go,
		// returns, declarations — carries no guard of its own.
		s.unguarded(n, g)
	}
}

// switchStmt walks a switch. A tagless switch whose case expressions are all
// error-only is the switch form of `if err != nil`, and its clauses inherit
// that guard; every other clause observes a value.
func (s *scan) switchStmt(st *ast.SwitchStmt, g guard) {
	s.stmt(st.Init, g)
	s.expr(st.Tag, g)
	for _, c := range st.Body.List {
		clause, ok := c.(*ast.CaseClause)
		if !ok {
			continue
		}
		cg := guardObservational
		if st.Tag == nil && allErrOnly(clause.List) {
			cg = guardErrOnly
		}
		for _, inner := range clause.Body {
			s.stmt(inner, cg)
		}
	}
}

// unguarded walks a statement that introduces no guard of its own, handing
// each nested statement and expression back to the guarded walkers so the
// enclosing guard is carried through unchanged.
func (s *scan) unguarded(n ast.Stmt, g guard) {
	ast.Inspect(n, func(node ast.Node) bool {
		if node == n {
			return true
		}
		if inner, ok := node.(ast.Stmt); ok {
			if _, isBlock := inner.(*ast.BlockStmt); !isBlock {
				s.stmt(inner, g)
				return false
			}
		}
		if e, ok := node.(ast.Expr); ok {
			s.expr(e, g)
			return false
		}
		return true
	})
}

// expr walks an expression looking for failure sites, helper calls and nested
// function literals (t.Run subtests, deferred closures).
func (s *scan) expr(e ast.Expr, g guard) {
	if e == nil || s.asserts {
		return
	}
	ast.Inspect(e, func(node ast.Node) bool {
		if s.asserts {
			return false
		}
		switch x := node.(type) {
		case *ast.FuncLit:
			// A subtest closure starts a fresh guard context and may rebind t.
			inner := &scan{pkg: s.pkg, tNames: mergeNames(s.tNames, paramNames(x.Type))}
			inner.stmt(x.Body, guardNone)
			if inner.asserts {
				s.asserts = true
			}
			return false
		case *ast.CallExpr:
			s.call(x, g)
			return true
		}
		return true
	})
}

// call classifies one call: a t.Error*/t.Fatal* failure site, or a call to a
// same-package assertion helper.
func (s *scan) call(c *ast.CallExpr, g guard) {
	switch fn := c.Fun.(type) {
	case *ast.SelectorExpr:
		recv, ok := fn.X.(*ast.Ident)
		if !ok || !s.tNames[recv.Name] {
			return
		}
		if isFailureSite(fn.Sel.Name) && g != guardErrOnly {
			s.asserts = true
		}
	case *ast.Ident:
		if s.pkg.assertionHelper(fn.Name) {
			s.asserts = true
		}
	}
}

// isFailureSite reports whether a testing.T method fails the test. Skip, Log
// and friends are deliberately absent: a skipped test is a silent pass, which
// TC-8 forbids on a load-bearing path.
func isFailureSite(name string) bool {
	switch name {
	case "Error", "Errorf", "Fatal", "Fatalf":
		return true
	}
	return false
}

func mergeNames(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// classifyCond decides whether a condition observes a value or merely notices
// that an error happened.
//
// `err != nil` is the shape of Go's error propagation, and a failure site under
// it fires for any command that runs at all — that is the liveness check M3 is
// named after. `err == nil` is the opposite: it claims the command had to
// refuse, which is a behavioural claim about the answer.
func classifyCond(cond ast.Expr) guard {
	if isErrOnly(cond) {
		return guardErrOnly
	}
	return guardObservational
}

func allErrOnly(exprs []ast.Expr) bool {
	if len(exprs) == 0 {
		return false
	}
	for _, e := range exprs {
		if !isErrOnly(e) {
			return false
		}
	}
	return true
}

func isErrOnly(cond ast.Expr) bool {
	switch c := cond.(type) {
	case *ast.ParenExpr:
		return isErrOnly(c.X)
	case *ast.BinaryExpr:
		switch c.Op {
		case token.LAND, token.LOR:
			return isErrOnly(c.X) && isErrOnly(c.Y)
		case token.NEQ:
			return isErrIdent(c.X) && isNil(c.Y) || isErrIdent(c.Y) && isNil(c.X)
		default:
			return false
		}
	default:
		return false
	}
}

func isNil(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// isErrIdent recognises an error-carrying variable by name. Go's convention is
// strong enough here that a type check would buy nothing but a dependency on
// building every tagged tier: anything named err/…Err/…Error compared against
// nil is an error, and anything else compared against nil (a response, a
// pointer field) is a value worth observing.
func isErrIdent(e ast.Expr) bool {
	var name string
	switch x := e.(type) {
	case *ast.Ident:
		name = x.Name
	case *ast.SelectorExpr:
		name = x.Sel.Name
	default:
		return false
	}
	lower := strings.ToLower(name)
	return lower == "err" || strings.HasSuffix(lower, "err") || strings.HasSuffix(lower, "error")
}
