//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// D-173 against a real Home Assistant: an instance whose profile reads
// everything except lock.*, and a draft that gives lint every reason to name a
// lock (a deny on lock.*, and a where: on lock.* the agent could satisfy by
// editing labels — the self-grant finding names an example entity). Over the
// agent's pinned MCP server (no terminal) lint never prints a lock id; the
// owner's lint at a terminal does.
// ============================================================================

const lintHidesLocks = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "*": [read]
    deny:
      "entity:lock.*": [read]
`

const lintLockDraft = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:lock.*": [control]
      "entity:*/registry/labels": [edit]
    deny:
      "entity:lock.*/attr/friendly_name": [read]
    where:
      "entity:lock.*": { label: pg_lint_label }
`

func TestPolicyLintNamesNoHiddenIDWithoutATerminal(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	locks := withPrefix(haStates(t, inst), "lock.")
	if len(locks) == 0 {
		t.Fatal("fixture: no lock.* entity — the test would prove nothing")
	}
	bin := buildHactl(t)
	home := t.TempDir()
	env := hactlEnv(home)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), lintHidesLocks)
	if err := os.WriteFile(filepath.Join(dir, "policy.draft.yaml"), []byte(lintLockDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	namesALock := func(s string) string {
		for _, l := range locks {
			if strings.Contains(s, l) {
				return l
			}
		}
		return ""
	}

	// The owner, at a terminal: the whole instance — the lock is named.
	owner, err := underTerminal(t, bin, env, nil, "--dir", dir, "policy", "lint", "--file", "policy.draft.yaml")
	if namesALock(owner) == "" {
		t.Fatalf("the owner's lint at a terminal names no lock (%v) — the case is inert:\n%s", err, owner)
	}

	// The agent, over its pinned MCP server: never a lock id, in any form.
	s := mcpSession(t, bin, env, dir)
	for _, cmd := range []string{
		"policy lint --file policy.draft.yaml",
		"policy lint --file policy.draft.yaml --json",
		"policy lint",
	} {
		text, _ := mcpRun(t, s, cmd)
		if l := namesALock(text); l != "" {
			t.Errorf("MCP %q names %s, which the active profile hides:\n%s", cmd, l, text)
		}
	}
	text, _ := mcpRun(t, s, "policy lint --file policy.draft.yaml")
	if !strings.Contains(text, "inventory: what the active profile can read") {
		t.Errorf("the agent's lint does not say what it checked against:\n%s", text)
	}
}
