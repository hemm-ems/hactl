package surfaceaudit

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ---------------------------------------------------------------------------
// Map-range surface
// ---------------------------------------------------------------------------

// MapRangeSurface is every statement in the module's non-test sources that
// ranges over a Go map.
//
// Rule (INVARIANTS.md H-16): an answer is a function of the instance, never of
// map iteration order. The Go runtime randomises map iteration on purpose, so
// any walk whose order can reach rendered output must be made canonical before
// rendering — and whether a given walk's order *can* reach output is a
// judgment no parser can make. The extractor therefore derives the census and
// the manifest carries the judgment, site by site: `proven` where a test pins
// the fed output byte-identical across runs, `exempt` where the order is
// structurally unobservable (a set is built, a slice is sorted before use, a
// single-key map is guarded by its length). What the gate closes is the
// census: a new map walk cannot appear silently, which is exactly how
// `companion wireguard status` shipped — it printed one arbitrary entry of
// `m.Resolved` for a release while a hand sweep of the module's other
// map-ranges sat in an audit report nothing re-runs.
//
// Unlike the parser-only surfaces in scan.go this one needs go/types: whether
// `range x` walks a map is a property of x's type, which for half the sites in
// the module lives in another file or another package (a struct field, a named
// map type, a decode target). A syntactic guess would miss exactly the new
// spellings the gate exists to catch, and a missed site here is silent — the
// one failure mode this package treats as worse than any other. The cost is
// that the tree must type-check for the surface to derive, which every tier
// already requires anyway.
//
// Build tags are handled by loading every configuration the sources declare
// (see buildTagConfigs) and unioning the walks, then cross-checking against
// the tag-blind parser scan: a file that compiles under none of the loaded
// configurations fails the derivation loudly instead of being silently
// invisible to the sweep.
func MapRangeSurface(root string) (Surface, error) {
	scanned, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}

	c := &mapWalkCollector{
		root:     root,
		byKey:    map[string]*mapWalkHit{},
		seenLine: map[string]bool{},
		compiled: map[string]bool{},
	}
	for _, tags := range buildTagConfigs(scanned) {
		if err := c.loadConfiguration(tags); err != nil {
			return Surface{}, err
		}
	}

	// The parser scan is tag-blind and the typed loads are not. Any file the
	// parser saw that no configuration compiled is a blind spot: its walks
	// exist in the source and appear on no surface.
	for _, f := range scanned {
		if !c.compiled[filepath.Join(root, filepath.FromSlash(f.rel))] {
			return Surface{}, fmt.Errorf(
				"%s compiles under no build configuration this sweep loads — extend buildTagConfigs before trusting the surface",
				f.rel)
		}
	}

	sort.Strings(c.order)
	s := Surface{
		Name: "maprange",
		Rule: "a walk over a Go map is made canonical before anything it feeds is rendered — an answer is a function of the instance, never of map iteration order",
	}
	for _, key := range c.order {
		h := c.byKey[key]
		sort.Strings(h.walks)
		h.site.Note = strings.Join(h.walks, "; ")
		s.Sites = append(s.Sites, h.site)
	}
	return s, nil
}

// mapWalkHit is one function's map walks, merged into a single site the way
// ClockSurface merges a function's layouts: the disposition is a judgment
// about the function's handling of iteration order, and a walk that moves
// within its function is the same walk.
type mapWalkHit struct {
	site  Site
	walks []string
}

// mapWalkCollector accumulates map walks across build configurations.
type mapWalkCollector struct {
	root     string
	byKey    map[string]*mapWalkHit
	order    []string
	seenLine map[string]bool // file:line, so a walk revisited under another tag is one walk
	compiled map[string]bool // absolute paths some configuration compiled
}

// loadConfiguration type-checks the module under one tag configuration and
// collects every map walk it holds.
func (c *mapWalkCollector) loadConfiguration(tags string) error {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedCompiledGoFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: c.root,
	}
	if tags != "" {
		cfg.BuildFlags = []string{"-tags=" + tags}
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("loading packages (tags %q): %w", tags, err)
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			// An unloadable package would not report its walks, and a
			// shrunken census passes its gate while proving nothing.
			return fmt.Errorf("package %s does not load under tags %q: %w", p.PkgPath, tags, p.Errors[0])
		}
		for i, f := range p.Syntax {
			fname := p.CompiledGoFiles[i]
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			c.compiled[fname] = true
			if err := c.collectFile(p, f, fname); err != nil {
				return err
			}
		}
	}
	return nil
}

// collectFile records every range-over-a-map in one file.
func (c *mapWalkCollector) collectFile(p *packages.Package, f *ast.File, fname string) error {
	rel, err := filepath.Rel(c.root, fname)
	if err != nil {
		rel = fname
	}
	rel = filepath.ToSlash(rel)

	var walkErr error
	ast.Inspect(f, func(n ast.Node) bool {
		if walkErr != nil {
			return false
		}
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		line := p.Fset.Position(rs.Pos()).Line
		tv, hasType := p.TypesInfo.Types[rs.X]
		if !hasType {
			// A range whose expression the checker did not type would be
			// classified by absence — the silent-miss failure mode again.
			walkErr = fmt.Errorf("%s:%d: no type information for a range expression", rel, line)
			return false
		}
		if _, isMap := tv.Type.Underlying().(*types.Map); !isMap {
			return true
		}
		lineKey := rel + ":" + strconv.Itoa(line)
		if c.seenLine[lineKey] {
			return true // the same walk, revisited under another tag configuration
		}
		c.seenLine[lineKey] = true
		c.record(f, rs, tv.Type, rel, line)
		return true
	})
	return walkErr
}

// record merges one walk into its function's site.
func (c *mapWalkCollector) record(f *ast.File, rs *ast.RangeStmt, t types.Type, rel string, line int) {
	fn := enclosingFunc(f, rs.Pos())
	if fn == "" {
		fn = "(var initializer)"
	}
	key := rel + ":" + fn
	h, known := c.byKey[key]
	if !known {
		h = &mapWalkHit{site: Site{Key: key, File: rel, Line: line}}
		c.byKey[key] = h
		c.order = append(c.order, key)
	}
	h.walks = append(h.walks, fmt.Sprintf("walks %s (%s) at line %d",
		rangeExprString(rs.X), shortTypeString(t), line))
}

// buildTagConfigs returns every build configuration the sources ask for: the
// untagged build first, then one configuration per build tag any non-test file
// mentions, in sorted order.
//
// Loading tag by tag matches how the Makefile lints and tests: each tag names
// a tier, and no build ever combines them. A `//go:build a && b` conjunction
// would satisfy none of these single-tag loads — which is why MapRangeSurface
// cross-checks the loads against the tag-blind parser scan and fails loudly on
// any file left uncompiled, rather than assuming this enumeration is complete.
func buildTagConfigs(files []srcFile) []string {
	seen := map[string]bool{}
	var tags []string
	for _, f := range files {
		for _, cg := range f.ast.Comments {
			for _, cmt := range cg.List {
				if !constraint.IsGoBuild(cmt.Text) {
					continue
				}
				expr, err := constraint.Parse(cmt.Text)
				if err != nil {
					continue
				}
				collectConstraintTags(expr, seen, &tags)
			}
		}
	}
	sort.Strings(tags)
	return append([]string{""}, tags...)
}

// collectConstraintTags gathers every tag name a build constraint mentions.
func collectConstraintTags(e constraint.Expr, seen map[string]bool, out *[]string) {
	switch x := e.(type) {
	case *constraint.TagExpr:
		if !seen[x.Tag] {
			seen[x.Tag] = true
			*out = append(*out, x.Tag)
		}
	case *constraint.NotExpr:
		collectConstraintTags(x.X, seen, out)
	case *constraint.AndExpr:
		collectConstraintTags(x.X, seen, out)
		collectConstraintTags(x.Y, seen, out)
	case *constraint.OrExpr:
		collectConstraintTags(x.X, seen, out)
		collectConstraintTags(x.Y, seen, out)
	}
}

// rangeExprString renders a range expression compactly for the report.
func rangeExprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return rangeExprString(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return rangeExprString(x.Fun) + "(…)"
	case *ast.IndexExpr:
		return rangeExprString(x.X) + "[…]"
	case *ast.CompositeLit:
		return "a map literal"
	case *ast.ParenExpr:
		return rangeExprString(x.X)
	case *ast.StarExpr:
		return "*" + rangeExprString(x.X)
	default:
		return fmt.Sprintf("%T", e)
	}
}

// shortTypeString renders a type with bare package names, so the report says
// `haapi.TraceListResult`, not a full module path.
func shortTypeString(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}
