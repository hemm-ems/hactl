// Package degeneracy makes an identity-less decode impossible to mistake for an
// answer.
//
// A wrong-shape JSON payload does not produce an error in Go — it produces a
// zero value, and a renderer happily prints a zero value as a plausible answer.
// That is exactly how `trace/get` decoded every automation run into an all-zero
// struct and rendered every one of them as PASS for months, with the rendering
// function at 100% test coverage (D1). The fix for traces was to spell empty
// "UNPARSED", loudly, and have every integration-test helper grep for the
// marker — which turned tests that assert nothing into detectors for the class.
//
// This package generalises that defense to every wire record hactl decodes.
// A record declares its identity — the field(s) without which it cannot be a
// real answer, e.g. an entity with no entity_id, a manifest with no domain —
// by implementing Identified on its pointer receiver. Check walks a decoded
// value, poisons every absent identity with Marker and returns an error naming
// the wire source, so the command fails loudly instead of printing a table of
// blanks. Both the poisoned value and the error text carry the marker, so both
// the text and the --json rendering path are covered.
//
// A record whose zero value is a *legitimate* answer must NOT implement
// Identified: poisoning it would make the suite cry wolf. Which records those
// are is pinned by sweep_test.go, which derives the set of json-tagged wire
// structs and the set of decode sites from the source itself and fails on any
// it cannot account for — so a new wire struct or a new json.Unmarshal has to
// be classified before it can ship.
package degeneracy

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Marker is the literal token hactl substitutes for a decoded record's missing
// identity, and the token every degeneracy error carries. The integration
// harness greps command output — and command errors — for it, so every renderer
// that can print an identity-less record is covered by every test that runs one.
//
// analyze.UnparsedMarker is defined as this constant; the trace renderer prints
// it as the result of a trace that decoded to nothing.
const Marker = "UNPARSED"

// ErrDegenerate is wrapped by every error Check returns, so a caller that has a
// fallback path can tell "this source is unavailable" (fall back) from "this
// source answered in a shape hactl no longer understands" (do not fall back —
// falling back would silently serve a different, quieter answer, which is the
// exact failure this package exists to prevent).
var ErrDegenerate = errors.New("wire payload decoded without its identity")

// Field is one component of a record's identity: the wire field name (for the
// diagnostic) and a pointer to the decoded value (so Check can poison it).
type Field struct {
	Value *string
	Name  string
}

// Identified is implemented, on the pointer receiver, by every decoded wire
// record that has an identity.
//
// Identity returns the fields without which the record cannot be a real answer.
// Returning nil means "nothing to check for this value" — used by records whose
// identity is conditional (ValidateResult only requires an error message when
// it reports invalid).
type Identified interface {
	Identity() []Field
}

var identifiedType = reflect.TypeFor[Identified]()

// Check walks v, poisons the absent identity fields of every record it contains
// with Marker, and reports an error when it poisoned anything. source names the
// wire operation (a WS command, a REST path) so the error says which payload
// changed shape.
//
// Pass a pointer to poison in place; a non-pointer is still inspected (through
// an addressable copy) so detection never silently degrades into a no-op.
//
// An empty list is not degenerate: Check only ever looks at records that were
// actually decoded, so "no traces found" stays a legitimate answer.
func Check(source string, v any) error {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}
	if rv.Kind() != reflect.Pointer {
		tmp := reflect.New(rv.Type()).Elem()
		tmp.Set(rv)
		rv = tmp.Addr()
	}
	c := &collector{seen: map[string]int{}, missing: map[string]int{}}
	walk(rv, c, 0)
	return c.err(source)
}

// maxDepth bounds the walk. Wire payloads nest a handful of levels; the bound
// exists so a self-referential type cannot hang a command.
const maxDepth = 12

func walk(v reflect.Value, c *collector, depth int) {
	if depth > maxDepth || !v.IsValid() || !carries(v.Type()) {
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return
		}
		walk(v.Elem(), c, depth+1)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return
		}
		for i := range v.Len() {
			walk(v.Index(i), c, depth+1)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		for _, k := range v.MapKeys() {
			mv := v.MapIndex(k)
			// Map values are not addressable: inspect (and poison) a copy,
			// then write it back so the poison reaches the renderer too.
			tmp := reflect.New(mv.Type()).Elem()
			tmp.Set(mv)
			walk(tmp, c, depth+1)
			v.SetMapIndex(k, tmp)
		}
	case reflect.Struct:
		if v.CanAddr() {
			// Checked rather than forced: Implements() and the assertion could
			// only disagree if the type changed under us, but a panic inside a
			// diagnostic would turn a wire-shape warning into a crash.
			if id, ok := v.Addr().Interface().(Identified); ok {
				c.inspect(v.Type().Name(), id)
			}
		}
		for i := range v.NumField() {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			walk(v.Field(i), c, depth+1)
		}
	default:
	}
}

var (
	carriesMu    sync.Mutex
	carriesCache = map[reflect.Type]bool{}
)

// carries reports whether a value of type t can transitively contain a record
// that implements Identified. It prunes the walk — without it, Check would
// iterate every byte of a json.RawMessage and rewrite every entry of a
// map[string]string looking for records that cannot be there.
//
// An interface type always answers true: its dynamic type is unknown until the
// value is in hand.
func carries(t reflect.Type) bool {
	carriesMu.Lock()
	defer carriesMu.Unlock()
	return carriesLocked(t, map[reflect.Type]bool{})
}

func carriesLocked(t reflect.Type, visiting map[reflect.Type]bool) bool {
	if got, ok := carriesCache[t]; ok {
		return got
	}
	if visiting[t] {
		// A cycle contributes nothing on its own; the answer comes from the
		// other branches. Do not cache this provisional false.
		return false
	}
	visiting[t] = true
	defer delete(visiting, t)

	var got bool
	switch t.Kind() {
	case reflect.Interface:
		got = true
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		if t.Kind() == reflect.Map {
			got = carriesLocked(t.Key(), visiting) || carriesLocked(t.Elem(), visiting)
		} else {
			got = carriesLocked(t.Elem(), visiting)
		}
	case reflect.Struct:
		got = reflect.PointerTo(t).Implements(identifiedType)
		for i := 0; !got && i < t.NumField(); i++ {
			if !t.Field(i).IsExported() {
				continue
			}
			got = carriesLocked(t.Field(i).Type, visiting)
		}
	default:
	}
	if len(visiting) == 1 {
		carriesCache[t] = got
	}
	return got
}

type collector struct {
	seen    map[string]int // type name -> records inspected
	missing map[string]int // "Type.field" -> records with that field absent
}

func (c *collector) inspect(typeName string, id Identified) {
	fields := id.Identity()
	if len(fields) == 0 {
		return
	}
	c.seen[typeName]++
	for _, f := range fields {
		if f.Value == nil || *f.Value != "" {
			continue
		}
		*f.Value = Marker
		c.missing[typeName+"."+f.Name]++
	}
}

func (c *collector) err(source string) error {
	if len(c.missing) == 0 {
		return nil
	}
	keys := make([]string, 0, len(c.missing))
	for k := range c.missing {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		typeName, field, _ := strings.Cut(k, ".")
		// "carry no %q" was the wording until finding #38, and it claimed
		// something this check cannot observe: it compares the decoded value
		// against Go's zero value, which an absent key and a present-but-empty
		// one both produce. Saying the field was missing sent a reader hunting
		// for a renamed field in a payload that carried it on every record.
		parts = append(parts, fmt.Sprintf("%d of %d %s records decoded %q as empty",
			c.missing[k], c.seen[typeName], typeName, field))
	}
	return fmt.Errorf("%s returned %s data: %s — the payload does not match the shape hactl decodes "+
		"(a renamed or removed wire field decodes to a zero value, not to an error): %w",
		source, Marker, strings.Join(parts, "; "), ErrDegenerate)
}
