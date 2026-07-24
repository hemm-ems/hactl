package companion

// Field-level contract between the Go response structs and the vendored
// companion OpenAPI spec (testdata/companion-v1.yaml). This is the hactl side of
// TC-5, and the direct guard for the D45 class: a documented response field that
// no Go struct decodes — or a Go json tag the spec never documents — is
// invisible to a path/method-only contract. `contract_test.go` (companion tier)
// checks paths and methods; this checks fields, in both directions, and needs no
// Docker (it only reflects over the structs and reads the spec file).
//
// Both sweeps derive from the single companion.Endpoints table (TC-7): a new
// client call added there is auto-covered here, and a spec path with no
// Endpoints entry (or vice versa) fails TestEndpointsCoverSpecPaths loudly.

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// decodeIgnore lists spec response properties that are deliberately NOT decoded
// by any Go struct, one justification per entry, keyed "METHOD /path[.nested]:
// field". It is intentionally empty: every documented response field is decoded
// (see types.go). Whole-endpoint body-discard (the reload endpoint) is expressed
// in the Endpoints table as a nil Response, not here. Keep this empty — a
// growing ignore list defeats the contract; a new undecoded field is a finding
// to decode or to justify explicitly, not to wave through.
var decodeIgnore = map[string]string{}

func loadCompanionSpec(t *testing.T) *openapi3.T {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "testdata", "companion-v1.yaml"),
		filepath.Join("testdata", "companion-v1.yaml"),
	}
	var specPath string
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if _, statErr := os.Stat(abs); statErr == nil {
			specPath = abs
			break
		}
	}
	if specPath == "" {
		t.Fatal("companion-v1.yaml not found in testdata")
	}
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("loading spec: %v", err)
	}
	return spec
}

// operationFor returns the operation for a (pathItem, method).
func specOperation(pi *openapi3.PathItem, method string) *openapi3.Operation {
	switch method {
	case "GET":
		return pi.Get
	case "POST":
		return pi.Post
	case "PUT":
		return pi.Put
	case "DELETE":
		return pi.Delete
	default:
		return nil
	}
}

// successSchema returns the JSON schema of an operation's 2xx (200 then 201)
// success response, or nil when the operation documents no JSON success body.
func successSchema(op *openapi3.Operation) *openapi3.Schema {
	if op == nil || op.Responses == nil {
		return nil
	}
	for _, code := range []int{200, 201} {
		rr := op.Responses.Status(code)
		if rr == nil || rr.Value == nil {
			continue
		}
		mt := rr.Value.Content.Get("application/json")
		if mt == nil || mt.Schema == nil || mt.Schema.Value == nil {
			continue
		}
		return mt.Schema.Value
	}
	return nil
}

func deref(rt reflect.Type) reflect.Type {
	for rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	return rt
}

// jsonFields maps a struct type's JSON property names to the corresponding field
// types. Fields tagged `-`, unexported, or without a name are skipped.
func jsonFields(rt reflect.Type) map[string]reflect.Type {
	rt = deref(rt)
	out := map[string]reflect.Type{}
	if rt.Kind() != reflect.Struct {
		return out
	}
	for f := range rt.Fields() {
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out[name] = f.Type
	}
	return out
}

// align unwraps pointers and matching slice/array layers so a Go struct type is
// paired with an object schema. ok is false when either side bottoms out before
// a struct↔object pairing (a scalar, a map, or a `[]any` against an array of
// unstructured objects) — those carry no named fields to reconcile.
func align(rt reflect.Type, s *openapi3.Schema) (reflect.Type, *openapi3.Schema, bool) {
	rt = deref(rt)
	for rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
		if s == nil || s.Items == nil || s.Items.Value == nil {
			return rt, s, false
		}
		s = s.Items.Value
		rt = deref(rt.Elem())
	}
	if rt.Kind() != reflect.Struct || s == nil || len(s.Properties) == 0 {
		return rt, s, false
	}
	return rt, s, true
}

// collect records, per Go struct type, the union of spec property names it is
// paired with across every endpoint (directly and nested). The union is what
// lets a struct reused across two schemas — ConfigDeleteResponse decodes both
// the PUT {status,diff,reloaded} and DELETE {status,reloaded} acks — pass
// Direction 1 without a field being flagged for the endpoint whose schema omits
// it.
func collect(rt reflect.Type, s *openapi3.Schema, union map[reflect.Type]map[string]bool, visited map[[2]any]bool) {
	art, as, ok := align(rt, s)
	if !ok {
		return
	}
	key := [2]any{art, as}
	if visited[key] {
		return
	}
	visited[key] = true

	set := union[art]
	if set == nil {
		set = map[string]bool{}
		union[art] = set
	}
	for name := range as.Properties {
		set[name] = true
	}
	for name, ft := range jsonFields(art) {
		if prop, has := as.Properties[name]; has && prop.Value != nil {
			collect(ft, prop.Value, union, visited)
		}
	}
}

// endpointSchemas returns, per endpoint with a decode target, its success schema.
func endpointSchemas(t *testing.T, spec *openapi3.T) map[string]*openapi3.Schema {
	t.Helper()
	out := map[string]*openapi3.Schema{}
	for _, ep := range Endpoints {
		if ep.Response == nil {
			continue
		}
		pi := spec.Paths.Find(ep.Path)
		if pi == nil {
			t.Errorf("%s %s: path missing from spec", ep.Method, ep.Path)
			continue
		}
		op := specOperation(pi, ep.Method)
		if op == nil {
			t.Errorf("%s %s: operation missing from spec", ep.Method, ep.Path)
			continue
		}
		s := successSchema(op)
		if s == nil {
			t.Errorf("%s %s: decodes into %T but the spec documents no JSON success body",
				ep.Method, ep.Path, ep.Response)
			continue
		}
		out[ep.Method+" "+ep.Path] = s
	}
	return out
}

// TestGoStructTagsAreDocumented is Direction 1: every json tag on a companion
// response struct maps to a documented property in the spec (across the union of
// schemas the struct decodes). A struct field the spec doesn't know about is a
// field hactl would populate from a payload the companion never promises.
func TestGoStructTagsAreDocumented(t *testing.T) {
	spec := loadCompanionSpec(t)
	schemas := endpointSchemas(t, spec)

	union := map[reflect.Type]map[string]bool{}
	visited := map[[2]any]bool{}
	for _, ep := range Endpoints {
		if ep.Response == nil {
			continue
		}
		if s := schemas[ep.Method+" "+ep.Path]; s != nil {
			collect(reflect.TypeOf(ep.Response), s, union, visited)
		}
	}

	types := make([]reflect.Type, 0, len(union))
	for rt := range union {
		types = append(types, rt)
	}
	sort.Slice(types, func(i, j int) bool { return types[i].String() < types[j].String() })

	for _, rt := range types {
		documented := union[rt]
		names := make([]string, 0)
		for name := range jsonFields(rt) {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !documented[name] {
				t.Errorf("%s.%s (json:%q) is decoded by hactl but documented in no companion response schema — "+
					"the struct or the spec drifted", rt.Name(), name, name)
			}
		}
	}
}

// TestSpecResponseFieldsAreDecoded is Direction 2: every response property in the
// spec is decoded by the corresponding Go struct or is on decodeIgnore with a
// justification. An undecoded field is the D45 class: invisible to a path-only
// contract, silently absent from every command that should surface it.
func TestSpecResponseFieldsAreDecoded(t *testing.T) {
	spec := loadCompanionSpec(t)
	schemas := endpointSchemas(t, spec)

	for _, ep := range Endpoints {
		if ep.Response == nil {
			continue
		}
		s := schemas[ep.Method+" "+ep.Path]
		if s == nil {
			continue
		}
		checkDecoded(t, ep.Method+" "+ep.Path, reflect.TypeOf(ep.Response), s)
	}
}

func checkDecoded(t *testing.T, label string, rt reflect.Type, s *openapi3.Schema) {
	t.Helper()
	art, as, ok := align(rt, s)
	if !ok {
		return
	}
	fields := jsonFields(art)
	names := make([]string, 0, len(as.Properties))
	for name := range as.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ft, has := fields[name]
		if has {
			if prop := as.Properties[name]; prop != nil && prop.Value != nil {
				checkDecoded(t, label+"."+name, ft, prop.Value)
			}
			continue
		}
		if _, ignored := decodeIgnore[label+": "+name]; ignored {
			continue
		}
		t.Errorf("%s response documents field %q that no Go field decodes (D45 class); "+
			"decode it or add a justified decodeIgnore entry", label, name)
	}
}

// TestEndpointsCoverSpecPaths keeps the Endpoints table complete both ways: every
// spec path is called by the client and every client path is documented. A new
// companion route therefore forces a loud classification failure here rather than
// slipping through unreflected (TC-7).
func TestEndpointsCoverSpecPaths(t *testing.T) {
	spec := loadCompanionSpec(t)

	inTable := map[string]bool{}
	for _, ep := range Endpoints {
		inTable[ep.Path] = true
	}
	for path := range spec.Paths.Map() {
		if !inTable[path] {
			t.Errorf("spec path %q is in no companion.Endpoints entry — add the client call or classify it", path)
		}
	}
	for path := range inTable {
		if spec.Paths.Find(path) == nil {
			t.Errorf("companion.Endpoints path %q is absent from the vendored spec (run: make sync-spec)", path)
		}
	}
}
