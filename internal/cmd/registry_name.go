package cmd

import (
	"fmt"
	"strings"
)

// requireRegistryName refuses a name that is blank, before the request rather
// than after it.
//
// `hactl area create "" --confirm` created a real area on a production
// instance whose `area_id` was the empty string, and from that moment every
// `hactl area` command failed — `ls`, `create` and `delete` alike — because
// H-14 refuses to render a record that decoded without its identity, and
// `delete` has to list the registry before it can resolve anything. Only a raw
// WebSocket call could undo it; hactl could not clean up after hactl.
//
// The refusal has to be here, in the client, and it cannot be moved anywhere
// later. The oracle (internal/integration/registry_blank_name_oracle_test.go)
// settles why:
//
//	name ""    → HA succeeds and mints a blank id      ← the outage
//	name "   " → HA succeeds under an id it chose      ← an id nobody asked for
//	             ("unknown"), and treats it as the same name as ""
//
// Nothing in that sequence gives hactl a moment to object: by the time the
// answer arrives, the record is in HA's registry. A validation that "checks the
// result" would only be able to report the damage.
//
// Blank means "no non-whitespace character", not "empty string". HA's own
// uniqueness check already normalises whitespace away — it refuses "   " as a
// duplicate of "" — so treating the two differently here would disagree with
// the server about what a name is. What the gate deliberately does NOT do is
// trim the caller's name: " Kitchen " is a real name and is sent verbatim
// (TestRegistryCreateAcceptsANameWithSurroundingSpace).
//
// kind is the family as the caller typed it ("area", "floor", "label"), so the
// message names the command that was refused and the identifier it would have
// poisoned.
func requireRegistryName(kind, name string) error {
	if strings.TrimSpace(name) != "" {
		return nil
	}
	return fmt.Errorf("%[1]s name is blank — Home Assistant accepts a blank name and mints a blank "+
		"%[1]s_id for it, which then fails every 'hactl %[1]s' command (%[1]s ls and %[1]s delete "+
		"included, since both must list the registry first) until the record is removed over a raw "+
		"WebSocket; hactl refuses before the request because afterwards there is nothing left to "+
		"refuse. Pass a name with at least one non-whitespace character", kind)
}
