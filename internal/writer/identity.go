package writer

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// D-130 (issue #169): a write replaces the target's entry with the text it is
// given, and the companion finds an entry by its `id:` and by nothing else. A
// text carrying another id therefore does not edit the automation named on the
// command line — it replaces it with a different one, under a different config
// id, and nothing said so. `auto apply` and `auto rollback` both refused
// nothing here: a copied file or a typo in `id:` was a silent rename.
//
// The rule is one comparison, made before anything is validated, rehearsed or
// written: the text's top-level `id:` must be a STRING equal to the target's
// config id. A number whose digits match is refused too, because the companion
// compares strings, so the written entry could no longer be found by its own
// id. The refusal names the deliberate paths, since a caller who meant to
// rename has a way to do it that says so.

// identityRole is the wording that differs between the two texts the rule
// guards: a candidate file an apply would write, and a backup a rollback would
// restore.
type identityRole struct {
	noun   string // what the text is: "file", "backup"
	verb   string // what the write does to the target: "replace", "restore"
	gerund string // the write itself: "writing", "restoring"
}

var (
	candidateRole = identityRole{noun: "file", verb: "replace", gerund: "writing"}
	backupRole    = identityRole{noun: "backup", verb: "restore", gerund: "restoring"}
)

// CheckCandidateID refuses a candidate file whose top-level `id:` is not the
// string configID (D-130). path is how the caller named the file; text is its
// content. A text that is not a YAML mapping passes: it is not an automation,
// and the parser downstream refuses it in its own words.
func CheckCandidateID(path string, text []byte, configID string) error {
	return checkIdentity(candidateRole, path, text, configID)
}

func checkIdentity(role identityRole, path string, text []byte, configID string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		if role == backupRole {
			return fmt.Errorf("parsing backup YAML: %w", err)
		}
		return fmt.Errorf("parsing local YAML: %w", err)
	}
	found, ok := idMismatch(&doc, configID)
	if ok {
		return nil
	}
	return fmt.Errorf("%s %s %s, not the config id of the automation it would %s (%q): "+
		"%s it would replace %s with a different automation. "+
		"To give the automation a new config id, run `hactl auto create -f %s`, then `hactl auto delete %s`; "+
		"to rename its entity, run `hactl ent rename`",
		role.noun, path, found, role.verb, configID,
		role.gerund, configID, path, configID)
}

// idMismatch reports whether doc's top-level `id:` is the string configID and,
// when it is not, how it differs — the phrase the refusal puts after the file's
// name. It reads the node, not a decoded map, because a decoded map has
// already turned `id: 1712345678901` into a number and lost both the fact that
// it was unquoted and the digits as written.
func idMismatch(doc *yaml.Node, configID string) (string, bool) {
	if len(doc.Content) == 0 {
		return "has no id", false // an empty document
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", true
	}
	var id *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "id" {
			id = root.Content[i+1]
			break
		}
	}
	if id != nil && id.Kind == yaml.AliasNode {
		id = id.Alias
	}
	switch {
	case id == nil || (id.Kind == yaml.ScalarNode && id.ShortTag() == "!!null"):
		return "has no id", false
	case id.Kind != yaml.ScalarNode:
		return "has an id that is not a string", false
	}
	switch id.ShortTag() {
	case "!!str":
		if id.Value == configID {
			return "", true
		}
		return fmt.Sprintf("has id %q", id.Value), false
	case "!!int", "!!float":
		return fmt.Sprintf("has id %s (a number — quote it)", id.Value), false
	default: // a bool, a timestamp: YAML reads them as something else than text
		return fmt.Sprintf("has id %s (not a string — quote it)", id.Value), false
	}
}
