package reqmap

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// RuleBlueprintUnexpandable refuses a candidate built from a blueprint hactl
// cannot expand (D-185); the reason follows after " — ".
const RuleBlueprintUnexpandable = "fail-closed: blueprint cannot be expanded"

// BlueprintReader reads a config file by its config-relative location.
type BlueprintReader func(location string) ([]byte, error)

// ExpandBlueprints replaces every mapping in a candidate that holds a
// `use_blueprint` key — an automation, a script, a template entity, at any
// depth — with what Home Assistant runs for it (D-185): the blueprint at
// `blueprints/<domain>/<path>`, every `!input` substituted with the given
// input or the blueprint's default, the candidate's own keys over the
// blueprint's, `use_blueprint` and `blueprint` dropped
// (homeassistant/components/blueprint/models.py, BlueprintInputs.
// async_substitute, as of 2026.11). What cannot be expanded is returned as
// undecidable, never passed: the caller refuses the write.
//
// domain is the domain being written (automation, script, template); read
// reads the blueprint file. A nil read makes every blueprint unreadable.
func ExpandBlueprints(candidate any, domain string, read BlueprintReader) (any, []CandidateUndecidable) {
	e := &bpExpander{domain: domain, read: read}
	return e.walk(candidate, ""), e.undecidable
}

type bpExpander struct {
	domain      string
	read        BlueprintReader
	undecidable []CandidateUndecidable
}

func (e *bpExpander) fail(p, why string) {
	e.undecidable = append(e.undecidable, CandidateUndecidable{Path: pathOrRoot(p), Rule: RuleBlueprintUnexpandable + " — " + why})
}

func (e *bpExpander) walk(v any, p string) any {
	switch t := v.(type) {
	case map[string]any:
		if ub, ok := t["use_blueprint"]; ok {
			out, err := e.expand(t, ub)
			if err != nil {
				e.fail(p+"/use_blueprint", err.Error())
				return t
			}
			return out
		}
		// In key order: what cannot be expanded is reported in the same order
		// on every run.
		out := make(map[string]any, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			out[k] = e.walk(t[k], p+"/"+k)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = e.walk(val, fmt.Sprintf("%s/%d", p, i))
		}
		return out
	}
	return v
}

// expand is one use_blueprint mapping.
func (e *bpExpander) expand(cfg map[string]any, ub any) (map[string]any, error) {
	if e.domain == "" {
		return nil, errors.New("the domain being written is unknown")
	}
	use, ok := ub.(map[string]any)
	if !ok {
		return nil, errors.New("use_blueprint is not a mapping")
	}
	rel, ok := use["path"].(string)
	if !ok || strings.TrimSpace(rel) == "" {
		return nil, errors.New("use_blueprint has no path")
	}
	if err := safeBlueprintPath(rel); err != nil {
		return nil, err
	}
	given := map[string]any{}
	switch in := use["input"].(type) {
	case nil:
	case map[string]any:
		given = in
	default:
		return nil, errors.New("use_blueprint input is not a mapping")
	}
	if e.read == nil {
		return nil, errors.New("blueprint files cannot be read in this invocation (no companion)")
	}
	loc := "blueprints/" + e.domain + "/" + rel
	raw, err := e.read(loc)
	if err != nil {
		return nil, fmt.Errorf("%s unreadable: %w", loc, err)
	}
	data, err := decodeBlueprint(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", loc, err)
	}
	top, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a mapping", loc)
	}
	meta, ok := top["blueprint"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s has no blueprint section", loc)
	}
	if d, _ := meta["domain"].(string); d != e.domain {
		return nil, fmt.Errorf("%s is a blueprint of domain %q, not %s", loc, d, e.domain)
	}
	declared, err := blueprintInputs(meta)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", loc, err)
	}
	values := make(map[string]any, len(declared))
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		def := declared[name]
		if v, ok := given[name]; ok {
			values[name] = v
			continue
		}
		if m, ok := def.(map[string]any); ok {
			if d, ok := m["default"]; ok {
				values[name] = d
				continue
			}
		}
		return nil, fmt.Errorf("input %s has neither a value nor a default", name)
	}
	processed, err := substituteInputs(top, values)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", loc, err)
	}
	out, _ := processed.(map[string]any)
	combined := make(map[string]any)
	maps.Copy(combined, out)
	maps.Copy(combined, cfg)
	delete(combined, "use_blueprint")
	delete(combined, "blueprint")
	return combined, nil
}

// safeBlueprintPath refuses a path that leaves the domain's blueprint folder.
func safeBlueprintPath(rel string) error {
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return fmt.Errorf("blueprint path %q is absolute or holds a backslash", rel)
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return fmt.Errorf("blueprint path %q holds an empty, . or .. segment", rel)
		}
	}
	if path.Clean(rel) != rel {
		return fmt.Errorf("blueprint path %q is not clean", rel)
	}
	return nil
}

// blueprintInputs flattens the declared inputs: an entry holding `input` is a
// section whose inputs count as the blueprint's (Blueprint.inputs). An input
// or a section with a `visible:` condition is refused — Home Assistant then
// fills a hidden input with its default by evaluating the condition, which
// hactl does not.
func blueprintInputs(meta map[string]any) (map[string]any, error) {
	out := map[string]any{}
	in, ok := meta["input"]
	if !ok || in == nil {
		return out, nil
	}
	decl, ok := in.(map[string]any)
	if !ok {
		return nil, errors.New("blueprint input is not a mapping")
	}
	visible := func(v any) bool {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		_, has := m["visible"]
		return has
	}
	for _, key := range slices.Sorted(maps.Keys(decl)) {
		v := decl[key]
		if visible(v) {
			return nil, fmt.Errorf("input %s has a visible: condition", key)
		}
		if m, ok := v.(map[string]any); ok {
			if sec, ok := m["input"]; ok {
				inner, ok := sec.(map[string]any)
				if !ok && sec != nil {
					return nil, fmt.Errorf("section %s input is not a mapping", key)
				}
				for _, name := range slices.Sorted(maps.Keys(inner)) {
					def := inner[name]
					if visible(def) {
						return nil, fmt.Errorf("input %s has a visible: condition", name)
					}
					out[name] = def
				}
				continue
			}
		}
		out[key] = v
	}
	return out, nil
}

// bpInput is a `!input <name>` node.
type bpInput struct{ name string }

// substituteInputs replaces every !input in list items and mapping values
// (never keys); one naming an undeclared input is refused (Home Assistant
// refuses such a blueprint as Missing input definition).
func substituteInputs(v any, values map[string]any) (any, error) {
	switch t := v.(type) {
	case bpInput:
		val, ok := values[t.name]
		if !ok {
			return nil, fmt.Errorf("!input %s names no declared input", t.name)
		}
		return val, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			s, err := substituteInputs(t[k], values)
			if err != nil {
				return nil, err
			}
			out[k] = s
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			s, err := substituteInputs(val, values)
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	return v, nil
}

// decodeBlueprint decodes a blueprint file, keeping `!input` as bpInput and
// refusing every other application tag (`!secret`, `!include`, …): hactl
// cannot resolve them the way Home Assistant does.
func decodeBlueprint(raw []byte) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("does not parse: %w", err)
	}
	if doc.Kind == 0 {
		return nil, errors.New("is empty")
	}
	return decodeBPNode(&doc, 0)
}

func decodeBPNode(n *yaml.Node, depth int) (any, error) {
	if depth > 200 {
		return nil, errors.New("nests too deep")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return nil, errors.New("is not one document")
		}
		return decodeBPNode(n.Content[0], depth+1)
	case yaml.AliasNode:
		return decodeBPNode(n.Alias, depth+1)
	case yaml.MappingNode:
		return decodeBPMapping(n, depth)
	case yaml.SequenceNode:
		return decodeBPSequence(n, depth)
	case yaml.ScalarNode:
		if n.Tag == "!input" {
			return bpInput{name: n.Value}, nil
		}
		if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
			return nil, fmt.Errorf("holds the tag %s", n.Tag)
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("holds a YAML node of kind %d", n.Kind)
}

func decodeBPMapping(n *yaml.Node, depth int) (any, error) {
	if n.Tag != "" && n.Tag != "!!map" {
		return nil, fmt.Errorf("holds the tag %s", n.Tag)
	}
	out := make(map[string]any, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag == "!!merge" {
			return nil, errors.New("holds a merge key or a non-scalar key")
		}
		val, err := decodeBPNode(v, depth+1)
		if err != nil {
			return nil, err
		}
		out[k.Value] = val
	}
	return out, nil
}

func decodeBPSequence(n *yaml.Node, depth int) (any, error) {
	if n.Tag != "" && n.Tag != "!!seq" {
		return nil, fmt.Errorf("holds the tag %s", n.Tag)
	}
	out := make([]any, 0, len(n.Content))
	for _, c := range n.Content {
		val, err := decodeBPNode(c, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, val)
	}
	return out, nil
}
