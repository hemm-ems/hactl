package reqmap

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/policy"
)

// RefChangeOwners places one change of a companion ref/replace plan in the
// typed config entries it lies in (D-77, R-3): the automation entry, script
// key or template entity whose definition the change's path runs through,
// read from the file the change lies in. content is that file's raw text
// (GET /v1/config/file?resolve=false — the tree the companion's walk rendered
// the path against); oldVal and newVal are the ref/replace pair, used to work
// out an owner's new id when the change rewrites the id itself (E-5).
//
// The answer is a deliberate superset where the file is ambiguous, never less
// (the request layer's pole): a path that matches the tree more than one way
// (a key holding `.` or `[`) gets the owners of every match. It places a
// change by the entry's shape, never by which domain key of
// configuration.yaml includes the file, so it needs no claim about Home
// Assistant's wiring:
//
//   - automation: an entry of a file's top-level list, of an `automation` (or
//     `automation <label>`) key's list, or a file that is one entry
//     (`!include_dir_list`), when the entry has an `id` and one of `trigger`,
//     `triggers`, `action`, `actions`, `use_blueprint`;
//   - script: a key of a `script` (or `script <label>`) mapping; a top-level
//     key whose value has `sequence` or `use_blueprint` (scripts.yaml); a file
//     that is one script (`!include_dir_named`: the file name is the key);
//   - template: an entity item, with a `unique_id`, of a template entity domain
//     key in a block of a file's top-level list or of a `template` key's list;
//     a change in the block outside every entity item (its trigger, condition,
//     action, variables) lies in every entity of the block.
//
// A change in none of these — an id-less automation, an entity without
// unique_id, a file of any other shape — has no typed owner and is checked as
// its file only (D-77, documented). A path that matches nothing in the tree,
// or a file that does not parse, is an error: the owners cannot be known, and
// the caller refuses the plan (fail closed) instead of checking less.
func RefChangeOwners(location string, content []byte, changePath, oldVal, newVal string) ([]TypedOwner, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("%s does not parse as YAML: %w", location, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s holds no YAML document", location)
	}
	if changePath == "" {
		return nil, errors.New("the change names no path")
	}
	root := derefNode(doc.Content[0])
	var chains [][]pathStep
	walkChangePath(root, changePath, true, nil, &chains, 0)
	if len(chains) == 0 {
		return nil, fmt.Errorf("path %q is not in %s as it reads now", changePath, location)
	}
	var out []TypedOwner
	for _, ch := range chains {
		for _, o := range ownersOf(location, root, ch, oldVal, newVal) {
			if !slices.Contains(out, o) {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

// pathStep is one segment of a matched path: the key or index taken and the node
// it led to.
type pathStep struct {
	key     string
	isIndex bool
	node    *yaml.Node
}

// maxChains bounds how many ways one path may match a tree; past it the path
// is treated as matching nothing (fail closed), never as matching some.
const maxChains = 64

// walkChangePath collects every way rest (a path rendered as the companion renders
// it: `key`, `.key`, `[n]`) walks down from n.
func walkChangePath(n *yaml.Node, rest string, first bool, acc []pathStep, out *[][]pathStep, depth int) {
	if len(*out) > maxChains || depth > 256 {
		return
	}
	n = derefNode(n)
	if rest == "" {
		*out = append(*out, slices.Clone(acc))
		return
	}
	switch n.Kind {
	case yaml.SequenceNode:
		idx, after, ok := leadingIndex(rest)
		if ok && idx < len(n.Content) {
			child := derefNode(n.Content[idx])
			walkChangePath(child, after, false, append(acc, pathStep{key: strconv.Itoa(idx), isIndex: true, node: child}), out, depth+1)
		}
	case yaml.MappingNode:
		for _, kv := range mappingPairs(n) {
			k := derefNode(kv[0])
			if k.Kind != yaml.ScalarNode {
				continue
			}
			var tokens []string
			if first {
				tokens = append(tokens, k.Value)
			} else {
				tokens = append(tokens, "."+k.Value)
			}
			if k.Tag == "!!int" {
				// The companion renders an integer key as an index.
				tokens = append(tokens, "["+k.Value+"]")
			}
			for _, tok := range tokens {
				after, ok := strings.CutPrefix(rest, tok)
				if !ok || (after != "" && after[0] != '.' && after[0] != '[') {
					continue
				}
				child := derefNode(kv[1])
				walkChangePath(child, after, false, append(acc, pathStep{key: k.Value, node: child}), out, depth+1)
			}
		}
	}
}

// leadingIndex parses a leading `[n]`.
func leadingIndex(s string) (int, string, bool) {
	if !strings.HasPrefix(s, "[") {
		return 0, "", false
	}
	end := strings.IndexByte(s, ']')
	if end < 2 {
		return 0, "", false
	}
	idx, err := strconv.Atoi(s[1:end])
	if err != nil || idx < 0 {
		return 0, "", false
	}
	return idx, s[end+1:], true
}

// derefNode follows an alias to its anchor.
func derefNode(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && n.Alias != nil && i < 64; i++ {
		n = n.Alias
	}
	return n
}

// mappingPairs returns a mapping's key/value pairs, with the pairs a `<<`
// merge key brings in after the explicit ones (an explicit key wins).
func mappingPairs(n *yaml.Node) [][2]*yaml.Node {
	var out [][2]*yaml.Node
	seen := map[string]bool{}
	var merged []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := derefNode(n.Content[i])
		if k.Kind == yaml.ScalarNode && k.Value == "<<" && k.Tag == "!!merge" {
			v := derefNode(n.Content[i+1])
			switch v.Kind {
			case yaml.MappingNode:
				merged = append(merged, v)
			case yaml.SequenceNode:
				for _, c := range v.Content {
					if c = derefNode(c); c.Kind == yaml.MappingNode {
						merged = append(merged, c)
					}
				}
			}
			continue
		}
		seen[k.Value] = true
		out = append(out, [2]*yaml.Node{n.Content[i], n.Content[i+1]})
	}
	for _, m := range merged {
		for _, kv := range mappingPairs(m) {
			if k := derefNode(kv[0]); !seen[k.Value] {
				seen[k.Value] = true
				out = append(out, kv)
			}
		}
	}
	return out
}

// yamlLookup returns a mapping's value for key, or nil.
func yamlLookup(n *yaml.Node, key string) *yaml.Node {
	n = derefNode(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for _, kv := range mappingPairs(n) {
		if k := derefNode(kv[0]); k.Kind == yaml.ScalarNode && k.Value == key {
			return derefNode(kv[1])
		}
	}
	return nil
}

// yamlScalar returns a mapping's scalar value for key.
func yamlScalar(n *yaml.Node, key string) (string, bool) {
	v := yamlLookup(n, key)
	if v == nil || v.Kind != yaml.ScalarNode || v.Tag == "!!null" || v.Value == "" {
		return "", false
	}
	return v.Value, true
}

func yamlHasAnyKey(n *yaml.Node, keys ...string) bool {
	for _, k := range keys {
		if yamlLookup(n, k) != nil {
			return true
		}
	}
	return false
}

var (
	automationKeyRe = regexp.MustCompile(`^automation( .+)?$`)
	scriptKeyRe     = regexp.MustCompile(`^script( .+)?$`)
	templateKeyRe   = regexp.MustCompile(`^template( .+)?$`)
)

// automationShaped: an automation entry carries its triggers, its actions or a
// blueprint — scenes (an `id` and `entities`) and everything else do not.
func automationShaped(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.MappingNode &&
		yamlHasAnyKey(n, "trigger", "triggers", "action", "actions", "use_blueprint")
}

// scriptShaped: a script body carries its sequence or a blueprint.
func scriptShaped(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.MappingNode && yamlHasAnyKey(n, "sequence", "use_blueprint")
}

// templateBlockShaped: a modern template block declares at least one entity
// domain key.
func templateBlockShaped(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.MappingNode && yamlHasAnyKey(n, templateEntityDomains...)
}

// chainNode is the node a chain reached after i+1 steps; -1 is the root.
func chainNode(root *yaml.Node, ch []pathStep, i int) *yaml.Node {
	if i < 0 {
		return root
	}
	return ch[i].node
}

// ownersOf places one matched chain.
func ownersOf(location string, root *yaml.Node, ch []pathStep, oldVal, newVal string) []TypedOwner {
	out := make([]TypedOwner, 0, 1)
	out = append(out, automationOwners(root, ch, oldVal, newVal)...)
	out = append(out, scriptOwners(location, root, ch)...)
	out = append(out, templateOwners(root, ch, oldVal, newVal)...)
	return out
}

func automationOwners(root *yaml.Node, ch []pathStep, oldVal, newVal string) []TypedOwner {
	entry := -2 // the step index of the entry; -1 is the root itself
	switch {
	case root.Kind == yaml.SequenceNode && len(ch) > 0:
		entry = 0
	case root.Kind == yaml.MappingNode && len(ch) > 0 && automationKeyRe.MatchString(ch[0].key):
		switch ch[0].node.Kind {
		case yaml.SequenceNode:
			if len(ch) > 1 {
				entry = 1
			}
		case yaml.MappingNode:
			entry = 0
		}
	case automationShaped(root):
		entry = -1
	}
	if entry < -1 {
		return nil
	}
	e := chainNode(root, ch, entry)
	if !automationShaped(e) {
		return nil
	}
	id, ok := yamlScalar(e, "id")
	if !ok {
		return nil // an id-less automation has no address (D-78): its file only
	}
	o := TypedOwner{Kind: policy.KindAutomation, ID: id}
	if len(ch) == entry+2 && ch[entry+1].key == "id" && !ch[entry+1].isIndex {
		o.NewID = ReplaceToken(id, oldVal, newVal)
	}
	return []TypedOwner{o}
}

func scriptOwners(location string, root *yaml.Node, ch []pathStep) []TypedOwner {
	if root.Kind != yaml.MappingNode {
		return nil
	}
	switch {
	case len(ch) > 1 && scriptKeyRe.MatchString(ch[0].key) && ch[0].node.Kind == yaml.MappingNode:
		return []TypedOwner{{Kind: policy.KindScript, ID: ch[1].key}}
	case len(ch) > 0 && !automationKeyRe.MatchString(ch[0].key) && scriptShaped(ch[0].node):
		return []TypedOwner{{Kind: policy.KindScript, ID: ch[0].key}}
	case scriptShaped(root):
		base := path.Base(location)
		return []TypedOwner{{Kind: policy.KindScript, ID: strings.TrimSuffix(base, path.Ext(base))}}
	}
	return nil
}

func templateOwners(root *yaml.Node, ch []pathStep, oldVal, newVal string) []TypedOwner {
	block := -2
	switch {
	case root.Kind == yaml.SequenceNode && len(ch) > 0:
		block = 0
	case root.Kind == yaml.MappingNode && len(ch) > 0 && templateKeyRe.MatchString(ch[0].key):
		switch ch[0].node.Kind {
		case yaml.SequenceNode:
			if len(ch) > 1 {
				block = 1
			}
		case yaml.MappingNode:
			block = 0
		}
	}
	if block < 0 {
		return nil
	}
	b := ch[block].node
	if !templateBlockShaped(b) || len(ch) <= block+1 {
		return nil
	}
	dom := ch[block+1]
	if !slices.Contains(templateEntityDomains, dom.key) {
		return blockEntities(b) // the block's shared part: every entity in it
	}
	item := -1
	switch dom.node.Kind {
	case yaml.SequenceNode:
		if len(ch) > block+2 {
			item = block + 2
		}
	case yaml.MappingNode:
		item = block + 1
	}
	if item < 0 {
		return blockEntities(b)
	}
	uid, ok := yamlScalar(ch[item].node, "unique_id")
	if !ok {
		return nil // an entity without unique_id has no address: its file only
	}
	o := TypedOwner{Kind: policy.KindTemplate, ID: uid}
	if len(ch) == item+2 && ch[item+1].key == "unique_id" && !ch[item+1].isIndex {
		o.NewID = ReplaceToken(uid, oldVal, newVal)
	}
	return []TypedOwner{o}
}

// blockEntities are the template entities a block declares that have a
// unique_id.
func blockEntities(b *yaml.Node) []TypedOwner {
	var out []TypedOwner
	for _, d := range templateEntityDomains {
		v := yamlLookup(b, d)
		if v == nil {
			continue
		}
		items := []*yaml.Node{v}
		if v.Kind == yaml.SequenceNode {
			items = v.Content
		}
		for _, it := range items {
			if uid, ok := yamlScalar(derefNode(it), "unique_id"); ok {
				out = append(out, TypedOwner{Kind: policy.KindTemplate, ID: uid})
			}
		}
	}
	return out
}

// ReplaceToken rewrites every whole-token occurrence of oldVal in s to newVal,
// as the companion's ref/replace does (`\b<old>\b` in Python, whose word
// characters are Unicode letters, digits and `_`): an occurrence counts only
// where the characters on either side are not word characters. It is how an
// owner's new id is worked out when a change rewrites the id itself (E-5).
func ReplaceToken(s, oldVal, newVal string) string {
	if oldVal == "" {
		return s
	}
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(s[i:], oldVal)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		start, end := i+j, i+j+len(oldVal)
		if boundaryBefore(s, start, oldVal) && boundaryAfter(s, end, oldVal) {
			b.WriteString(s[i:start])
			b.WriteString(newVal)
			i = end
			continue
		}
		_, size := utf8.DecodeRuneInString(s[start:])
		b.WriteString(s[i : start+size])
		i = start + size
	}
}

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// boundaryBefore: `\b` at start — the rune before and the first rune of the
// match differ in being word characters.
func boundaryBefore(s string, start int, match string) bool {
	first, _ := utf8.DecodeRuneInString(match)
	prevWord := false
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		prevWord = isWord(r)
	}
	return prevWord != isWord(first)
}

// boundaryAfter: `\b` at end.
func boundaryAfter(s string, end int, match string) bool {
	last, _ := utf8.DecodeLastRuneInString(match)
	nextWord := false
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		nextWord = isWord(r)
	}
	return nextWord != isWord(last)
}
