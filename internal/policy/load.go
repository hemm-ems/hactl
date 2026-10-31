package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// wrongName is the extension a policy saved under the wrong name carries.
const wrongName = "policy.yml"

func load(instanceDir string) (*Policy, error) {
	path := filepath.Join(instanceDir, FileName)
	ymlPath := filepath.Join(instanceDir, wrongName)

	ymlExists, err := regularFileExists(ymlPath)
	if err != nil {
		return nil, err
	}
	yamlExists, err := regularFileExists(path)
	if err != nil {
		return nil, err
	}

	if !yamlExists {
		if ymlExists {
			return nil, &LoadError{File: ymlPath, Problems: []Problem{{
				Message: "hactl reads only " + FileName + ": a policy saved as " + wrongName +
					" is not read — rename it to " + FileName,
			}}}
		}
		return &Policy{source: path, exists: false, selection: SelectedByNoFile}, nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // the instance's own policy file, by design
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	pol, problems := parse(data)
	if ymlExists {
		problems = append(problems, Problem{
			Message: "a " + wrongName + " sits beside " + FileName + ": hactl reads only " + FileName +
				", so one of the two is not what the owner thinks is in effect — remove or merge it",
		})
	}
	if len(problems) > 0 {
		sortProblems(problems)
		return nil, &LoadError{File: path, Problems: problems}
	}
	pol.source, pol.exists = path, true
	return pol, nil
}

// regularFileExists: (false, nil) for a missing path, an error for a path that
// exists but is not a regular file or cannot be inspected.
func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspecting %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s exists but is not a regular file", path)
	}
	return true, nil
}

func sortProblems(ps []Problem) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Line != ps[j].Line {
			return ps[i].Line < ps[j].Line
		}
		return ps[i].Message < ps[j].Message
	})
}

var yamlLineRE = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// parse reads data as a policy. Every problem found is returned; the Policy
// is meaningful only when there are none.
func parse(data []byte) (*Policy, []Problem) {
	w := &walker{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, []Problem{{Line: 0, Message: "the file is empty: version missing (want version: 1)"}}
		}
		return nil, []Problem{yamlProblem(err)}
	}
	// A second document would be silently ignored by a single Decode. Never
	// Decode again after an error: yaml.v3 does not recover from one.
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		w.addf(extra.Line, "more than one YAML document: a policy is one document, and everything from this --- on would be ignored")
	case !errors.Is(err, io.EOF):
		w.problems = append(w.problems, yamlProblem(err))
	}

	if len(doc.Content) == 0 {
		w.addf(0, "the file is empty: version missing (want version: 1)")
		return nil, w.problems
	}
	pol := w.top(doc.Content[0])
	return pol, w.problems
}

func yamlProblem(err error) Problem {
	msg := err.Error()
	if m := yamlLineRE.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return Problem{Line: n, Message: "not valid YAML: " + m[2]}
	}
	return Problem{Message: "not valid YAML: " + strings.TrimPrefix(msg, "yaml: ")}
}

// walker collects problems while reading the node tree.
type walker struct{ problems []Problem }

func (w *walker) addf(line int, format string, args ...any) {
	w.problems = append(w.problems, Problem{Line: line, Message: fmt.Sprintf(format, args...)})
}

// pair is one mapping entry in file order.
type pair struct{ key, val *yaml.Node }

// usable refuses the YAML constructs the policy format does not define and
// that a node walk would otherwise mis-read: an alias (its node has no
// content of its own, so a walk sees an empty value and drops the rules it
// stands for).
func (w *walker) usable(n *yaml.Node, what string) bool {
	if n.Kind == yaml.AliasNode {
		w.addf(n.Line, "%s is a YAML alias (*%s): anchors, aliases and merge keys are not part of the policy format — write the value out", what, n.Value)
		return false
	}
	return true
}

// mapping returns n's entries in file order, or ok=false with a problem when
// n is not a mapping. Duplicate keys, merge keys and non-scalar keys are
// problems: YAML forbids a key twice, and a walk that kept the last would
// silently drop the first.
func (w *walker) mapping(n *yaml.Node, what string) ([]pair, bool) {
	if !w.usable(n, what) {
		return nil, false
	}
	if n.Kind != yaml.MappingNode {
		w.addf(n.Line, "%s must be a mapping (got %s)", what, describe(n))
		return nil, false
	}
	pairs := make([]pair, 0, len(n.Content)/2)
	firstLine := map[string]int{}
	ok := true
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch {
		case k.Kind == yaml.AliasNode:
			w.addf(k.Line, "a key in %s is a YAML alias (*%s): anchors, aliases and merge keys are not part of the policy format", what, k.Value)
			ok = false
			continue
		case k.Tag == "!!merge":
			w.addf(k.Line, "merge key << in %s: anchors, aliases and merge keys are not part of the policy format — write the entries out", what)
			ok = false
			continue
		case k.Kind != yaml.ScalarNode:
			w.addf(k.Line, "a key in %s must be a plain string (got %s)", what, describe(k))
			ok = false
			continue
		}
		if prev, dup := firstLine[k.Value]; dup {
			w.addf(k.Line, "duplicate key %q in %s (first at line %d): YAML allows a key once, and one of the two would be silently lost", k.Value, what, prev)
			ok = false
			continue
		}
		firstLine[k.Value] = k.Line
		pairs = append(pairs, pair{k, v})
	}
	return pairs, ok
}

// str reads a non-empty string scalar.
func (w *walker) str(n *yaml.Node, what string) (string, bool) {
	if !w.usable(n, what) {
		return "", false
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		w.addf(n.Line, "%s must be a string (got %s)", what, describe(n))
		return "", false
	}
	if n.Value == "" {
		w.addf(n.Line, "%s is empty", what)
		return "", false
	}
	return n.Value, true
}

func describe(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.AliasNode:
		return "an alias"
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			return "nothing (null)"
		case "!!str":
			return strconv.Quote(n.Value)
		default:
			return n.Tag + " " + strconv.Quote(n.Value)
		}
	default:
		return "an unknown node"
	}
}

func (w *walker) top(n *yaml.Node) *Policy {
	pairs, _ := w.mapping(n, "the top level")
	if n.Kind != yaml.MappingNode {
		return nil
	}
	var (
		versionSeen bool
		active      string
		activeLine  int
		activeSet   bool
		profiles    []*profile
	)
	for _, kv := range pairs {
		switch kv.key.Value {
		case "version":
			versionSeen = true
			v := kv.val
			if w.usable(v, "version") && (v.Kind != yaml.ScalarNode || v.Tag != "!!int" || !isOne(v.Value)) {
				w.addf(v.Line, "version must be the integer 1 (got %s)", describe(v))
			}
		case "active":
			// Presence decides, never the decoded value: `active:` with no
			// value is a half-edited switch, not an absent key (D-60).
			activeLine = kv.key.Line
			if s, ok := w.str(kv.val, "active"); ok {
				active, activeSet = s, true
			} else {
				activeSet = true // broken already; keep it out of D-61's fallback
			}
		case "profiles":
			profiles = w.profiles(kv.val)
		default:
			w.addf(kv.key.Line, "unknown key %q (want version, active or profiles)", kv.key.Value)
		}
	}
	if !versionSeen {
		w.addf(n.Line, "version missing (want version: 1)")
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].name < profiles[j].name })

	pol := &Policy{profiles: profiles}
	find := func(name string) *profile {
		for _, pr := range profiles {
			if pr.name == name {
				return pr
			}
		}
		return nil
	}
	switch {
	case activeSet && active != "":
		pr := find(active)
		if pr == nil {
			names := make([]string, len(profiles))
			for i, p := range profiles {
				names[i] = p.name
			}
			declared := "none"
			if len(names) > 0 {
				declared = strings.Join(names, ", ")
			}
			w.addf(activeLine, "active names %q, which the file does not declare (declared: %s)", active, declared)
		}
		pol.view, pol.selection = pr, SelectedByActive
	case activeSet:
		// broken; reported above
	case find("default") != nil:
		pol.view, pol.selection = find("default"), SelectedByDefault
	default:
		pol.selection = SelectedByImplicit
	}
	return pol
}

func isOne(s string) bool {
	n, err := strconv.ParseInt(s, 0, 64)
	return err == nil && n == 1
}

func (w *walker) profiles(n *yaml.Node) []*profile {
	pairs, _ := w.mapping(n, "profiles")
	out := make([]*profile, 0, len(pairs))
	for _, kv := range pairs {
		if kv.key.Tag != "!!str" || kv.key.Value == "" {
			w.addf(kv.key.Line, "a profile name must be a non-empty string (got %s)", describe(kv.key))
			continue
		}
		if pr := w.profile(kv.key.Value, kv.key.Line, kv.val); pr != nil {
			out = append(out, pr)
		}
	}
	return out
}

func (w *walker) profile(name string, line int, n *yaml.Node) *profile {
	what := "profile " + strconv.Quote(name)
	pairs, _ := w.mapping(n, what)
	if n.Kind != yaml.MappingNode {
		return nil
	}
	pr := &profile{name: name, line: line, deflt: Allow}
	var whereNode *yaml.Node
	for _, kv := range pairs {
		switch kv.key.Value {
		case "default":
			if s, ok := w.str(kv.val, what+" default"); ok {
				switch s {
				case "allow":
					pr.deflt = Allow
				case "deny":
					pr.deflt = Deny
				default:
					w.addf(kv.val.Line, "%s default must be allow or deny (got %q)", what, s)
				}
			}
		case "allow":
			pr.rules = append(pr.rules, w.rules(Allow, what, kv.val)...)
		case "deny":
			pr.rules = append(pr.rules, w.rules(Deny, what, kv.val)...)
		case "where":
			whereNode = kv.val
		default:
			w.addf(kv.key.Line, "unknown key %q in %s (want default, allow, deny or where)", kv.key.Value, what)
		}
	}
	if whereNode != nil {
		w.where(pr, what, whereNode)
	}
	sort.SliceStable(pr.rules, func(i, j int) bool {
		a, b := pr.rules[i], pr.rules[j]
		if a.Effect != b.Effect {
			return a.Effect == Deny
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Pattern < b.Pattern
	})
	return pr
}

func (w *walker) rules(eff Effect, what string, n *yaml.Node) []Rule {
	section := what + " " + string(eff)
	pairs, _ := w.mapping(n, section)
	out := make([]Rule, 0, len(pairs))
	for _, kv := range pairs {
		pat := kv.key.Value
		if kv.key.Tag != "!!str" {
			w.addf(kv.key.Line, "pattern %s in %s must be a string", describe(kv.key), section)
			continue
		}
		addr, err := parseAddress(pat, false)
		if err != nil {
			w.addf(kv.key.Line, "pattern %q: %v", pat, err)
			continue
		}
		written, ok := w.verbList(kv.val, pat)
		if !ok {
			continue
		}
		verbs, err := ParseVerbs(written)
		if err != nil {
			w.addf(kv.val.Line, "verbs for %q: %v", pat, err)
			continue
		}
		out = append(out, Rule{Effect: eff, Pattern: pat, Written: written, Verbs: verbs, Line: kv.key.Line, addr: addr})
	}
	return out
}

func (w *walker) verbList(n *yaml.Node, pat string) ([]string, bool) {
	what := "verbs for " + strconv.Quote(pat)
	if !w.usable(n, what) {
		return nil, false
	}
	if n.Kind != yaml.SequenceNode {
		w.addf(n.Line, "%s must be a list (got %s)", what, describe(n))
		return nil, false
	}
	out := make([]string, 0, len(n.Content))
	ok := true
	for _, item := range n.Content {
		s, good := w.str(item, "a verb in "+what)
		if !good {
			ok = false
			continue
		}
		out = append(out, s)
	}
	if !ok {
		return nil, false
	}
	if len(out) == 0 {
		w.addf(n.Line, "%s: empty verb list (want at least one of read, control, edit, create, delete or write)", what)
		return nil, false
	}
	return out, true
}

func (w *walker) where(pr *profile, what string, n *yaml.Node) {
	section := what + " where"
	pairs, _ := w.mapping(n, section)
	for _, kv := range pairs {
		pat := kv.key.Value
		var target *Rule
		for i := range pr.rules {
			if pr.rules[i].Effect == Allow && pr.rules[i].Pattern == pat {
				target = &pr.rules[i]
				break
			}
		}
		if target == nil {
			w.addf(kv.key.Line, "where %q names no allow pattern of %s (a where narrows an allow, written exactly as in allow)", pat, what)
			continue
		}
		if target.addr.any || !target.addr.spec.Where {
			w.addf(kv.key.Line, "where %q: a selector narrows only an entity or device pattern", pat)
			continue
		}
		sel, ok := w.selector(kv.val, pat)
		if ok {
			target.Where = sel
		}
	}
}

func (w *walker) selector(n *yaml.Node, pat string) (*Where, bool) {
	what := "the selector for " + strconv.Quote(pat)
	pairs, ok := w.mapping(n, what)
	if n.Kind != yaml.MappingNode {
		return nil, false
	}
	sel := &Where{}
	for _, kv := range pairs {
		switch kv.key.Value {
		case "area":
			v, good := w.selectorValues(kv.val, what+" area")
			ok = ok && good
			sel.Area = v
		case "label":
			v, good := w.selectorValues(kv.val, what+" label")
			ok = ok && good
			sel.Label = v
		default:
			w.addf(kv.key.Line, "unknown key %q in %s (want area or label)", kv.key.Value, what)
			ok = false
		}
	}
	if len(pairs) == 0 && n.Kind == yaml.MappingNode {
		w.addf(n.Line, "%s is empty (want area, label or both)", what)
		return nil, false
	}
	return sel, ok
}

func (w *walker) selectorValues(n *yaml.Node, what string) ([]string, bool) {
	if !w.usable(n, what) {
		return nil, false
	}
	if n.Kind == yaml.ScalarNode {
		s, ok := w.str(n, what)
		if !ok {
			return nil, false
		}
		return []string{s}, true
	}
	if n.Kind != yaml.SequenceNode {
		w.addf(n.Line, "%s must be a string or a list of strings (got %s)", what, describe(n))
		return nil, false
	}
	if len(n.Content) == 0 {
		w.addf(n.Line, "%s is an empty list", what)
		return nil, false
	}
	out := make([]string, 0, len(n.Content))
	ok := true
	for _, item := range n.Content {
		s, good := w.str(item, "a value in "+what)
		if !good {
			ok = false
			continue
		}
		out = append(out, s)
	}
	return out, ok
}
