// Package jsonwalk walks and rewrites decoded JSON/YAML trees (the map[string]any
// / []any / scalar shapes produced by encoding/json or yaml unmarshalling).
//
// It exists because there is no typed struct for the arbitrary nesting under a
// Lovelace dashboard's views[].cards[]/entities[] (only the top-level Views is
// typed as []json.RawMessage), so entity-reference scanning and replacement need
// an any-based walker rather than more structs. Both this and the companion's
// Python refscan emit the same {location, path, matched_value} shape so callers
// can present one uniform result set regardless of source.
package jsonwalk

import (
	"sort"
	"strconv"
	"strings"
)

// Path identifies a location in a decoded tree. Each element is either a string
// (map key) or an int (slice index).
type Path []any

// String renders a path in dotted/bracketed form, e.g. "views[0].cards[2].entity".
func (p Path) String() string {
	var b strings.Builder
	for i, seg := range p {
		switch s := seg.(type) {
		case string:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s)
		case int:
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(s))
			b.WriteByte(']')
		}
	}
	return b.String()
}

// clone returns an independent copy of p so a visitor may retain it safely.
func (p Path) clone() Path {
	out := make(Path, len(p))
	copy(out, p)
	return out
}

// Walk traverses root depth-first, calling visit for every node — the root, each
// map value, and each slice element — with its path and value. Map keys are
// visited in sorted order so traversal is deterministic. The Path passed to
// visit is safe to retain (it is not reused across calls).
func Walk(root any, visit func(path Path, value any)) {
	walk(nil, root, visit)
}

func walk(path Path, node any, visit func(Path, any)) {
	visit(path, node)
	switch n := node.(type) {
	case map[string]any:
		for _, k := range sortedKeys(n) {
			walk(childPath(path, k), n[k], visit)
		}
	case []any:
		for i, v := range n {
			walk(childPath(path, i), v, visit)
		}
	}
}

// FindString calls visit with the path of every string leaf equal to target.
func FindString(root any, target string, visit func(path Path)) {
	Walk(root, func(p Path, v any) {
		if s, ok := v.(string); ok && s == target {
			visit(p)
		}
	})
}

// Replace returns a deep copy of root with every string leaf equal to oldValue
// replaced by newValue, along with the paths that were changed (in deterministic
// order). root is never mutated. Map keys are never rewritten — only values.
func Replace(root any, oldValue, newValue string) (result any, changed []Path) {
	result = replace(nil, root, oldValue, newValue, &changed)
	return result, changed
}

func replace(path Path, node any, oldValue, newValue string, changed *[]Path) any {
	switch n := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for _, k := range sortedKeys(n) {
			out[k] = replace(childPath(path, k), n[k], oldValue, newValue, changed)
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, v := range n {
			out[i] = replace(childPath(path, i), v, oldValue, newValue, changed)
		}
		return out
	case string:
		if n == oldValue {
			*changed = append(*changed, path.clone())
			return newValue
		}
		return n
	default:
		return node
	}
}

func childPath(path Path, seg any) Path {
	child := make(Path, len(path)+1)
	copy(child, path)
	child[len(path)] = seg
	return child
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
