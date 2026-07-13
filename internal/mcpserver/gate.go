// Package mcpserver runs hactl as an MCP (Model Context Protocol) stdio
// server, exposing the CLI as a single generic tool. It deliberately does not
// import internal/cmd; the command runner and resolver are injected via
// Options to avoid an import cycle.
package mcpserver

import (
	"fmt"
	"strings"
)

// Decision classifies a command path for MCP exposure.
type Decision int

const (
	// Allowed means the command may run in the current mode.
	Allowed Decision = iota
	// BlockedReadOnly means the command mutates state and the server was
	// started without --allow-writes.
	BlockedReadOnly
	// BlockedAlways means the command never makes sense over MCP
	// (interactive, or the MCP server itself).
	BlockedAlways
	// BlockedUnknown means the command is not classified; fail closed.
	BlockedUnknown
)

// readCommands lists command paths that only observe state. Every leaf
// command must appear in exactly one of readCommands, writeCommands, or
// alwaysBlocked; TestGateExhaustive enforces this against the live command
// tree, so adding a CLI command without classifying it here fails the build.
var readCommands = map[string]bool{
	"hactl area ls":                    true,
	"hactl auto cat":                   true,
	"hactl auto diff":                  true,
	"hactl auto ls":                    true,
	"hactl auto show":                  true,
	"hactl cache status":               true,
	"hactl cc logs":                    true,
	"hactl cc ls":                      true,
	"hactl cc show":                    true,
	"hactl changes":                    true,
	"hactl companion logs":             true,
	"hactl companion status":           true,
	"hactl companion wireguard status": true,
	"hactl config block":               true,
	"hactl config entries":             true,
	"hactl config file":                true,
	"hactl config files":               true,
	"hactl config flow-inspect":        true,
	// config show is read-only: diagnostics is a plain GET, and the
	// options-flow fallback starts then immediately aborts a flow without
	// submitting any step, so nothing is mutated (unlike config options,
	// which opens a flow as the entry point to a write and is gated write).
	"hactl config show":    true,
	"hactl dash grep":      true,
	"hactl dash ls":        true,
	"hactl dash resources": true,
	"hactl dash show":      true,
	"hactl device ls":      true,
	"hactl device show":    true,
	"hactl ent anomalies":  true,
	"hactl ent hist":       true,
	"hactl ent ls":         true,
	"hactl ent related":    true,
	"hactl ent show":       true,
	"hactl ent who":        true,
	"hactl floor ls":       true,
	"hactl health":         true,
	"hactl helper cat":     true,
	"hactl helper ls":      true,
	"hactl helper show":    true,
	"hactl issues":         true,
	"hactl label ls":       true,
	"hactl log":            true,
	"hactl log show":       true,
	"hactl ref scan":       true,
	"hactl ref validate":   true,
	"hactl rtfm":           true,
	"hactl script cat":     true,
	"hactl script diff":    true,
	"hactl script ls":      true,
	"hactl script show":    true,
	"hactl tpl cat":        true,
	"hactl tpl eval":       true,
	"hactl trace show":     true,
	"hactl version":        true,
}

// writeCommands lists command paths that mutate Home Assistant, the
// companion add-on, or local hactl state. Blocked unless --allow-writes.
var writeCommands = map[string]bool{
	"hactl area create":                true,
	"hactl area delete":                true,
	"hactl auto apply":                 true,
	"hactl auto create":                true,
	"hactl auto delete":                true,
	"hactl auto rollback":              true,
	"hactl cache clear":                true,
	"hactl cache refresh":              true,
	"hactl companion wireguard config": true,
	"hactl companion wireguard down":   true,
	"hactl companion wireguard up":     true,
	"hactl config delete":              true,
	"hactl config flow-start":          true,
	"hactl config flow-step":           true,
	"hactl config options":             true,
	"hactl dash create":                true,
	"hactl dash delete":                true,
	"hactl dash replace":               true,
	"hactl dash save":                  true,
	"hactl ent set-area":               true,
	"hactl ent set-label":              true,
	"hactl floor create":               true,
	"hactl floor delete":               true,
	"hactl helper create":              true,
	"hactl helper delete":              true,
	"hactl label create":               true,
	"hactl label delete":               true,
	"hactl ref replace":                true,
	"hactl rollback":                   true,
	"hactl script apply":               true,
	"hactl script create":              true,
	"hactl script delete":              true,
	"hactl script run":                 true,
	"hactl svc call":                   true,
	"hactl tpl create":                 true,
	"hactl tpl delete":                 true,
}

// alwaysBlocked lists command paths that are never exposed over MCP.
var alwaysBlocked = map[string]bool{
	"hactl setup":      true, // interactive prompts
	"hactl mcp":        true, // would recurse into a nested server
	"hactl completion": true, // shell-completion scripts, useless to a model
	"hactl help":       true, // cobra help command; --help works on any command
}

// Gate decides whether a command path may run. Group commands (e.g. bare
// "hactl ent") just print help and are allowed; classification applies to
// leaf commands.
func Gate(path string, allowWrites bool) (Decision, string) {
	switch {
	case alwaysBlocked[path] || strings.HasPrefix(path, "hactl completion "):
		return BlockedAlways, fmt.Sprintf("command %q is not available over MCP", path)
	case readCommands[path]:
		return Allowed, ""
	case writeCommands[path]:
		if allowWrites {
			return Allowed, ""
		}
		return BlockedReadOnly, fmt.Sprintf(
			"command %q is blocked: server is running read-only; restart with 'hactl mcp --allow-writes' to permit mutating commands", path)
	case isGroup(path):
		return Allowed, ""
	default:
		return BlockedUnknown, fmt.Sprintf("command %q is not classified for MCP use; run it via the hactl CLI instead", path)
	}
}

// groupCommands are non-leaf paths that only print help when run bare.
var groupCommands = map[string]bool{
	"hactl":                     true,
	"hactl area":                true,
	"hactl auto":                true,
	"hactl cache":               true,
	"hactl cc":                  true,
	"hactl companion":           true,
	"hactl companion wireguard": true,
	"hactl config":              true,
	"hactl dash":                true,
	"hactl device":              true,
	"hactl ent":                 true,
	"hactl floor":               true,
	"hactl helper":              true,
	"hactl label":               true,
	"hactl ref":                 true,
	"hactl script":              true,
	"hactl svc":                 true,
	"hactl tpl":                 true,
	"hactl trace":               true,
}

func isGroup(path string) bool {
	return groupCommands[path]
}

// ClassifiedPaths returns every command path the gate knows, keyed by set
// ("read", "write", "always", "group"). Used by the exhaustiveness test in
// internal/cmd, which compares it against the live command tree in both
// directions: no unclassified command, no stale classification.
func ClassifiedPaths() map[string][]string {
	out := make(map[string][]string, 4)
	for set, m := range map[string]map[string]bool{
		"read": readCommands, "write": writeCommands,
		"always": alwaysBlocked, "group": groupCommands,
	} {
		for p := range m {
			out[set] = append(out[set], p)
		}
	}
	return out
}
