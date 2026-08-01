package surfaceaudit

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ---------------------------------------------------------------------------
// Shared-state surface
// ---------------------------------------------------------------------------

// destroyers are the standard-library calls that can make a file that was
// there stop being there, resolved by full name so an aliased import cannot
// slip past.
//
//   - os.WriteFile and os.Create truncate an existing file without a word.
//   - os.OpenFile does the same whenever the caller passes O_CREATE without
//     O_EXCL, and O_EXCL is exactly the distinction this surface exists to make
//     visible.
//   - os.Rename replaces its destination silently, which is how a tmp-file
//     write ends: legitimate when the file belongs to this process's own
//     bookkeeping, a lost update when two callers share it.
var destroyers = map[string]bool{
	"os.WriteFile": true,
	"os.Create":    true,
	"os.OpenFile":  true,
	"os.Rename":    true,
}

// SharedStateSurface is every function in the module's non-test sources that
// can destroy a file.
//
// Rule (INVARIANTS.md H-26): hactl is never the only caller. The instance
// directory is shared — a second terminal, a CI job, an MCP server, the
// multi-agent fleet the findings came from — so a file hactl creates to
// preserve a state it is about to replace may not overwrite one that is
// already there, and state hactl reads back may have been written by somebody
// else.
//
// The census is mechanical and the judgment is per site, because whether a
// destroyed file mattered is not a property a parser can read: `os.WriteFile`
// into a caller-named output path is the caller's business, and
// `os.WriteFile` into `<instance>/backups/` is somebody's only undo. All three
// backup writers in this module got that wrong the same way — a name at
// one-second resolution, no existence check — and each was fixed once, in the
// place it was reported, which is precisely the shape this package exists to
// stop (see the four defects in the package doc).
//
// The extractor deliberately does NOT try to decide which paths are inside an
// instance directory. That derivation would be a heuristic over string
// building, and a heuristic that misses is silent — the one failure mode this
// package treats as worse than any other. A census of every destroyer is
// bigger and honest: a new one is red until somebody says which kind it is.
func SharedStateSurface(root string) (Surface, error) {
	scanned, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}

	c := &destroyerCollector{
		root:     root,
		byKey:    map[string]*destroyerHit{},
		seenLine: map[string]bool{},
		compiled: map[string]bool{},
	}
	for _, tags := range buildTagConfigs(scanned) {
		if err := c.loadConfiguration(tags); err != nil {
			return Surface{}, err
		}
	}
	for _, f := range scanned {
		if !c.compiled[filepath.Join(root, filepath.FromSlash(f.rel))] {
			return Surface{}, fmt.Errorf(
				"%s compiles under no build configuration this sweep loads — extend buildTagConfigs before trusting the surface",
				f.rel)
		}
	}

	sort.Strings(c.order)
	s := Surface{
		Name: "sharedstate",
		Rule: "a file hactl creates to preserve a state it is about to replace never overwrites an existing one, and state another caller can have written is not read as this caller's own (H-26)",
	}
	for _, key := range c.order {
		h := c.byKey[key]
		sort.Strings(h.calls)
		h.site.Note = strings.Join(h.calls, "; ")
		s.Sites = append(s.Sites, h.site)
	}
	return s, nil
}

type destroyerHit struct {
	site  Site
	calls []string
}

type destroyerCollector struct {
	root     string
	byKey    map[string]*destroyerHit
	order    []string
	seenLine map[string]bool
	compiled map[string]bool
}

func (c *destroyerCollector) loadConfiguration(tags string) error {
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
			return fmt.Errorf("package %s does not load under tags %q: %w", p.PkgPath, tags, p.Errors[0])
		}
		for i, f := range p.Syntax {
			fname := p.CompiledGoFiles[i]
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			c.compiled[fname] = true
			c.collectFile(p, f, fname)
		}
	}
	return nil
}

func (c *destroyerCollector) collectFile(p *packages.Package, f *ast.File, fname string) {
	rel, err := filepath.Rel(c.root, fname)
	if err != nil {
		rel = fname
	}
	rel = filepath.ToSlash(rel)

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeFullName(p, call)
		if !destroyers[name] {
			return true
		}
		line := p.Fset.Position(call.Pos()).Line
		lineKey := rel + ":" + strconv.Itoa(line)
		if c.seenLine[lineKey] {
			return true // the same call, revisited under another tag configuration
		}
		c.seenLine[lineKey] = true

		fn := enclosingFunc(f, call.Pos())
		if fn == "" {
			fn = "(var initializer)"
		}
		key := rel + ":" + fn
		h, known := c.byKey[key]
		if !known {
			h = &destroyerHit{site: Site{Key: key, File: rel, Line: line}}
			c.byKey[key] = h
			c.order = append(c.order, key)
		}
		h.calls = append(h.calls, fmt.Sprintf("calls %s at line %d", name, line))
		return true
	})
}

// calleeFullName resolves a call's callee to "<package>.<Func>" using the type
// checker, so `import stdos "os"` is the same site as `os` and a local
// function that merely shares a name is not.
func calleeFullName(p *packages.Package, call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	obj, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
	if !ok || obj.Pkg() == nil {
		return ""
	}
	return obj.Pkg().Name() + "." + obj.Name()
}
