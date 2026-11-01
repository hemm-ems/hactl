package surfaceaudit

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/tools/go/packages"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// ---------------------------------------------------------------------------
// Policy surface (H-29)
// ---------------------------------------------------------------------------
//
// H-29: every request hactl sends carries a policy verdict, and every write
// command checks its whole plan before its first write. A policy is a
// guardrail only as wide as its coverage — a request the policy never sees is
// a request it allows — and hactl has no single choke point: websocket
// messages leave through sendCommand AND through direct conn.WriteJSON calls,
// REST through haapi.Client's request builders, companion calls through
// companion.Client's. A mapping written from "the requests the write commands
// send" would be the hand enumeration this package keeps finding one site
// short. So the surface is derived from the source, and it has two halves.
//
// The request half is every request the product's source can send, keyed the
// way dev/surfaces/policy.manifest and the classifier (internal/policy/reqmap)
// key it: `ws <type>`, `ws supervisor/api <METHOD> <endpoint>`, `rest <METHOD>
// <template>`, `companion <METHOD> <route>`. A path the source builds by
// concatenation is keyed by the classifier row of the same shape, so the
// source's names for the dynamic parts never matter. The product is the set of
// module packages the hactl binary imports — test rigs (internal/hatest, the
// fake Supervisor) are not product and are never sites.
//
// The command half is every write command, derived — never hand-listed (D-80,
// R-17): every `--confirm` command of the live cobra tree, plus every command
// whose Run reaches a write sink without one. [PolicyCommandSites] computes
// that reachability over the product's SSA call graph.

// The request builders: the only functions allowed to build an HTTP request.
// Any other http.NewRequest in the product is a request no site describes,
// which the extractor refuses to stay silent about.
var (
	haapiBuilders = map[string]string{
		"doGet": "GET", "doDelete": "DELETE", "doPost": "POST", "doPostOnce": "POST",
	}
	companionBuilders = map[string]string{
		"doGet": "GET", "doPostBody": "POST", "doDelete": "DELETE", "doPut": "PUT",
	}
)

const (
	haapiDir     = "internal/haapi"
	companionDir = "internal/companion"
	// wsSender is the generic websocket sender; its callers are the sites.
	wsSender = "(*WSClient).sendCommand"
	// wsProxy is the generic Supervisor proxy; its callers are the sites.
	wsProxy = "(*WSClient).SupervisorAPI"
)

// requestSite is one derived request, with the function that sends it.
type requestSite struct {
	key  string
	file string
	line int
	fn   string // enclosingFunc spelling: "(*WSClient).EntityRegistryUpdate"
}

// productFiles parses the non-test files of every module package the hactl
// binary imports, in its default build.
func productFiles(root string) ([]srcFile, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps | packages.NeedModule,
		Dir:  root,
	}
	pkgs, err := packages.Load(cfg, "./cmd/hactl")
	if err != nil {
		return nil, fmt.Errorf("loading the hactl binary's packages: %w", err)
	}
	if len(pkgs) != 1 || pkgs[0].Module == nil {
		return nil, errors.New("./cmd/hactl did not load as one package of a module")
	}
	mod := pkgs[0].Module.Path
	var paths []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.PkgPath != mod && !strings.HasPrefix(p.PkgPath, mod+"/") {
			return
		}
		paths = append(paths, p.GoFiles...)
	})
	sort.Strings(paths)
	out := make([]srcFile, 0, len(paths))
	for _, path := range paths {
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, perr)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		out = append(out, srcFile{rel: filepath.ToSlash(rel), fset: fset, ast: f})
	}
	return out, nil
}

// PolicyRequestSites is the request half of the policy surface: every request
// the product's source can send.
func PolicyRequestSites(root string) ([]Site, error) {
	files, err := productFiles(root)
	if err != nil {
		return nil, err
	}
	reqs, err := policyRequests(files)
	if err != nil {
		return nil, err
	}
	return requestSitesOf(reqs), nil
}

// testInfraDirs are the trees a source walk leaves out: test infrastructure,
// never product, and imported by no non-test file (policy.manifest header).
// Only [PolicySurface]'s fallback walk needs them; the binary's import closure
// leaves them out by construction.
var testInfraDirs = []string{"internal/hatest/", "internal/companiontest_discovery/"}

// PolicySurface is H-29's whole site set over root: the request half derived
// from the source and the command half handed in, one Site per write command
// keyed by its cobra command path (`hactl area create`, the `cmd ` prefix
// added when absent) — the census [PolicyCommandSites] derives. An empty census
// is an error: it means the cobra walk or the call graph stopped matching, and
// a surface built on it would pass forever proving nothing.
//
// The request half is [PolicyRequestSites] — the hactl binary's own import
// closure — whenever root holds the binary (./cmd/hactl). A tree without it (a
// fixture) is read by a walk of its non-test sources minus [testInfraDirs].
func PolicySurface(root string, commands []Site) (Surface, error) {
	if len(commands) == 0 {
		return Surface{}, errors.New("the write-command census is empty — the cobra walk or the call graph has stopped matching")
	}
	var reqs []Site
	if fi, err := os.Stat(filepath.Join(root, "cmd", "hactl")); err == nil && fi.IsDir() {
		if reqs, err = PolicyRequestSites(root); err != nil {
			return Surface{}, err
		}
	} else {
		files, werr := scanSources(root)
		if werr != nil {
			return Surface{}, werr
		}
		files = slices.DeleteFunc(files, func(f srcFile) bool {
			return slices.ContainsFunc(testInfraDirs, func(d string) bool { return strings.HasPrefix(f.rel, d) })
		})
		derived, derr := policyRequests(files)
		if derr != nil {
			return Surface{}, derr
		}
		reqs = requestSitesOf(derived)
	}
	if len(reqs) == 0 {
		return Surface{}, errors.New("the request half is empty — the extractor has stopped matching the clients")
	}
	sites := slices.Clone(reqs)
	for _, c := range commands {
		if !strings.HasPrefix(c.Key, "cmd ") {
			c.Key = "cmd " + c.Key
		}
		sites = append(sites, c)
	}
	return Surface{
		Name:  "policy",
		Rule:  "every request hactl sends carries a policy verdict, and every write command checks its whole plan before its first write (H-29)",
		Sites: sites,
	}, nil
}

// requestSitesOf collapses derived requests into one site per key, the note
// naming every function that sends it.
func requestSitesOf(reqs []requestSite) []Site {
	byKey := map[string]*Site{}
	fns := map[string][]string{}
	var keys []string
	for _, r := range reqs {
		if _, ok := byKey[r.key]; !ok {
			byKey[r.key] = &Site{Key: r.key, File: r.file, Line: r.line}
			keys = append(keys, r.key)
		}
		fns[r.key] = append(fns[r.key], r.fn)
	}
	sort.Strings(keys)
	out := make([]Site, 0, len(keys))
	for _, k := range keys {
		s := byKey[k]
		names := uniqueSorted(fns[k])
		s.Note = "sent by " + strings.Join(names, ", ")
		out = append(out, *s)
	}
	return out
}

func uniqueSorted(ss []string) []string {
	sort.Strings(ss)
	out := ss[:0]
	for i, s := range ss {
		if i == 0 || s != ss[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// policyRequests derives the requests in a set of parsed files. An unresolvable
// send — a websocket type, a Supervisor endpoint or an HTTP request built where
// no site can be named — is an error, never a silent omission.
func policyRequests(files []srcFile) ([]requestSite, error) {
	helpers := builderHelpers(files)
	var out []requestSite
	var errs []string
	for _, f := range files {
		dir := filepath.ToSlash(filepath.Dir(f.rel))
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := funcName(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				line := f.fset.Position(call.Pos()).Line
				where := fmt.Sprintf("%s:%d (%s)", f.rel, line, name)
				if helpers[dir+"|"+name] && isHTTPConstructor(call) {
					return true // a builder's own helper: the builder's callers are the sites
				}
				reqs, err := requestsOfCall(call, fn, name, dir)
				if err != nil {
					errs = append(errs, where+": "+err.Error())
					return true
				}
				for _, k := range reqs {
					out = append(out, requestSite{key: k, file: f.rel, line: line, fn: name})
				}
				return true
			})
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, fmt.Errorf("requests no site can name:\n  %s", strings.Join(errs, "\n  "))
	}
	return out, nil
}

// funcName spells a declaration the way enclosingFunc does.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return "(" + typeName(fn.Recv.List[0].Type) + ")." + fn.Name.Name
	}
	return fn.Name.Name
}

// requestsOfCall returns the keys of the requests one call sends, if any.
func requestsOfCall(call *ast.CallExpr, fn *ast.FuncDecl, name, dir string) ([]string, error) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, nil
	}
	method := sel.Sel.Name
	if pkg, isPkg := sel.X.(*ast.Ident); isPkg && pkg.Name == "http" {
		switch method {
		case "NewRequest", "NewRequestWithContext", "Get", "Post", "PostForm", "Head":
			if isBuilder(dir, name) {
				return nil, nil
			}
			return nil, fmt.Errorf("http.%s outside the request builders (%s doGet/doDelete/doPost/doPostOnce, %s doGet/doPostBody/doDelete/doPut) builds a request no policy site names", method, haapiDir, companionDir)
		}
	}
	switch method {
	case "sendCommand":
		return wsSendKeys(call, name)
	case "SupervisorAPI":
		return supervisorKeys(call, fn)
	case "WriteJSON":
		return writeJSONKeys(fn, name)
	}
	if dir == haapiDir && !isBuilder(dir, name) {
		if m, isB := haapiBuilders[method]; isB {
			return httpKeys(reqmap.REST, m, call, fn)
		}
	}
	if dir == companionDir && !isBuilder(dir, name) {
		if m, isB := companionBuilders[method]; isB {
			return httpKeys(reqmap.Companion, m, call, fn)
		}
	}
	return nil, nil
}

// isHTTPConstructor reports whether a call is one of the net/http calls that
// build or send a request.
func isHTTPConstructor(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "http" {
		return false
	}
	switch sel.Sel.Name {
	case "NewRequest", "NewRequestWithContext", "Get", "Post", "PostForm", "Head":
		return true
	}
	return false
}

// builderHelpers returns the functions of the two client directories that a
// request builder delegates to (`doGet` → `send` → http.NewRequest): every
// function there whose callers — at least one — are all builders or such
// helpers. A request built in one is sent only through a builder, whose callers
// are the sites, so it names nothing new. A helper with any other caller is no
// helper: a request it builds is refused like any other unnamed send. Keys are
// `<dir>|<funcName>`; callers are matched by the called name within the same
// directory.
func builderHelpers(files []srcFile) map[string]bool {
	declared := clientFuncs(files)
	callers := clientCallers(files, declared)
	allowed := map[string]bool{}
	for dir, names := range declared {
		for _, full := range names {
			if isBuilder(dir, full) {
				allowed[dir+"|"+full] = true
			}
		}
	}
	helpers := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for k, cs := range callers {
			if !allowed[k] && onlyCalledBy(k, cs, allowed) {
				allowed[k], helpers[k], changed = true, true, true
			}
		}
	}
	return helpers
}

// clientDirFuncs yields each function declaration with a body in the two
// client directories, with its directory.
func clientDirFuncs(files []srcFile, visit func(dir string, fn *ast.FuncDecl)) {
	for _, f := range files {
		dir := filepath.ToSlash(filepath.Dir(f.rel))
		if dir != haapiDir && dir != companionDir {
			continue
		}
		for _, decl := range f.ast.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				visit(dir, fn)
			}
		}
	}
}

// clientFuncs maps each client directory's bare function names to their
// enclosingFunc spelling.
func clientFuncs(files []srcFile) map[string]map[string]string {
	declared := map[string]map[string]string{}
	clientDirFuncs(files, func(dir string, fn *ast.FuncDecl) {
		if declared[dir] == nil {
			declared[dir] = map[string]string{}
		}
		declared[dir][fn.Name.Name] = funcName(fn)
	})
	return declared
}

// clientCallers maps `<dir>|<funcName>` to the functions of the same directory
// that call it by name.
func clientCallers(files []srcFile, declared map[string]map[string]string) map[string]map[string]bool {
	callers := map[string]map[string]bool{}
	clientDirFuncs(files, func(dir string, fn *ast.FuncDecl) {
		caller := funcName(fn)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			callee, ok := declared[dir][calledName(call)]
			if !ok || callee == caller {
				return true
			}
			k := dir + "|" + callee
			if callers[k] == nil {
				callers[k] = map[string]bool{}
			}
			callers[k][caller] = true
			return true
		})
	})
	return callers
}

// calledName is the bare name a call names: `f(…)` or `x.f(…)`.
func calledName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}
	return ""
}

// onlyCalledBy reports whether a function has callers and every one of them is
// allowed.
func onlyCalledBy(key string, callers, allowed map[string]bool) bool {
	if len(callers) == 0 {
		return false
	}
	dir, _, _ := strings.Cut(key, "|")
	for c := range callers {
		if !allowed[dir+"|"+c] {
			return false
		}
	}
	return true
}

// isBuilder reports whether a function is one of the request builders.
func isBuilder(dir, name string) bool {
	switch dir {
	case haapiDir:
		_, ok := haapiBuilders[strings.TrimPrefix(name, "(*Client).")]
		return ok && strings.HasPrefix(name, "(*Client).")
	case companionDir:
		_, ok := companionBuilders[strings.TrimPrefix(name, "(*Client).")]
		return ok && strings.HasPrefix(name, "(*Client).")
	}
	return false
}

func wsSendKeys(call *ast.CallExpr, name string) ([]string, error) {
	if len(call.Args) < 2 {
		return nil, fmt.Errorf("sendCommand called with %d arguments", len(call.Args))
	}
	if name == wsProxy {
		// The generic Supervisor proxy: its callers are keyed on (method,
		// endpoint), below, never on the type alone.
		return nil, nil
	}
	if t := litString(call.Args[1]); t != "" {
		return []string{"ws " + t}, nil
	}
	return nil, errors.New("sendCommand's type is not a string literal, so no `ws <type>` site can name it")
}

func supervisorKeys(call *ast.CallExpr, fn *ast.FuncDecl) ([]string, error) {
	if len(call.Args) < 3 {
		return nil, fmt.Errorf("SupervisorAPI called with %d arguments", len(call.Args))
	}
	m := strings.ToUpper(litString(call.Args[2]))
	if m == "" {
		return nil, errors.New("SupervisorAPI's method is not a string literal")
	}
	var keys []string
	for _, tmpl := range exprTemplates(call.Args[1], fn, 0) {
		key, ok := reqmap.MatchTemplate(reqmap.WS, m, tmpl)
		if !ok {
			key = "ws supervisor/api " + m + " " + tmpl
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// writeJSONKeys keys a direct conn.WriteJSON by the `"type": "<lit>"` of the
// message its function builds. The generic sender is the one function whose
// type is not a literal.
func writeJSONKeys(fn *ast.FuncDecl, name string) ([]string, error) {
	var lits []string
	dynamic := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok || litString(kv.Key) != "type" {
			return true
		}
		if v := litString(kv.Value); v != "" {
			lits = append(lits, v)
		} else {
			dynamic = true
		}
		return true
	})
	if dynamic && len(lits) == 0 && name == wsSender {
		return nil, nil
	}
	if len(lits) != 1 || dynamic {
		return nil, fmt.Errorf("conn.WriteJSON in a function building %d literal message types (and a dynamic one: %v); a direct send must build exactly one", len(lits), dynamic)
	}
	return []string{"ws " + lits[0]}, nil
}

func httpKeys(tr reqmap.Transport, method string, call *ast.CallExpr, fn *ast.FuncDecl) ([]string, error) {
	if len(call.Args) < 2 {
		return nil, fmt.Errorf("request builder called with %d arguments", len(call.Args))
	}
	var keys []string
	for _, tmpl := range exprTemplates(call.Args[1], fn, 0) {
		tmpl, _, _ = strings.Cut(tmpl, "?")
		key, ok := reqmap.MatchTemplate(tr, method, tmpl)
		if !ok {
			key = string(tr) + " " + method + " " + snakePlaceholders(tmpl)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// snakePlaceholders spells every `{name}` of a template the way the manifest's
// key grammar does — snake_case (`{thingID}` → `{thing_id}`) — so a site no row
// names yet is reported in the grammar its new line has to use.
func snakePlaceholders(tmpl string) string {
	var b strings.Builder
	inside := false
	runes := []rune(tmpl)
	for i, r := range runes {
		switch {
		case r == '{':
			inside = true
		case r == '}':
			inside = false
		case inside && unicode.IsUpper(r):
			prevLower := i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]))
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			prevUpper := i > 0 && unicode.IsUpper(runes[i-1])
			if prevLower || (prevUpper && nextLower) {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// exprTemplates renders a path expression as the templates it can take: a
// literal is itself, a concatenation is the product of its parts, a local
// variable is every value assigned to it in the function (a `+=` appends), and
// anything else is one `{name}` placeholder.
func exprTemplates(e ast.Expr, fn *ast.FuncDecl, depth int) []string {
	if depth > 8 {
		return []string{"{expr}"}
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if s := litString(x); s != "" || x.Kind == token.STRING {
			return []string{s}
		}
	case *ast.ParenExpr:
		return exprTemplates(x.X, fn, depth+1)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			var out []string
			for _, l := range exprTemplates(x.X, fn, depth+1) {
				for _, r := range exprTemplates(x.Y, fn, depth+1) {
					out = append(out, l+r)
				}
			}
			return out
		}
	case *ast.Ident:
		if vals := localValues(x.Name, fn, depth); len(vals) > 0 {
			return vals
		}
		return []string{"{" + x.Name + "}"}
	case *ast.SelectorExpr:
		return []string{"{" + x.Sel.Name + "}"}
	case *ast.CallExpr:
		if len(x.Args) > 0 {
			if id, ok := x.Args[0].(*ast.Ident); ok {
				return []string{"{" + id.Name + "}"}
			}
		}
		return []string{"{" + callName(x) + "}"}
	}
	return []string{"{expr}"}
}

func callName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return "call"
}

// localValues returns every template a local variable is given in fn: each
// `:=`/`=`/`var` value, plus each of those with every `+=` suffix appended.
func localValues(name string, fn *ast.FuncDecl, depth int) []string {
	var bases, suffixes []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			if len(st.Lhs) != len(st.Rhs) {
				return true
			}
			for i, l := range st.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok || id.Name != name {
					continue
				}
				vals := exprTemplates(st.Rhs[i], fn, depth+1)
				if st.Tok == token.ADD_ASSIGN {
					suffixes = append(suffixes, vals...)
				} else {
					bases = append(bases, vals...)
				}
			}
		case *ast.ValueSpec:
			for i, id := range st.Names {
				if id.Name == name && i < len(st.Values) {
					bases = append(bases, exprTemplates(st.Values[i], fn, depth+1)...)
				}
			}
		}
		return true
	})
	out := append([]string(nil), bases...)
	for _, b := range bases {
		for _, s := range suffixes {
			out = append(out, b+s)
		}
	}
	return uniqueSorted(out)
}
