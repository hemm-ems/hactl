package manual

import (
	"fmt"
	"sort"
	"strings"
)

// CoreHeadings, FamilySections, and the alias map are ported verbatim from
// integrations/llm/tools.py (_CORE_HEADINGS/_GROUP_SECTIONS/_GROUP_ALIASES);
// keep the two in sync. Heading strings must match docs/manual.md verbatim —
// enforced by TestMappedHeadingsExist.

// CoreHeadings are the sections every session needs regardless of command
// family. Cross-family workflows stay here; single-family workflows travel
// with their family.
var CoreHeadings = []string{
	"## Quick routing",
	"## Mental model",
	`### "What went wrong recently?" / "What broke?"`,
	`### "Show me the daily report" / "Morning check" / "Status summary"`,
	"## Filtering & discovery",
	"## Output conventions",
	"## Global flags",
}

// FamilySections maps a command family to its manual headings, workflows
// before reference (the model reads the head and skims the tail).
var FamilySections = map[string][]string{
	"auto": {
		`### "Why did my automation fail?"`,
		`### "Deploy an automation change"`,
		`### "Create a new automation / script / helper"`,
		`### "Delete an automation / helper"`,
		`### "Find and act on a group of automations"`,
		"### Automations",
		"### Automations — create & delete",
		"### Write path (automations)",
		"### Ghost entities (`--restored`)",
	},
	"script": {
		`### "Deploy a script change"`,
		"### Scripts",
		"### Scripts — create & delete",
		"### Write path (scripts)",
	},
	"ent": {
		`### "Is this sensor behaving normally?"`,
		`### "What else is related to this entity?"`,
		`### "Organize entities with labels"`,
		"### Entities & history",
		"### Ghost entities (`--restored`)",
	},
	"device": {
		`### "Which entities belong to <concept>?" (find things by concept)`,
		"### Devices",
	},
	"label": {
		`### "Organize entities with labels"`,
		"### Registry: labels, areas, floors",
	},
	"dash": {
		`### "Build a dashboard" / "Design or modify a dashboard"`,
		"### Dashboards (Lovelace)",
	},
	"svc": {
		`### "Find and act on a group of automations"`,
		"### Templates & services",
	},
	"tpl": {
		"### Templates & services",
		"### Templates — create & delete",
	},
	"config": {"### Config entries & flows"},
	"helper": {
		`### "Create a new automation / script / helper"`,
		"### Helpers",
	},
	"log":       {"### Logs & custom components"},
	"health":    {"### Setup & health"},
	"cache":     {"### Cache & version"},
	"companion": {"### WireGuard (companion lifeline)"},
	"energy":    {"### Energy"},
	"ref":       {"### References (find and rename entity_ids)"},
}

// Aliases maps top-level commands onto the family whose sections cover them.
// rollback is CLI-only (tools.py has no wrapper for it). tools.py also
// aliases setup→health, but on the CLI setup is the interactive bootstrap and
// is exempt from injection instead.
var Aliases = map[string]string{
	"trace":    "auto",
	"rollback": "auto",
	"cc":       "log",
	"issues":   "health",
	"changes":  "health",
	"area":     "label",
	"floor":    "label",
}

// Exempt lists top-level commands that never trigger manual delivery: the
// manual itself, the MCP server (has its own delivery), interactive/meta
// commands, and cobra's hidden completion machinery.
var Exempt = map[string]bool{
	"rtfm":             true,
	"mcp":              true,
	"setup":            true,
	"version":          true,
	"help":             true,
	"completion":       true,
	"__complete":       true,
	"__completeNoDesc": true,
}

// FamilyFor resolves a top-level command name to its manual family.
func FamilyFor(top string) (string, bool) {
	if f, ok := Aliases[top]; ok {
		return f, true
	}
	if _, ok := FamilySections[top]; ok {
		return top, true
	}
	return "", false
}

// FamilyMembers returns every top-level command a family's sections cover —
// the family's own name plus every alias pointing at it — in stable order.
//
// It is derived from the same two tables FamilyFor resolves against, so a new
// alias cannot leave the user-facing naming behind (`area` and `floor` were
// aliased onto `label` and the prose kept saying "label" for years).
func FamilyMembers(family string) []string {
	members := make([]string, 0, 3)
	if _, ok := FamilySections[family]; ok {
		members = append(members, family)
	}
	for alias, f := range Aliases {
		if f == family {
			members = append(members, alias)
		}
	}
	sort.Strings(members)
	return members
}

// FamilyLabel names a family the way a caller can act on it.
//
// A family that covers one command is named after it. A family that covers
// several is named after all of them, because naming it after one states
// something false about the other two: label/area/floor share a single manual
// section keyed "label", and both the how-to banner and the --confirm refusal
// interpolated that internal key — an agent that had never typed `label` was
// told this was its "first label command", which it can act on by running the
// wrong command. Same shape on trace/rollback (keyed "auto"), cc (keyed "log")
// and changes/issues (keyed "health").
func FamilyLabel(family string) string {
	members := FamilyMembers(family)
	if len(members) == 0 {
		return family
	}
	return strings.Join(members, "/")
}

// Families returns the family names in stable order (map iteration is not).
func Families() []string {
	return []string{
		"auto", "cache", "companion", "config", "dash", "device", "energy",
		"ent", "health", "helper", "label", "log", "ref", "script", "svc", "tpl",
	}
}

// The bracket notes below are tuning-sensitive prompt surface (measured in
// dev/tuning) and are parsed by dev/tuning/inject_tokens.py — every note must
// begin with "[hactl manual". Wording tracks tools.py with CLI adaptations.

// CoreNote precedes the core block.
const CoreNote = "[hactl manual core — delivered once with your first hactl " +
	"command of this session. Detailed how-to sections for each command " +
	"family arrive automatically with the result of your first command from " +
	"that family. Every write command is dry-run by default; repeat it with " +
	"--confirm only after the user explicitly confirms the plan — the " +
	"original request is not that confirmation. A --confirm on your first " +
	"command of a family is refused: dry-run first.]"

// FullNote precedes the whole manual in full mode.
const FullNote = "[hactl manual — delivered once with your first hactl " +
	"command of this session. Use it for every subsequent command, flag, and " +
	"workflow decision.]"

// FamilyNote precedes a family how-to block. It names every command the block
// covers (FamilyLabel), never just the internal key the sections are filed
// under — see FamilyLabel for what that cost.
func FamilyNote(family string) string {
	return fmt.Sprintf("[hactl manual — '%[1]s' family how-to, delivered "+
		"with your first %[1]s command. Use it for every subsequent %[1]s "+
		"call. Complete the routing-table sequence for the user's question "+
		"before drilling into anything from this section.]", FamilyLabel(family))
}
