package surfaceaudit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// skipDirs are directories no surface is derived from.
var skipDirs = map[string]bool{
	"vendor": true, "node_modules": true, "testdata": true, "_archive": true,
	"dist": true, "bin": true,
}

// srcFile is one parsed non-test source file.
type srcFile struct {
	rel  string
	fset *token.FileSet
	ast  *ast.File
}

// scanSources parses every non-test .go file under root, ignoring build tags.
//
// Tags are ignored on purpose. A rule about how the product renders a timestamp
// or resolves a target holds in every build configuration, and reading only
// what the untagged build compiles is the same narrowing that made the Docker
// tiers invisible to the linter for months.
func scanSources(root string) ([]srcFile, error) {
	var out []srcFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		out = append(out, srcFile{rel: filepath.ToSlash(rel), fset: fset, ast: f})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// enclosingFunc names the function a node sits in, receiver included.
func enclosingFunc(f *ast.File, pos token.Pos) string {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || pos < fn.Pos() || pos > fn.End() {
			continue
		}
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			return "(" + typeName(fn.Recv.List[0].Type) + ")." + fn.Name.Name
		}
		return fn.Name.Name
	}
	return ""
}

// litString returns the value of an untyped string literal, or "".
func litString(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return v
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + typeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeName(t.X) + "." + t.Sel.Name
	}
	return "?"
}

// ---------------------------------------------------------------------------
// Clock surface
// ---------------------------------------------------------------------------

// clockLayoutTokens are the reference-time fragments that mean a string literal
// is a Go time layout carrying a wall clock. Only hour tokens count: a
// date-only layout has no clock to place in the wrong zone.
//
// Matching the layout rather than the function name is the point. The timezone
// fix reached `formatShortTime` and missed `shortTimestamp`, which renders a
// clock by slicing the ISO string and never mentions time at all. A rule keyed
// on names could not have seen it; a rule keyed on "this code produces an
// hour:minute a human will read" does.
var clockLayoutTokens = []string{"15:04", "3:04", "03:04"}

// isoClockCut recognises the other way to produce a wall clock: cutting the ISO
// timestamp apart. `strings.Cut(ts, "T")` on an RFC3339 string yields the clock
// in the source's own zone, which for Home Assistant is always UTC — and it
// never parses a time, so no fixture and no layout change can reach it. This is
// the spelling `analyze.shortTimestamp` uses, and the reason the timezone fix
// could not have found it by looking at time handling.
//
// The separator must be an argument to a string-splitting call. A bare "T" is
// otherwise just a letter, and matching it anywhere reports a `case "T":` in a
// type switch as a timestamp renderer.
const isoClockCut = "T"

// stringSplitters are the calls whose separator argument decides where a
// string is cut.
var stringSplitters = map[string]bool{
	"Cut": true, "Split": true, "SplitN": true, "Index": true,
	"LastIndex": true, "CutPrefix": true, "CutSuffix": true,
}

// ClockSurface is every place the product renders a wall clock a human reads.
//
// Rule: Home Assistant reports timestamps in UTC and hactl's reader is in their
// own zone, so every site that renders an hour must convert. There is no
// correct site that does not — which is what makes the surface closable.
func ClockSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	type hit struct {
		site Site
		why  []string
	}
	byKey := map[string]*hit{}
	var order []string

	record := func(f srcFile, pos token.Pos, why string) {
		fn := enclosingFunc(f.ast, pos)
		if fn == "" {
			return // package-level layout constants are not renderers
		}
		key := f.rel + ":" + fn
		h, ok := byKey[key]
		if !ok {
			p := f.fset.Position(pos)
			h = &hit{site: Site{Key: key, File: f.rel, Line: p.Line}}
			byKey[key] = h
			order = append(order, key)
		}
		if slices.Contains(h.why, why) {
			return
		}
		h.why = append(h.why, why)
	}

	for _, f := range files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			if pos, why, ok := clockEvidence(n); ok {
				record(f, pos, why)
			}
			return true
		})
	}

	sort.Strings(order)
	s := Surface{
		Name: "clock",
		Rule: "every site that renders a wall clock converts Home Assistant's UTC to the reader's zone",
	}
	for _, key := range order {
		h := byKey[key]
		h.site.Note = strings.Join(h.why, "; ")
		s.Sites = append(s.Sites, h.site)
	}
	return s, nil
}

// clockEvidence reports whether a node produces a wall clock, and how.
//
// The two spellings are separated because only one of them is discoverable by
// looking at time handling. A layout literal says "this is a timestamp" out
// loud; cutting the ISO string on "T" does not, and that is the spelling the
// timezone fix walked past.
func clockEvidence(n ast.Node) (pos token.Pos, why string, ok bool) {
	if call, isCall := n.(*ast.CallExpr); isCall {
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel || !stringSplitters[sel.Sel.Name] {
			return 0, "", false
		}
		for _, arg := range call.Args {
			if litString(arg) == isoClockCut {
				return call.Pos(), `cuts an ISO timestamp on "T" without parsing it`, true
			}
		}
		return 0, "", false
	}
	lit, isLit := n.(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return 0, "", false
	}
	val, err := strconv.Unquote(lit.Value)
	if err != nil {
		return 0, "", false
	}
	for _, tok := range clockLayoutTokens {
		if strings.Contains(val, tok) {
			return lit.Pos(), "clock layout " + strconv.Quote(val), true
		}
	}
	return 0, "", false
}

// ---------------------------------------------------------------------------
// Target-resolution surface
// ---------------------------------------------------------------------------

// runFuncTarget reports the target and writer parameters of a
// `run<Family><Verb>` command entrypoint, or ("", "") when the function takes
// no caller-supplied target.
//
// The convention across internal/cmd is exact and machine-readable:
//
//	func runAutoApply(ctx context.Context, w io.Writer, autoID string) error
//
// A string parameter after (ctx, w) is an identifier the caller typed, which
// means it is an identifier that has to resolve.
func runFuncTarget(fn *ast.FuncDecl) (target, writer string) {
	if fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "run") || fn.Type.Params == nil {
		return "", ""
	}
	var flat []struct{ name, typ string }
	for _, p := range fn.Type.Params.List {
		t := typeName(p.Type)
		if len(p.Names) == 0 {
			flat = append(flat, struct{ name, typ string }{"", t})
			continue
		}
		for _, n := range p.Names {
			flat = append(flat, struct{ name, typ string }{n.Name, t})
		}
	}
	if len(flat) < 3 || flat[0].typ != "context.Context" || flat[1].typ != "io.Writer" {
		return "", ""
	}
	if flat[2].typ != "string" {
		return "", ""
	}
	return flat[2].name, flat[1].name
}

// referencesIdent reports whether an expression mentions a given identifier.
func referencesIdent(e ast.Expr, name string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// returnGuards collects the variables whose failure branch leaves the function.
//
// Both ways this repository reports a lookup that found nothing count, because
// both are real resolution:
//
//	if err != nil { return err }               // error-returning fetch
//	switch { case defErr != nil: return … }
//	if !ok { return fmt.Errorf("not found") }  // bool-returning resolver
//
// and none of these does, which is the distinction the whole rule rests on:
//
//	if err != nil { slog.Warn("could not …", "error", err) }
//	switch { case diffErr != nil: slog.Warn(…) }   // and then carries on
//	if !ok { entityID = "automation." + autoID }   // guesses instead
func returnGuards(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	consider := func(cond ast.Expr, body ast.Node) {
		var name string
		switch c := cond.(type) {
		case *ast.BinaryExpr:
			// `err != nil`
			if c.Op != token.NEQ || !isNilIdent(c.Y) {
				return
			}
			id, ok := c.X.(*ast.Ident)
			if !ok {
				return
			}
			name = id.Name
		case *ast.UnaryExpr:
			// `!ok`
			if c.Op != token.NOT {
				return
			}
			id, ok := c.X.(*ast.Ident)
			if !ok {
				return
			}
			name = id.Name
		default:
			return
		}
		returns := false
		ast.Inspect(body, func(n ast.Node) bool {
			if _, isRet := n.(*ast.ReturnStmt); isRet {
				returns = true
			}
			return !returns
		})
		if returns {
			out[name] = true
		}
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.IfStmt:
			consider(st.Cond, st.Body)
		case *ast.CaseClause:
			for _, c := range st.List {
				consider(c, &ast.BlockStmt{List: st.Body})
			}
		}
		return true
	})
	return out
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// targetAliases follows the identifier the caller typed through the plain
// renames a command does before using it — `configID := automationID`,
// `entityID := autoID`. Without this the analysis loses the target at the first
// assignment and calls a command unresolved that resolves one line later.
func targetAliases(fn *ast.FuncDecl, target string) map[string]bool {
	aliases := map[string]bool{target: true}
	for range 4 { // a fixpoint; command bodies never chain renames deeper
		grew := false
		ast.Inspect(fn, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			mentions := false
			for _, r := range as.Rhs {
				for a := range aliases {
					if referencesIdent(r, a) {
						mentions = true
					}
				}
			}
			if !mentions {
				return true
			}
			for _, l := range as.Lhs {
				if id, isID := l.(*ast.Ident); isID && id.Name != "_" && !aliases[id.Name] {
					aliases[id.Name] = true
					grew = true
				}
			}
			return true
		})
		if !grew {
			break
		}
	}
	return aliases
}

// writerCalls are the ways a command emits its answer to the caller.
var writerCalls = map[string]bool{
	"Fprint": true, "Fprintf": true, "Fprintln": true, "Fwrite": true,
}

// firstOutput returns the position of the first write to the command's writer,
// or token.NoPos when the function prints nothing.
func firstOutput(fn *ast.FuncDecl, writer string) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !writerCalls[sel.Sel.Name] || len(call.Args) == 0 {
			return true
		}
		if !referencesIdent(call.Args[0], writer) {
			return true
		}
		if pos == token.NoPos || call.Pos() < pos {
			pos = call.Pos()
		}
		return true
	})
	return pos
}

// firstEscapingTargetCall returns the position of the earliest call that both
// takes the caller-supplied identifier and has its error returned.
func firstEscapingTargetCall(fn *ast.FuncDecl, target string) token.Pos {
	guards := returnGuards(fn)
	if len(guards) == 0 {
		return token.NoPos
	}
	aliases := targetAliases(fn, target)
	pos := token.NoPos
	check := func(as *ast.AssignStmt) {
		if !callArgsMention(as.Rhs, aliases) || !assignsAnyGuard(as.Lhs, guards) {
			return
		}
		if pos == token.NoPos || as.Pos() < pos {
			pos = as.Pos()
		}
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			check(st)
		case *ast.IfStmt:
			// `if _, err := f(target); err != nil { return … }`
			if as, ok := st.Init.(*ast.AssignStmt); ok {
				check(as)
			}
		}
		return true
	})
	return pos
}

// callArgsMention reports whether any call on the right-hand side passes one of
// the target's aliases as an argument.
func callArgsMention(rhs []ast.Expr, aliases map[string]bool) bool {
	for _, r := range rhs {
		call, ok := r.(*ast.CallExpr)
		if !ok {
			continue
		}
		for _, arg := range call.Args {
			for a := range aliases {
				if referencesIdent(arg, a) {
					return true
				}
			}
		}
	}
	return false
}

// assignsAnyGuard reports whether the left-hand side binds a variable whose
// failure branch leaves the function.
func assignsAnyGuard(lhs []ast.Expr, guards map[string]bool) bool {
	for _, l := range lhs {
		if id, ok := l.(*ast.Ident); ok && guards[id.Name] {
			return true
		}
	}
	return false
}

// targetResolvedBeforeOutput is the structural form of the manual's promise:
// "Dry runs resolve their target before printing a plan … a preview fails
// exactly where --confirm would."
//
// Ordering is the whole rule, and it is why a weaker phrasing does not work.
// Asking only "does the target's failure escape somewhere?" passes
// `runAutoApply`: it hands autoID to writer.Apply, whose error is returned. But
// on the dry-run path Apply never fetches, so that return is unreachable for a
// bad id — while the *earlier* fetch that would have caught it, writer.Diff,
// has its 404 demoted to slog.Warn. By the time any error could escape, the
// command has already printed "validation: ok" and invited --confirm.
//
// Asking instead "can the target's failure escape before the first byte of the
// answer?" separates it from its sibling exactly. `script apply` fetches the
// remote definition, returns the fetch error, and only then prints. Same
// operation, same family, opposite order.
func targetResolvedBeforeOutput(fn *ast.FuncDecl, target, writer string) bool {
	resolve := firstEscapingTargetCall(fn, target)
	if resolve == token.NoPos {
		return false
	}
	out := firstOutput(fn, writer)
	if out == token.NoPos {
		return true // resolves and prints nothing: nothing can be a false plan
	}
	return resolve < out
}

// TargetSurface is every command entrypoint that accepts an identifier from the
// caller and can finish successfully even when that identifier names nothing.
//
// Rule: a command resolves the identifier it was handed before acting on it, so
// that a target which cannot be resolved is an error rather than a plan, and
// every command in a family accepts every identifier the family prints.
//
// `auto apply` is the case this exists for. It fetches the remote config with
// the id it was given, logs the 404 as a WARN, prints "validation: ok" and
// "dry-run: … use --confirm to apply", and exits 0 — a success-shaped plan for
// an automation that does not exist, against an endpoint whose POST is
// create-or-update. Its sibling `script apply` returns the identical error.
func TargetSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name: "target",
		Rule: "an identifier that resolves to nothing ends the command, rather than becoming a plan",
	}
	for _, f := range files {
		if !strings.HasPrefix(f.rel, "internal/cmd/") {
			continue
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			target, writer := runFuncTarget(fn)
			if target == "" || target == "_" {
				continue
			}
			if targetResolvedBeforeOutput(fn, target, writer) {
				continue
			}
			s.Sites = append(s.Sites, Site{
				Key:  f.rel + ":" + fn.Name.Name,
				File: f.rel,
				Line: f.fset.Position(fn.Pos()).Line,
				Note: "prints before an unresolvable " + target + " can fail it",
			})
		}
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}

// ---------------------------------------------------------------------------
// Retry surface
// ---------------------------------------------------------------------------

// postHelpers maps each request helper to whether its retry policy is
// method-aware. A helper that retries without looking at the method turns one
// non-idempotent call into up to three.
//
// `doPostOnce` exists because the class was known: its two call sites were
// added when retrying a flow-start was found to leave several flows dangling.
// The fix went to the two sites where the symptom was seen; `doWithRetry`,
// which every other POST still routes through, was never changed. That is the
// same shape as `auto apply`, and it is why this surface is keyed on call
// sites rather than on the helpers — there are only two helpers, so a surface
// of helpers would be too small to notice anything.
var postHelpers = map[string]bool{
	// doWithRetry now gates its 5xx retry on httpretry.IsIdempotent, so a POST
	// through doPost is issued once. Flipping this back to false is the
	// mutation check: it re-lists all seven call sites.
	"doPost":     true,
	"doPostOnce": true, // single attempt by construction
}

// RetrySurface is every place a non-idempotent request is issued.
//
// Rule (INVARIANTS.md H-1): a POST is retried only when the request provably
// never left the client. A 5xx means the server may have acted, so retrying it
// can fire a service, create a config entry, or write an automation twice.
func RetrySurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name:       "retry",
		Rule:       "a non-idempotent request is never retried against a server that may already have acted on it",
		AllowEmpty: true,
	}
	for _, f := range files {
		if !strings.HasPrefix(f.rel, "internal/haapi/") {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, isSel := call.Fun.(*ast.SelectorExpr)
			if !isSel {
				return true
			}
			safe, known := postHelpers[sel.Sel.Name]
			if !known || safe {
				return true
			}
			fn := enclosingFunc(f.ast, call.Pos())
			if fn == "" || postHelpers[strings.TrimPrefix(fn, "(*Client).")] {
				return true // the helper's own body, not a call site
			}
			s.Sites = append(s.Sites, Site{
				Key:  f.rel + ":" + fn,
				File: f.rel,
				Line: f.fset.Position(call.Pos()).Line,
				Note: "POSTs via " + sel.Sel.Name + ", which retries on 5xx regardless of method",
			})
			return true
		})
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}

// ---------------------------------------------------------------------------
// Preview surface
// ---------------------------------------------------------------------------

// PreviewSurface is every command entrypoint that has a --confirm gate but does
// not build its preview with the shared dryRun() plan.
//
// Rule (H-2, second half): a preview is machine-readable. dryRunPlan.render is
// the only thing in the package that consults --json, so a preview assembled
// with Fprintf is prose no matter what the caller asked for.
//
// Nine previews were in exactly that state — `svc call` and `script run` among
// them, the two an MCP caller reaches for most — while the cited enforcement,
// TestPreviewJSONIsMachineReadable, exercised `helper create` and nothing else.
// A tenth, `auto create`, did build a plan but printed a prose validation line
// to the same writer first, so its stdout did not parse either. That one is not
// visible here; it is why the gate has an executable half as well.
func PreviewSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name:       "preview",
		Rule:       "a preview is built with dryRun(), which is the only renderer that honours --json",
		AllowEmpty: true,
	}
	cmdFuncs := packageFuncs(files, "internal/cmd/")
	for _, f := range files {
		if !strings.HasPrefix(f.rel, "internal/cmd/") {
			continue
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "run") {
				continue
			}
			if !gatesOnConfirm(fn) || buildsAPreview(fn, cmdFuncs) {
				continue
			}
			s.Sites = append(s.Sites, Site{
				Key:  f.rel + ":" + fn.Name.Name,
				File: f.rel,
				Line: f.fset.Position(fn.Pos()).Line,
				Note: "has a --confirm gate and no dryRun() plan",
			})
		}
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}

// gatesOnConfirm reports whether a function branches on a --confirm flag.
func gatesOnConfirm(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && strings.HasPrefix(id.Name, "flag") && strings.HasSuffix(id.Name, "Confirm") {
			found = true
		}
		return !found
	})
	return found
}

// packageFuncs indexes every package-level function under a directory prefix,
// so a call can be followed one level.
func packageFuncs(files []srcFile, prefix string) map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	for _, f := range files {
		if !strings.HasPrefix(f.rel, prefix) {
			continue
		}
		for _, decl := range f.ast.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				out[fn.Name.Name] = fn
			}
		}
	}
	return out
}

// buildsAPreview reports whether a function reaches dryRun(), directly or
// through one same-package helper.
//
// The indirection is real and correct code: `ent set-label`, `ent set-area` and
// `label create` each assemble their plan in a named `dryRun…Summary` helper so
// the confirmed and preview paths cannot describe different things. A gate that
// only matched the direct call would have reported all three as defects, and a
// gate that cries wolf is one people learn to override.
func buildsAPreview(fn *ast.FuncDecl, pkg map[string]*ast.FuncDecl) bool {
	return reachesFunc(fn, "dryRun", pkg)
}

// reachesFunc reports whether fn calls the named same-package function,
// directly or through one same-package helper hop.
func reachesFunc(fn *ast.FuncDecl, name string, pkg map[string]*ast.FuncDecl) bool {
	if callsNamedFunc(fn, name) {
		return true
	}
	reached := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || reached {
			return !reached
		}
		id, isID := call.Fun.(*ast.Ident)
		if !isID {
			return true
		}
		if helper, known := pkg[id.Name]; known && callsNamedFunc(helper, name) {
			reached = true
		}
		return !reached
	})
	return reached
}

// callsNamedFunc reports whether a function directly calls the named
// package-level function.
func callsNamedFunc(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, isID := call.Fun.(*ast.Ident); isID && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// ---------------------------------------------------------------------------
// Automation-reference surface
// ---------------------------------------------------------------------------

// automationResolver is the one function that turns any identifier hactl
// prints for an automation — config `id:`, alias, entity_id, or the entity's
// object id — into the live automation. Every wrapper (resolveAutomationConfigID,
// automationConfigIDFor, automationEntityIDFor) is a one-hop caller of it,
// which is what reachesFunc follows.
const automationResolver = "resolveAutomation"

// automationTargetParam reports whether a run-entrypoint's target parameter is
// an automation reference, by the package's own naming convention: the
// caller-supplied identifier is named autoID or automationID, and nothing else
// in internal/cmd names a parameter that way.
func automationTargetParam(name string) bool {
	return strings.Contains(strings.ToLower(name), "auto")
}

// AutomationRefSurface is every command entrypoint that takes an automation
// reference from the caller but never hands it to the one shared resolver.
//
// Rule (docs/decisions.md D-1, INVARIANTS.md H-17): every command that takes an
// automation identifier accepts every form the family prints — config `id:`,
// alias, entity_id, object id — which in this codebase means exactly one thing:
// the reference passes through resolveAutomation. A parallel, narrower lookup
// is how the past half-fixes happened twice — `auto diff`/`auto apply` still
// refused the id `auto ls` prints after the resolver existed and its own doc
// comment named them as callers, and `auto rollback` matched the raw reference
// against backup filenames that are keyed by config id.
//
// This is a VIOLATION surface: empty is the goal. A new `auto` command whose
// entrypoint follows the run(ctx, w, autoID) convention is swept in
// automatically, and doing its own resolution fails the gate until it is
// dispositioned.
func AutomationRefSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name:       "autoref",
		Rule:       "an automation reference reaches resolveAutomation, so every command accepts every identifier form the family prints",
		AllowEmpty: true,
	}
	cmdFuncs := packageFuncs(files, "internal/cmd/")
	for _, f := range files {
		if !strings.HasPrefix(f.rel, "internal/cmd/") {
			continue
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			target, _ := runFuncTarget(fn)
			if target == "" || !automationTargetParam(target) {
				continue
			}
			if reachesFunc(fn, automationResolver, cmdFuncs) {
				continue
			}
			s.Sites = append(s.Sites, Site{
				Key:  f.rel + ":" + fn.Name.Name,
				File: f.rel,
				Line: f.fset.Position(fn.Pos()).Line,
				Note: "takes " + target + " and never passes it through " + automationResolver,
			})
		}
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}
