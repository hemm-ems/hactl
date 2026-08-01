package surfaceaudit

import (
	"go/ast"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Attributed surface
// ---------------------------------------------------------------------------

// AttributedSurface is every listing-row field filled from a Go constant
// instead of from the instance.
//
// Rule (INVARIANTS.md H-28): a field that describes the OBJECT is read from the
// wire. A constant in that position states a property of the code path that
// built the row, and a reader has no way to tell the two apart.
//
// # The finding
//
// `helper ls` merges two reads: the companion's per-domain YAML files, then
// every remaining helper-domain entity in /api/states. The second branch set
// `Source: "storage"` on every row it produced — so the column did not say
// where a helper is defined, it said which branch had found it. Those agree
// only on a tidy instance. A helper domain written inline in
// configuration.yaml is in no `<domain>.yaml`, so the companion returns nothing
// for it, every helper falls to the second branch, and all 222 helpers on the
// reference instance were reported as created in the Home Assistant UI — 42 of
// them wrongly (finding #104). Home Assistant had answered the question in the
// same payload: `editable` is true for a storage collection and false for a
// YAML one.
//
// # Why a violation surface rather than a census
//
// The sibling surfaces (clock, confirm, target) list every place a rule
// reaches, and emptiness there means the extractor has stopped matching. This
// one lists CONSTANTS IN A DESCRIPTIVE POSITION, which is the violation itself,
// so zero is the goal — the same shape as result.manifest. AllowEmpty says so,
// and TestAttributedExtractorFlagsAnInventedField guards the extractor against
// silently matching nothing by feeding it a known-bad literal.
//
// # What counts as a site
//
// A composite literal of a row type — the repo's convention is a type name
// ending in `Row`, one per listing — with a field set to a string constant.
// That is deliberately syntactic, like boolcell's renderer list: the question
// "is this value derived from the wire" is not decidable from the AST, but
// "was it typed into the source" is, and every invented value has that shape.
//
// Empty-string constants are not sites. `Icon: ""` is the absence of a value,
// which is the honest answer this law asks for, not a claim about the object.
//
// A NAMED constant counts too, and that is not a refinement — it is the whole
// difference between a gate and a decoration. Naming the literal is the first
// thing anyone does when they tidy this code: `Source: "yaml"` becomes
// `Source: helperSourceYAML`, the value is identical, and a rule that matched
// only string literals would go quietly to zero sites and stay there. So the
// declared string constants are collected first and an identifier naming one is
// the same site as the literal it replaced.
func AttributedSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name:       "attributed",
		Rule:       "a listing-row field describing the object is read from the wire, never assigned a constant by the branch that built the row",
		AllowEmpty: true,
	}
	constants := declaredStringConstants(files)
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, isLit := n.(*ast.CompositeLit)
				if !isLit {
					return true
				}
				rowType, isRow := rowTypeName(lit.Type)
				if !isRow {
					return true
				}
				for _, field := range constantStringFields(lit, constants) {
					s.Sites = append(s.Sites, Site{
						Key:  f.rel + ":" + rowType + "." + field.name,
						File: f.rel,
						Line: f.fset.Position(field.pos.Pos()).Line,
						Note: "assigns the constant " + strconv.Quote(field.value),
					})
				}
				return true
			})
		}
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}

// rowTypeName reports whether a composite literal builds a listing row, and
// under what name.
//
// The convention is the type name's `Row` suffix, which every listing in this
// repository already follows (helperRow, autoRow, scriptRow, refRow,
// energySourceRow). Following the suffix rather than a list of type names is
// what makes the surface reach the listing somebody adds next.
func rowTypeName(e ast.Expr) (string, bool) {
	var name string
	switch t := e.(type) {
	case *ast.Ident:
		name = t.Name
	case *ast.SelectorExpr:
		name = t.Sel.Name
	default:
		return "", false
	}
	if !strings.HasSuffix(name, "Row") || name == "Row" {
		return "", false
	}
	return name, true
}

type constantField struct {
	name  string
	value string
	pos   ast.Node
}

// declaredStringConstants collects every `const name = "value"` in the tree,
// so an identifier standing in for a literal is recognised as one.
func declaredStringConstants(files []srcFile) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if ok && gen.Tok == token.CONST {
				collectStringConsts(gen, out)
			}
		}
	}
	return out
}

// collectStringConsts records the `name = "value"` pairs of one const block.
func collectStringConsts(gen *ast.GenDecl, out map[string]string) {
	for _, spec := range gen.Specs {
		vs, isValue := spec.(*ast.ValueSpec)
		if !isValue || len(vs.Names) != len(vs.Values) {
			continue
		}
		for i, name := range vs.Names {
			basic, isBasic := vs.Values[i].(*ast.BasicLit)
			if !isBasic || basic.Kind != token.STRING {
				continue
			}
			if value, err := strconv.Unquote(basic.Value); err == nil && value != "" {
				out[name.Name] = value
			}
		}
	}
}

// constantValue reads a string constant out of an expression, whether it was
// written as a literal or as the name of one.
func constantValue(e ast.Expr, constants map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(v.Value)
		return value, err == nil
	case *ast.Ident:
		value, declared := constants[v.Name]
		return value, declared
	default:
		return "", false
	}
}

// constantStringFields returns the keyed fields of lit whose value is a string
// constant typed into the source — a literal, or an identifier naming one.
func constantStringFields(lit *ast.CompositeLit, constants map[string]string) []constantField {
	var out []constantField
	for _, elt := range lit.Elts {
		kv, isKV := elt.(*ast.KeyValueExpr)
		if !isKV {
			continue
		}
		key, isIdent := kv.Key.(*ast.Ident)
		if !isIdent {
			continue
		}
		value, isConst := constantValue(kv.Value, constants)
		if !isConst || value == "" {
			// An empty constant is the absence of a value, which is what this
			// law asks a site to say when the instance did not answer.
			continue
		}
		out = append(out, constantField{name: key.Name, value: value, pos: kv})
	}
	return out
}
