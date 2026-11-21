package reqmap

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
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
// A path that walks through a node the file shares — a YAML anchor that an
// alias or a `<<` merge key elsewhere refers to — changes every place that
// shares it: the companion rewrites the anchored node once, and every entry
// holding an alias of it reads the new value (D-104). So the change is placed
// in every typed entry from which the changed node can be reached, aliases
// and merges followed; a file whose aliases make that set too large to walk
// is an error (fail closed), never "the anchor's entry only".
//
// A change in none of these — an id-less automation, an entity without
// unique_id, a file of any other shape — has no typed owner here and is
// checked as its file; a file that other files `!include` is placed in their
// entries too, by [RefChangeIncluderOwners]. A path that matches nothing in
// the tree, or a file that does not parse, is an error: the owners cannot be
// known, and the caller refuses the plan (fail closed) instead of checking
// less.
func RefChangeOwners(location string, content []byte, changePath, oldVal, newVal string) ([]TypedOwner, error) {
	root, chains, err := matchChange(location, content, changePath)
	if err != nil {
		return nil, err
	}
	var out []TypedOwner
	if err := placeChains(location, root, chains, oldVal, newVal, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// matchChange parses a file and matches a change's path in it: the root and
// every way the path walks it. No match is an error.
func matchChange(location string, content []byte, changePath string) (*yaml.Node, [][]pathStep, error) {
	root, err := parseRoot(location, content)
	if err != nil {
		return nil, nil, err
	}
	if changePath == "" {
		return nil, nil, errors.New("the change names no path")
	}
	var chains [][]pathStep
	walkChangePath(root, changePath, true, nil, &chains, 0)
	if len(chains) == 0 {
		return nil, nil, fmt.Errorf("path %q is not in %s as it reads now", changePath, location)
	}
	return root, chains, nil
}

// parseRoot parses a file's one YAML document and returns its root node.
func parseRoot(location string, content []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("%s does not parse as YAML: %w", location, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s holds no YAML document", location)
	}
	return derefNode(doc.Content[0]), nil
}

// placeChains adds to out the owners of every matched chain and — when the
// file shares nodes through anchors — of every other way the changed node is
// reached (D-104).
func placeChains(location string, root *yaml.Node, chains [][]pathStep, oldVal, newVal string, out *[]TypedOwner) error {
	shared := treeHasAlias(root, 0)
	for _, ch := range chains {
		all := [][]pathStep{ch}
		if shared && len(ch) > 0 {
			alt, err := chainsTo(root, ch[len(ch)-1].node)
			if err != nil {
				return fmt.Errorf("%s: %w", location, err)
			}
			all = append(all, alt...)
		}
		for _, c := range all {
			for _, o := range ownersOf(location, root, c, oldVal, newVal) {
				if !slices.Contains(*out, o) {
					*out = append(*out, o)
				}
			}
		}
	}
	return nil
}

// treeHasAlias reports whether n's tree holds an alias (a `*x`, or a `<<: *x`
// merge) — the only way two places of one file share a node.
func treeHasAlias(n *yaml.Node, depth int) bool {
	if n == nil || depth > maxTreeDepth {
		return n != nil // too deep to tell: treat it as shared (fail closed)
	}
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if treeHasAlias(c, depth+1) {
			return true
		}
	}
	return false
}

const (
	// maxTreeDepth bounds every walk of a file's tree.
	maxTreeDepth = 256
	// maxSharedChains bounds the ways one node may be reached through
	// aliases and includes; past it the change cannot be placed (fail
	// closed). An anchor shared by thousands of entries is the limit.
	maxSharedChains = 4096
	// maxSharedVisits bounds the nodes one such walk visits, so a file of
	// nested aliases (a "billion laughs") fails instead of hanging.
	maxSharedVisits = 1 << 20
)

// errTooShared: the changed node is reached too many ways to place.
var errTooShared = errors.New("the changed node is shared too many ways (anchors, aliases, merge keys) to work out every entry it lies in")

// chainsTo returns every path from root to target, following aliases and
// `<<` merges as the companion's loaded tree does — every place the node
// shows up.
func chainsTo(root, target *yaml.Node) ([][]pathStep, error) {
	w := &sharedWalk{target: target}
	if err := w.walk(root, nil, 0); err != nil {
		return nil, err
	}
	return w.out, nil
}

// sharedWalk is one chainsTo walk: its target, what it found, and its budget.
type sharedWalk struct {
	target *yaml.Node
	out    [][]pathStep
	visits int
}

func (w *sharedWalk) walk(n *yaml.Node, acc []pathStep, depth int) error {
	w.visits++
	if w.visits > maxSharedVisits || depth > maxTreeDepth {
		return errTooShared
	}
	n = derefNode(n)
	switch n {
	case nil:
		return nil
	case w.target:
		if len(w.out) >= maxSharedChains {
			return errTooShared
		}
		w.out = append(w.out, slices.Clone(acc))
		return nil
	}
	for _, step := range childSteps(n) {
		if err := w.walk(step.node, append(acc, step), depth+1); err != nil {
			return err
		}
	}
	return nil
}

// childSteps are a node's children as path steps — a sequence's items by
// index, a mapping's scalar keys (merged keys included) — each dereferenced.
func childSteps(n *yaml.Node) []pathStep {
	var out []pathStep
	switch n.Kind {
	case yaml.SequenceNode:
		for i, c := range n.Content {
			out = append(out, pathStep{key: strconv.Itoa(i), isIndex: true, node: derefNode(c)})
		}
	case yaml.MappingNode:
		for _, kv := range mappingPairs(n) {
			if k := derefNode(kv[0]); k.Kind == yaml.ScalarNode {
				out = append(out, pathStep{key: k.Value, node: derefNode(kv[1])})
			}
		}
	}
	return out
}

// --- changes in an included file (D-105) ------------------------------------

// includeTags are the include tags Home Assistant's loader resolves.
const (
	tagInclude              = "!include"
	tagIncludeDirList       = "!include_dir_list"
	tagIncludeDirNamed      = "!include_dir_named"
	tagIncludeDirMergeList  = "!include_dir_merge_list"
	tagIncludeDirMergeNamed = "!include_dir_merge_named"
)

// maxIncludeDepth bounds how many includes deep a change is followed up.
const maxIncludeDepth = 16

// RefChangeIncluderOwners places one change of a ref/replace plan in the typed
// entries of the files that include the file it lies in (D-105): a trigger
// list in `common/temp_trigger.yaml` that `- id: prod_heating … trigger:
// !include common/temp_trigger.yaml` pulls in lies in automation prod_heating,
// although the fragment itself has no automation's shape. files holds the raw
// text of the instance's config files, by companion-relative path (GET
// /v1/config/files, each read with resolve=false); location's own entry is
// ignored. Every `!include`, `!include_dir_list`, `!include_dir_named`,
// `!include_dir_merge_list` and `!include_dir_merge_named` that resolves to
// the file (a path relative to the including file's directory, a directory at
// any depth above it) is followed, up through every file that includes the
// includer in turn, and the change is placed by [RefChangeOwners]'s shapes in
// each including file.
//
// It is an error — the caller refuses (fail closed) — when the change's path
// is not in the file, or when a file that may include it does not parse, or
// the includes nest deeper than any real configuration.
func RefChangeIncluderOwners(location string, content []byte, changePath, oldVal, newVal string, files map[string][]byte) ([]TypedOwner, error) {
	root, chains, err := matchChange(location, content, changePath)
	if err != nil {
		return nil, err
	}
	var targets []*yaml.Node
	for _, ch := range chains {
		if t := ch[len(ch)-1].node; !slices.Contains(targets, t) {
			targets = append(targets, t)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []TypedOwner
	for _, t := range targets {
		up := includeWalk{files: files, names: names, oldVal: oldVal, newVal: newVal, out: &out}
		if err := up.place(path.Clean(location), root, t, map[string]bool{path.Clean(location): true}, 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type includeWalk struct {
	files          map[string][]byte
	names          []string
	oldVal, newVal string
	out            *[]TypedOwner
}

// place finds every file that includes loc (whose tree is locRoot), places
// target in its entries with loc's tree spliced in at each include, and
// follows that file's own includers.
func (w includeWalk) place(loc string, locRoot, target *yaml.Node, visited map[string]bool, depth int) error {
	if depth >= maxIncludeDepth {
		return fmt.Errorf("%s is included more than %d files deep", loc, maxIncludeDepth)
	}
	for _, name := range w.names {
		g := path.Clean(name)
		if visited[g] {
			continue
		}
		data := w.files[name]
		if !mayInclude(data, loc) {
			continue
		}
		gRoot, err := parseRoot(g, data)
		if err != nil {
			return fmt.Errorf("%s may include %s but does not parse: %w", g, loc, err)
		}
		if spliceIncludes(g, gRoot, loc, locRoot, 0) == 0 {
			continue
		}
		chains, err := chainsTo(gRoot, target)
		if err != nil {
			return fmt.Errorf("%s: %w", g, err)
		}
		if err := placeChains(g, gRoot, chains, w.oldVal, w.newVal, w.out); err != nil {
			return err
		}
		next := maps.Clone(visited)
		next[g] = true
		if err := w.place(g, gRoot, target, next, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// mayInclude is a cheap text filter: a file that names no include tag, or
// that names neither loc's file name nor a directory include, cannot include
// loc.
func mayInclude(data []byte, loc string) bool {
	if !bytes.Contains(data, []byte(tagInclude)) {
		return false
	}
	return bytes.Contains(data, []byte(path.Base(loc))) || bytes.Contains(data, []byte("!include_dir_"))
}

// spliceIncludes replaces, in g's tree, every include that resolves to loc by
// loc's tree (as Home Assistant's loader does), and returns how many it
// replaced. An alias node is never entered: its anchor is replaced where it is
// defined, which every alias then shares.
func spliceIncludes(g string, n *yaml.Node, loc string, locRoot *yaml.Node, depth int) int {
	if n == nil || depth > maxTreeDepth || n.Kind == yaml.AliasNode {
		return 0
	}
	if n.Kind == yaml.ScalarNode {
		if spliceInclude(g, n, loc, locRoot) {
			return 1
		}
		return 0
	}
	count := 0
	for _, c := range n.Content {
		count += spliceIncludes(g, c, loc, locRoot, depth+1)
	}
	return count
}

func spliceInclude(g string, n *yaml.Node, loc string, locRoot *yaml.Node) bool {
	ref := path.Clean(path.Join(path.Dir(g), strings.TrimSpace(n.Value)))
	alias := func() *yaml.Node { return &yaml.Node{Kind: yaml.AliasNode, Alias: locRoot} }
	switch n.Tag {
	case tagInclude:
		if ref != loc {
			return false
		}
		*n = *alias()
	case tagIncludeDirList, tagIncludeDirNamed, tagIncludeDirMergeList, tagIncludeDirMergeNamed:
		if !strings.HasPrefix(loc, ref+"/") || (path.Ext(loc) != ".yaml" && path.Ext(loc) != ".yml") {
			return false
		}
		switch n.Tag {
		case tagIncludeDirList:
			*n = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{alias()}}
		case tagIncludeDirNamed:
			base := path.Base(loc)
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSuffix(base, path.Ext(base))}
			*n = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{key, alias()}}
		case tagIncludeDirMergeList:
			if locRoot.Kind != yaml.SequenceNode {
				return false
			}
			*n = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: locRoot.Content}
		case tagIncludeDirMergeNamed:
			if locRoot.Kind != yaml.MappingNode {
				return false
			}
			*n = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: locRoot.Content}
		}
	default:
		return false
	}
	return true
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
