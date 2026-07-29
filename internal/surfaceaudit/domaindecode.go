package surfaceaudit

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ---------------------------------------------------------------------------
// Domain-decode surface
// ---------------------------------------------------------------------------
//
// H-21: a listing decodes only the entities it lists. `/api/states` is every
// entity in the instance; a command that renders one domain must apply its own
// attribute schema only to the records it renders. `auto ls` and `script ls`
// decoded the whole payload into automationAttributes/scriptAttributes and
// filtered to `automation.`/`script.` afterwards, so a live instance
// (HA 2026.7.4) killed both commands with
//
//	parsing states: json: cannot unmarshal number -1.7525 into Go struct field
//	automationAttributes.attributes.current of type int
//
// on an entity neither command lists. Neither H-7 nor the H-14 sweep fires
// here: both govern decodes that silently yield *nothing*, and this one fails
// loudly on data it should never have read.
//
// The rule is a conjunction — a *domain-specific schema* is applied to an
// *unfiltered payload* — so the surface derives all three of its legs, and none
// of them can appear silently:
//
//   - the schemas: every struct declaring a `json:"attributes"` field whose
//     type is not a map. A map attribute bag has no opinion about any domain's
//     types and cannot collide; anything else is a schema somebody wrote for
//     one domain, and a new one is unclassified the day it appears (spec
//     acceptance criterion 4).
//   - the payloads: every function that reads the whole `/api/states` document.
//     This is the superset half.
//   - the joins: every function that hands a pointer to a domain attribute
//     schema into a call (the shape of a decode target), and every function
//     that both names a schema in its signature and unmarshals wire bytes.
//
// Every hand count this rule was written with was wrong, in both directions.
// SPEC-states-domain-decode.md §2 said "exactly two structs in the module have
// a non-map `json:"attributes"` field"; there are three, because the ordering
// fix added statesEnvelope and nobody recounted. §7 listed the immune payload
// readers as five, corrected them to six on a recount, and the fix then made
// seven. And the joins — "the class is exactly two sites — not a guess, a
// derived count" — are seven: the two listings, the two functions the fix
// factored out of them, and `auto show`, `script show` and `script apply`,
// which decode a states payload into a domain schema as well. Those last three
// are safe, but for a reason nobody had written down until this ledger asked.
//
// A function that does two of those is one site with both notes: the
// disposition is a judgment about that function, and it is made once.
//
// What the manifest carries and the parser cannot: whether the set a site
// decodes is in fact a subset of the set it renders. `runAutoShow` decodes a
// single record from `/api/states/<entity_id>` — address-scoped to the domain,
// so its decoded set and its rendered set are the same one entity — and no AST
// can tell that from `fetchAutomations` reading the whole instance. The
// extractor derives the census; the ledger holds the reasoning, site by site,
// exactly as the map-range surface does for iteration order.
//
// Like MapRangeSurface this needs go/types rather than the parser-only scan in
// scan.go: whether `&x` points at a domain schema is a property of x's type,
// which for most of these sites is declared in another function or another
// file. A syntactic guess would miss precisely the new spellings the gate
// exists to catch, and a missed site here is silent.

// attributesJSONKey is the wire key every `/api/states` record carries its
// attribute bag under. Every struct that decodes one names it.
const attributesJSONKey = "attributes"

// statesReader is the client method that returns the whole `/api/states`
// document. A function that calls it holds every entity in the instance.
const statesReader = "GetStates"

// DomainDecodeSurface is every place a domain-specific attribute schema can
// meet a Home Assistant states payload.
//
// Rule (INVARIANTS.md H-21): the set of entities whose attributes a command
// decodes into a domain-specific schema is a subset of the set it renders.
func DomainDecodeSurface(root string) (Surface, error) {
	files, modulePkgs, err := loadTypedFiles(root)
	if err != nil {
		return Surface{}, err
	}

	c := &domainDecodeCollector{
		byKey:      map[string]*domainDecodeHit{},
		schemas:    map[string]bool{},
		modulePkgs: modulePkgs,
	}
	for _, f := range files {
		c.collectSchemas(f)
	}
	if c.err != nil {
		return Surface{}, c.err
	}
	// An extractor that has stopped matching passes forever while proving
	// nothing. Every /api/states record carries its attributes under this key,
	// so a module that decodes states at all declares the tag somewhere.
	if c.attributeFields == 0 {
		return Surface{}, fmt.Errorf(
			"no struct in the module declares a `json:%q` field — the states record shape has changed "+
				"and this extractor no longer matches the code it audits", attributesJSONKey)
	}
	for _, f := range files {
		c.collectFunctions(f)
	}
	if c.err != nil {
		return Surface{}, c.err
	}

	sort.Strings(c.order)
	s := Surface{
		Name: "domaindecode",
		Rule: "a domain-specific attribute schema is applied only to the entities the command renders — " +
			"the set a listing decodes is a subset of the set it lists",
	}
	for _, key := range c.order {
		h := c.byKey[key]
		sort.Strings(h.why)
		h.site.Note = strings.Join(h.why, "; ")
		s.Sites = append(s.Sites, h.site)
	}
	return s, nil
}

// domainDecodeHit is one site's evidence, merged across the three legs.
type domainDecodeHit struct {
	site Site
	why  []string
}

// domainDecodeCollector accumulates the surface in two passes: the schemas
// first, because the join detection is defined against them.
type domainDecodeCollector struct {
	byKey map[string]*domainDecodeHit
	order []string
	// schemas holds the fully qualified type strings of the domain attribute
	// schemas — both the carriers and the attribute structs they nest.
	schemas map[string]bool
	// modulePkgs is every import path this module compiles, so a field type
	// declared outside it (json.RawMessage) is never mistaken for a schema
	// somebody here wrote for one domain.
	modulePkgs map[string]bool
	// attributeFields counts structs declaring a json:"attributes" field, map
	// or not — the extractor's own liveness check.
	attributeFields int
	err             error
}

// record merges one piece of evidence into a site.
func (c *domainDecodeCollector) record(key, rel string, line int, why string) {
	h, known := c.byKey[key]
	if !known {
		h = &domainDecodeHit{site: Site{Key: key, File: rel, Line: line}}
		c.byKey[key] = h
		c.order = append(c.order, key)
	}
	if !slices.Contains(h.why, why) {
		h.why = append(h.why, why)
	}
}

// collectSchemas records every struct that decodes an attribute bag into
// something other than a map, and remembers the types involved so the join
// detection can recognise them.
func (c *domainDecodeCollector) collectSchemas(f typedFile) {
	for _, st := range attributeStructs(f.file) {
		for _, fld := range st.node.Fields.List {
			if jsonTagName(fld.Tag) != attributesJSONKey {
				continue
			}
			c.attributeFields++
			t := f.pkg.TypesInfo.Types[fld.Type].Type
			if t == nil {
				c.err = fmt.Errorf("%s: no type information for the %q field of %s — "+
					"a site classified by absence is the one failure mode this package cannot tolerate",
					f.rel, attributesJSONKey, st.display())
				return
			}
			if _, isMap := t.Underlying().(*types.Map); isMap {
				// A map attribute bag holds whatever the instance sent. It
				// describes no domain, so no entity of any domain can fail it,
				// and the ordering rule has nothing to reach here.
				continue
			}
			c.recordSchema(f, st, fld, t)
		}
	}
}

// recordSchema adds one non-map attribute schema as a site.
//
// The field's type is part of the key on purpose. A carrier whose attributes
// change from json.RawMessage to a domain struct is a different site, and a
// ledger keyed on the struct name alone would carry its old reason forward as
// if it still described the code — the "harmonised toward the wrong sibling"
// failure this package exists to catch.
func (c *domainDecodeCollector) recordSchema(f typedFile, st attributeStruct, fld *ast.Field, t types.Type) {
	line := f.pkg.Fset.Position(fld.Pos()).Line
	key := fmt.Sprintf("%s:%s.%s %s", f.rel, st.display(), attributesJSONKey, shortTypeString(t))
	c.record(key, f.rel, line, fmt.Sprintf(
		"declares the %q bag as %s rather than a map of whatever the instance sent",
		attributesJSONKey, shortTypeString(t)))

	// The carrier and the attribute struct are both decode targets: a listing
	// decodes []carrier, and the ordering fix decodes carrier.Attributes on its
	// own. Both are recognised as domain schemas by the join detection.
	if st.spec != nil {
		if def := f.pkg.TypesInfo.Defs[st.spec.Name]; def != nil {
			c.schemas[types.TypeString(def.Type(), nil)] = true
		}
	}
	if named, ok := t.(*types.Named); ok && named.Obj().Pkg() != nil && c.modulePkgs[named.Obj().Pkg().Path()] {
		c.schemas[types.TypeString(t, nil)] = true
	}
}

// collectFunctions records the payload readers and the joins.
func (c *domainDecodeCollector) collectFunctions(f typedFile) {
	for _, decl := range f.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		c.collectCalls(f, fn)
		c.collectSignatureDecode(f, fn)
	}
}

// collectCalls records the two per-call legs: a read of the whole states
// document, and a pointer to a domain attribute schema handed into a call —
// which is the shape of a decode target, and the only shape through which a
// callee can write one.
func (c *domainDecodeCollector) collectCalls(f typedFile, fn *ast.FuncDecl) {
	key := f.rel + ":" + funcDeclKey(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		line := f.pkg.Fset.Position(call.Pos()).Line
		if method := statesReadCall(f, call); method != "" {
			c.record(key, f.rel, line, "reads the whole /api/states document via "+method)
		}
		for _, arg := range call.Args {
			schema := c.pointedSchema(f, arg)
			if schema == "" {
				continue
			}
			c.record(key, f.rel, line, fmt.Sprintf("hands a pointer to %s into %s",
				schema, rangeExprString(call.Fun)))
		}
		return true
	})
}

// collectSignatureDecode records a function that names a domain attribute
// schema in its own signature and unmarshals wire bytes in its body.
//
// This is the leg that catches the shared machinery. decodeStateAttributes
// takes a statesEnvelope and an `any`, so no schema ever appears in its call
// arguments as a typed pointer — and it is where the ordering fix concentrated
// the schema application for every domain listing.
func (c *domainDecodeCollector) collectSignatureDecode(f typedFile, fn *ast.FuncDecl) {
	schema := c.signatureSchema(f, fn)
	if schema == "" || !bodyDecodesWire(f.file, fn.Body) {
		return
	}
	key := f.rel + ":" + funcDeclKey(fn)
	c.record(key, f.rel, f.pkg.Fset.Position(fn.Pos()).Line,
		"unmarshals wire bytes and declares "+schema+" in its own signature")
}

// signatureSchema names the first domain attribute schema a function's
// parameters or results mention, or "".
func (c *domainDecodeCollector) signatureSchema(f typedFile, fn *ast.FuncDecl) string {
	var fields []*ast.Field
	if fn.Type.Params != nil {
		fields = append(fields, fn.Type.Params.List...)
	}
	if fn.Type.Results != nil {
		fields = append(fields, fn.Type.Results.List...)
	}
	for _, fld := range fields {
		t := f.pkg.TypesInfo.Types[fld.Type].Type
		if t == nil {
			continue
		}
		if name := c.schemaName(baseType(t)); name != "" {
			return name
		}
	}
	return ""
}

// pointedSchema names the domain attribute schema an expression points at, or
// "" when it points at nothing on this surface.
//
// The pointer requirement is what separates a decode target from a rendered
// value. `automationTraceKey(a automationEntity)` and `buildAutoRows(autos
// []automationEntity)` receive entities that are already decoded and cannot
// re-impose a schema on anything; `json.Unmarshal(data, &all)` and
// `decodeStateAttributes(s, &a.Attributes)` can, and do.
func (c *domainDecodeCollector) pointedSchema(f typedFile, arg ast.Expr) string {
	t := f.pkg.TypesInfo.Types[arg].Type
	if t == nil {
		return ""
	}
	ptr, isPtr := t.Underlying().(*types.Pointer)
	if !isPtr {
		return ""
	}
	return c.schemaName(baseType(ptr.Elem()))
}

// schemaName reports the qualified type as a short name when it is a domain
// attribute schema, and "" otherwise.
func (c *domainDecodeCollector) schemaName(t types.Type) string {
	if t == nil || !c.schemas[types.TypeString(t, nil)] {
		return ""
	}
	return shortTypeString(t)
}

// baseType strips the containers a decode target is wrapped in, so that
// `*[]statesEnvelope` and `*automationAttributes` both reduce to the schema
// they carry.
func baseType(t types.Type) types.Type {
	for {
		switch x := t.(type) {
		case *types.Pointer:
			t = x.Elem()
		case *types.Slice:
			t = x.Elem()
		case *types.Array:
			t = x.Elem()
		default:
			return t
		}
	}
}

// statesReadCall names the method when a call reads the whole states document.
func statesReadCall(f typedFile, call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != statesReader {
		return ""
	}
	fn, ok := f.pkg.TypesInfo.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Signature().Recv() == nil {
		return ""
	}
	return "(" + shortTypeString(fn.Signature().Recv().Type()) + ")." + fn.Name()
}

// bodyDecodesWire reports whether a function body unmarshals encoded bytes.
func bodyDecodesWire(file *ast.File, body *ast.BlockStmt) bool {
	byName, dotted := codecImports(file)
	if len(dotted) > 0 {
		// A dot import erases the qualifier this detection keys on. The decode
		// surface treats that as a site of its own; here it means the file's
		// functions cannot be cleared, so none of them is.
		return true
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "ReadJSON" {
			found = true
			return false
		}
		id, isID := sel.X.(*ast.Ident)
		if !isID {
			return true
		}
		if _, isCodec := byName[id.Name]; isCodec && (unmarshalNames[sel.Sel.Name] || sel.Sel.Name == "Decode") {
			found = true
			return false
		}
		return true
	})
	return found
}

// funcDeclKey names a function the way enclosingFunc does, receiver included,
// so a site key reads the same whichever leg derived it.
func funcDeclKey(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return "(" + typeName(fn.Recv.List[0].Type) + ")." + fn.Name.Name
	}
	return fn.Name.Name
}

// attributeStruct is one struct literal in the source, with the type name it
// was declared under when it has one.
type attributeStruct struct {
	node *ast.StructType
	spec *ast.TypeSpec // nil for an anonymous struct
	line int
}

// display names the struct for a site key.
func (a attributeStruct) display() string {
	if a.spec != nil {
		return a.spec.Name.Name
	}
	return "anonymous struct at line " + strconv.Itoa(a.line)
}

// attributeStructs returns every struct type in a file, named or not.
//
// Anonymous structs are included deliberately. A `var x struct{ Attributes
// someSchema \`json:"attributes"\` }` declared inside a function decodes a
// states record exactly as a named type does, and a scan that only walked
// TypeSpecs would classify it by absence.
func attributeStructs(f *ast.File) []attributeStruct {
	named := map[*ast.StructType]*ast.TypeSpec{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if st, isStruct := ts.Type.(*ast.StructType); isStruct {
			named[st] = ts
		}
		return true
	})

	var out []attributeStruct
	ast.Inspect(f, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok {
			return true
		}
		out = append(out, attributeStruct{node: st, spec: named[st]})
		return true
	})
	return out
}

// jsonTagName reads a struct tag's json name, or "".
func jsonTagName(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	unquoted, err := strconv.Unquote(tag.Value)
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(reflect.StructTag(unquoted).Get("json"), ",")
	return name
}

// ---------------------------------------------------------------------------
// Typed loading
// ---------------------------------------------------------------------------

// typedFile is one non-test source file with the type information for it.
type typedFile struct {
	pkg  *packages.Package
	file *ast.File
	rel  string
}

// loadTypedFiles type-checks the module under every build configuration its
// sources declare and returns each non-test file once, together with the set of
// import paths the module itself compiles.
//
// The tag handling mirrors MapRangeSurface's, including the cross-check against
// the tag-blind parser scan: a file that compiles under no configuration this
// sweep loads would contribute no sites and look identical to a file with none.
func loadTypedFiles(root string) ([]typedFile, map[string]bool, error) {
	scanned, err := scanSources(root)
	if err != nil {
		return nil, nil, err
	}

	seen := map[string]bool{}
	modulePkgs := map[string]bool{}
	var out []typedFile
	for _, tags := range buildTagConfigs(scanned) {
		loaded, loadErr := loadOneConfiguration(root, tags, seen, modulePkgs)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		out = append(out, loaded...)
	}

	for _, f := range scanned {
		if !seen[filepath.Join(root, filepath.FromSlash(f.rel))] {
			return nil, nil, fmt.Errorf(
				"%s compiles under no build configuration this sweep loads — extend buildTagConfigs before trusting the surface",
				f.rel)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, modulePkgs, nil
}

// loadOneConfiguration type-checks the module under one tag set.
func loadOneConfiguration(root, tags string, seen, modulePkgs map[string]bool) ([]typedFile, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedCompiledGoFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: root,
	}
	if tags != "" {
		cfg.BuildFlags = []string{"-tags=" + tags}
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("loading packages (tags %q): %w", tags, err)
	}

	var out []typedFile
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			// An unloadable package reports no sites, and a shrunken census
			// passes its gate while proving nothing.
			return nil, fmt.Errorf("package %s does not load under tags %q: %w", p.PkgPath, tags, p.Errors[0])
		}
		modulePkgs[p.PkgPath] = true
		for i, f := range p.Syntax {
			fname := p.CompiledGoFiles[i]
			if strings.HasSuffix(fname, "_test.go") || seen[fname] {
				continue
			}
			seen[fname] = true
			rel, relErr := filepath.Rel(root, fname)
			if relErr != nil {
				rel = fname
			}
			out = append(out, typedFile{pkg: p, file: f, rel: filepath.ToSlash(rel)})
		}
	}
	return out, nil
}
