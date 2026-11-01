package policy

import (
	"errors"
	"fmt"
	"strconv"
)

// Verb is one of the five things a rule can grant or deny (D-53). The order of
// the constants is the canonical order: every list of verbs hactl renders —
// in prose, in --json, in a Verdict's rule text when it is canonicalised — is
// in this order, never in map order (H-16).
type Verb int

const (
	// Read is seeing a resource: `ent show`, `config file`, `dash show`.
	Read Verb = iota + 1
	// Control is changing runtime state, not config: `svc call`, `script run`.
	Control
	// Edit is changing an existing config or registry field: `auto apply`,
	// `ent set-label`, `dash save`.
	Edit
	// Create is bringing a new resource into existence: `auto create`,
	// `area create`.
	Create
	// Delete is removing a resource: `auto delete`, `dash delete`.
	Delete
)

// WriteAlias is the rule-only spelling for {Control, Edit, Create, Delete}.
// It is accepted in a rule's verb list (ParseVerbs) and refused where one
// concrete verb is asked about (ParseVerb, `policy check`), because a verdict
// is per verb.
const WriteAlias = "write"

// AllVerbs returns the five verbs in canonical order.
func AllVerbs() []Verb { return []Verb{Read, Control, Edit, Create, Delete} }

// String returns the verb's spelling in policy.yaml and in output: "read",
// "control", "edit", "create", "delete". An out-of-range value renders as
// "verb(<n>)" so it can never pass for a real verb.
func (v Verb) String() string {
	switch v {
	case Read:
		return "read"
	case Control:
		return "control"
	case Edit:
		return "edit"
	case Create:
		return "create"
	case Delete:
		return "delete"
	default:
		return "verb(" + strconv.Itoa(int(v)) + ")"
	}
}

// ParseVerb parses one concrete verb, exactly as spelled in the manual
// (lowercase; no trimming, no case folding). "write" is refused with an error
// naming the four verbs it stands for; anything else unknown is refused with an
// error listing the five verbs. Used by `policy check <resource> <verb>`.
func ParseVerb(s string) (Verb, error) {
	for _, v := range AllVerbs() {
		if v.String() == s {
			return v, nil
		}
	}
	if s == WriteAlias {
		return 0, errors.New(`"write" is an alias for control, edit, create and delete in rules; a verdict is per verb, so ask about one of them`)
	}
	return 0, fmt.Errorf("unknown verb %q (want read, control, edit, create or delete)", s)
}

// ParseVerbs parses a rule's verb list. "write" expands to Control, Edit,
// Create, Delete. The result is de-duplicated and in canonical order. An empty
// list, or any unknown verb, is an error naming the offending value — the
// caller (Load) turns it into a D-60 problem at the list's file:line.
func ParseVerbs(ss []string) ([]Verb, error) {
	if len(ss) == 0 {
		return nil, errors.New("empty verb list (want at least one of read, control, edit, create, delete or write)")
	}
	var set [Delete + 1]bool
	for _, s := range ss {
		if s == WriteAlias {
			for _, v := range []Verb{Control, Edit, Create, Delete} {
				set[v] = true
			}
			continue
		}
		v, err := ParseVerb(s)
		if err != nil {
			return nil, fmt.Errorf("unknown verb %q (want read, control, edit, create, delete or write)%s", s,
				didYouMean(s, []string{"read", "control", "edit", "create", "delete", WriteAlias}))
		}
		set[v] = true
	}
	out := make([]Verb, 0, len(set))
	for _, v := range AllVerbs() {
		if set[v] {
			out = append(out, v)
		}
	}
	return out, nil
}
