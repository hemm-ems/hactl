package surfaceaudit

import (
	"go/ast"
	"go/token"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// ---------------------------------------------------------------------------
// Decode surface
// ---------------------------------------------------------------------------
//
// H-7: a decode that yields nothing never renders as success. A wrong-shape
// payload does not error in Go — it decodes to a zero value, and a renderer
// prints a zero value as a plausible answer. That is how `trace/get` rendered
// every automation run as PASS for months (D1).
//
// The H-14 sweep (internal/degeneracy/sweep_test.go) derives every
// json.Unmarshal site inside degeneracy.WirePackages and forces each to call
// degeneracy.Check or carry a written reason. This surface is the closure
// around that sweep: it derives every decode site the sweep structurally
// cannot see —
//
//   - json.Unmarshal in any package *outside* WirePackages (which is where
//     internal/writer decoded the live automation config for years),
//   - json.Unmarshal *inside* a wire package in a shape the sweep cannot
//     record (a target it cannot name, in a function with no Check),
//   - yaml/xml/toml unmarshals anywhere — the sweep scans encoding/json only,
//   - json/yaml NewDecoder constructions, whose later .Decode calls no
//     text-level scan can attribute,
//   - gorilla websocket's ReadJSON, a json decode that never mentions json,
//   - a dot-import of any codec package, which strips the package qualifier
//     the other detections key on.
//
// Between the two gates there is no place in the module a decode call of
// these forms can sit unexamined. What neither can see is a codec library
// this module has never imported; that boundary is the same one the clock
// surface accepts for its layout tokens.

// codecClass says which decode family an import path provides.
type codecClass int

const (
	notACodec codecClass = iota
	// codecJSON is exactly encoding/json — the one package whose Unmarshal
	// calls the H-14 sweep scans, and therefore the only class any site may
	// be excluded from this surface for.
	codecJSON
	// codecOther is every other decoding package (yaml, xml, toml, and any
	// third-party json). The sweep never scans these, so every call site is
	// on this surface.
	codecOther
)

// codecOf classifies an import path.
func codecOf(importPath string) codecClass {
	if importPath == "encoding/json" {
		return codecJSON
	}
	base := path.Base(importPath)       // "gopkg.in/yaml.v3" → "yaml.v3"
	base, _, _ = strings.Cut(base, ".") // → "yaml"
	if base == "yaml" || base == "xml" || base == "toml" || strings.Contains(base, "json") {
		return codecOther
	}
	return notACodec
}

// unmarshalNames are the package-level decode entry points a codec exposes.
var unmarshalNames = map[string]bool{"Unmarshal": true, "UnmarshalStrict": true}

// codecImports maps a file's local import names to their codec class, and
// returns any dot-imported codec paths separately — a dot import erases the
// qualifier every other detection keys on, so it is a site in its own right.
func codecImports(f *ast.File) (byName map[string]codecClass, dotted []string) {
	byName = map[string]codecClass{}
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		class := codecOf(p)
		if class == notACodec {
			continue
		}
		local := path.Base(p)
		if cut, _, ok := strings.Cut(local, "."); ok && cut != "" {
			local = cut // gopkg.in/yaml.v3's package name is "yaml"
		}
		if imp.Name != nil {
			switch imp.Name.Name {
			case "_":
				continue
			case ".":
				dotted = append(dotted, p)
				continue
			default:
				local = imp.Name.Name
			}
		}
		byName[local] = class
	}
	return byName, dotted
}

// decodeTarget mirrors sweep_test.go's targetName: the variable a decode
// writes into, or "" when the sweep could not record the site. The mirror is
// load-bearing — a site the sweep cannot record must land here instead of
// nowhere.
func decodeTarget(arg ast.Expr) string {
	if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		arg = unary.X
	}
	switch e := arg.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if base := decodeTarget(e.X); base != "" {
			return base + "." + e.Sel.Name
		}
	}
	return ""
}

// callsDegeneracyCheck reports whether the function enclosing pos calls
// degeneracy.Check — the other way a wire-package decode is under the sweep's
// jurisdiction (the sweep skips checked functions wholesale, whatever shape
// their decode targets take).
func callsDegeneracyCheck(f *ast.File, pos token.Pos) bool {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || pos < fn.Pos() || pos > fn.End() {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, isSel := n.(*ast.SelectorExpr)
			if isSel && sel.Sel.Name == "Check" {
				if id, isID := sel.X.(*ast.Ident); isID && id.Name == "degeneracy" {
					found = true
				}
			}
			return !found
		})
		return found
	}
	return false
}

// sweepGoverned reports whether TestSweep_EveryDecodeSiteIsChecked already
// derives this call: an `json.Unmarshal(data, &target)` spelled with the
// literal qualifier "json", resolving to encoding/json, in a top-level file of
// a degeneracy.WirePackages package, that the sweep can either record (a
// nameable target) or already considers checked (the function calls Check).
func sweepGoverned(rel, localName string, class codecClass, selName string, call *ast.CallExpr, f *ast.File) bool {
	if class != codecJSON || localName != "json" || selName != "Unmarshal" || len(call.Args) != 2 {
		return false
	}
	dir := path.Dir(rel)
	governed := false
	for _, pkg := range degeneracy.WirePackages {
		if dir == pkg {
			governed = true
		}
	}
	if !governed {
		return false
	}
	return decodeTarget(call.Args[1]) != "" || callsDegeneracyCheck(f, call.Pos())
}

// DecodeSurface is every decode site in the module that the H-14 sweep cannot
// see.
//
// Rule (H-7): a decode that yields nothing never renders as success — every
// decode site is poisoned by degeneracy.Check, guarded where its payload has
// no identity to poison, or dispositioned in dev/surfaces/decode.manifest.
func DecodeSurface(root string) (Surface, error) {
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

	for _, f := range files {
		record := func(key string, pos token.Pos, why string) {
			h, ok := byKey[key]
			if !ok {
				p := f.fset.Position(pos)
				h = &hit{site: Site{Key: key, File: f.rel, Line: p.Line}}
				byKey[key] = h
				order = append(order, key)
			}
			if !slices.Contains(h.why, why) {
				h.why = append(h.why, why)
			}
		}
		decodeCalls(f, record)
	}

	sort.Strings(order)
	s := Surface{
		Name: "decode",
		Rule: "a decode that yields nothing never renders as success — every site the H-14 json sweep cannot see is poisoned, guarded, or dispositioned",
	}
	for _, key := range order {
		h := byKey[key]
		h.site.Note = strings.Join(h.why, "; ")
		s.Sites = append(s.Sites, h.site)
	}
	return s, nil
}

// decodeCalls reports every decode site in one parsed file that the H-14 sweep
// does not derive.
func decodeCalls(f srcFile, record func(key string, pos token.Pos, why string)) {
	byName, dotted := codecImports(f.ast)
	for _, p := range dotted {
		record(f.rel+":import "+p, f.ast.Pos(),
			"dot import of "+p+" strips the qualifier every decode detection keys on")
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		why := decodeCallEvidence(f, call, byName)
		if why == "" {
			return true
		}
		if fn := enclosingFunc(f.ast, call.Pos()); fn != "" {
			record(f.rel+":"+fn, call.Pos(), why)
		}
		return true
	})
}

// decodeCallEvidence says why one call is a decode site on this surface, or ""
// when it is not one (or when the H-14 sweep already derives it).
func decodeCallEvidence(f srcFile, call *ast.CallExpr, byName map[string]codecClass) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	selName := sel.Sel.Name
	if selName == "ReadJSON" {
		// gorilla/websocket's Conn.ReadJSON — a json decode that never says
		// json. Keyed like every other site, on the enclosing function.
		return "decodes a websocket frame via ReadJSON"
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	class, isCodec := byName[id.Name]
	if !isCodec {
		return ""
	}
	switch {
	case unmarshalNames[selName]:
		if sweepGoverned(f.rel, id.Name, class, selName, call, f.ast) {
			return "" // TestSweep_EveryDecodeSiteIsChecked derives this one
		}
		return "decodes via " + id.Name + "." + selName
	case selName == "NewDecoder":
		return "constructs " + id.Name + ".NewDecoder, whose Decode calls no text-level sweep can attribute"
	}
	return ""
}
