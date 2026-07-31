//go:build companion

package companiontest

import (
	"context"
	"strings"
	"testing"
)

// ============================================================================
// WP2 — document integrity, proved end to end.
//
// Both cases below are companion-routed, and the live instance runs the
// RELEASED add-on, so step ⑥ of FIXPLAN-livefire.md's ritual cannot verify a
// companion-side fix against it until the next release. This tier is the
// substitute the plan names: a real Home Assistant, a companion built from
// source (COMPANION_SRC), and the hactl binary driving both.
// ============================================================================

// TestE2EResolvedModeKeepsHATagsCLI is finding #20 through the whole stack.
//
// Resolved mode re-serialized the document through a load/dump that did not
// know HA's tags, so `entity_id: !input button_entity` came back as
// `entity_id: '!input button_entity'` — a quoted string, still valid YAML, and
// an automation that triggers on an entity literally named "!input
// button_entity". The blueprint it was found in contains no `!include` at all,
// which is why the fixture below has none either: the corruption was never
// about include resolution, the only thing resolved mode's help claims to do.
//
// The assertion pair is the point. `--raw` is the ground truth (the bytes on
// disk), and resolved mode may legitimately reflow the document; what it may
// not do is disagree with `--raw` about which values are tags.
func TestE2EResolvedModeKeepsHATagsCLI(t *testing.T) {
	// At the config root rather than under blueprints/: the companion's write
	// route does not create directories, and whether HA has materialised
	// `blueprints/automation/` yet is not a property this case is about. What
	// is load-bearing is the CONTENT — `!input`, and no `!include` anywhere.
	const path = "e2e_blueprint_probe.yaml"
	const blueprint = `blueprint:
  name: E2E Toggle On Button
  domain: automation
  input:
    button_entity:
      name: Button
      selector:
        entity:
          domain: input_boolean
triggers:
  - trigger: state
    entity_id: !input button_entity
actions:
  - action: input_boolean.toggle
    target:
      entity_id: !input target_entity
`

	writeConfigFileE2E(t, path, blueprint)

	raw, err := testClient.ReadConfigFileRaw(context.Background(), path)
	if err != nil {
		t.Fatalf("reading %s raw: %v", path, err)
	}
	if strings.Contains(raw.Content, "!include") {
		t.Fatal("the fixture grew an !include — it exists to show the corruption is not about include resolution")
	}

	out, err := runHactlE2E(t, "config", "file", path, "--tokensmax", "0")
	if err != nil {
		t.Fatalf("config file %s: %v\n%s", path, err, out)
	}

	if !strings.Contains(out, "entity_id: !input button_entity") {
		t.Errorf("resolved mode did not render the tag as a tag:\n%s", out)
	}
	for _, quoted := range []string{`'!input button_entity'`, `"!input button_entity"`} {
		if strings.Contains(out, quoted) {
			t.Errorf("resolved mode quoted the tag into a string literal (%s):\n%s", quoted, out)
		}
	}
	// The second tag in the same file, because a fix that reached the first
	// occurrence and not the rest would look identical above.
	if !strings.Contains(out, "entity_id: !input target_entity") {
		t.Errorf("the second tag in the file was not preserved:\n%s", out)
	}
}

// TestE2EResolvedModeKeepsSecretDirectivesCLI is the other tag kind, and the
// one where getting it wrong hides something: `!secret` names a KEY, and a
// reader shown a quoted string does not know a lookup happens at all.
//
// No secrets.yaml is written and none is needed: the resolver is forbidden from
// reading it (C-3, proved in the companion's own tier), so the only thing that
// can travel is the key — and this case is about whether the key travels as a
// tag or as a string.
func TestE2EResolvedModeKeepsSecretDirectivesCLI(t *testing.T) {
	const path = "e2e_secret_probe.yaml"

	writeConfigFileE2E(t, path, "rest:\n  headers:\n    Authorization: !secret e2e_probe_token\n")

	out, err := runHactlE2E(t, "config", "file", path, "--tokensmax", "0")
	if err != nil {
		t.Fatalf("config file %s: %v\n%s", path, err, out)
	}
	if !strings.Contains(out, "Authorization: !secret e2e_probe_token") {
		t.Errorf("the !secret directive was not preserved as a tag:\n%s", out)
	}
	for _, quoted := range []string{`'!secret e2e_probe_token'`, `"!secret e2e_probe_token"`} {
		if strings.Contains(out, quoted) {
			t.Errorf("the !secret directive was quoted into a string literal (%s):\n%s", quoted, out)
		}
	}
}

// TestE2EConfigBlockRoutesATemplateUniqueIDCLI is finding #24: a real
// unique_id in template.yaml answered `Block not found`, the same wording a
// typo gets, while the command's own --help promised `tpl cat <unique_id>`.
//
// The redirect is earned by asking the companion whether the id resolves as a
// template, so the last assertion here is that the command it names actually
// answers — a referral to something that also fails would be worse than none.
func TestE2EConfigBlockRoutesATemplateUniqueIDCLI(t *testing.T) {
	const uniqueID = "e2e_block_redirect_probe"

	original := readConfigFileE2E(t, "template.yaml")
	t.Cleanup(func() { writeConfigFileE2E(t, "template.yaml", original) })

	writeConfigFileE2E(t, "template.yaml", original+
		"\n- sensor:\n"+
		"    - name: E2E Block Redirect Probe\n"+
		"      unique_id: "+uniqueID+"\n"+
		"      state: \"1\"\n")

	out, err := runHactlE2E(t, "config", "block", "template.yaml", uniqueID)
	if err == nil {
		t.Fatalf("`config block template.yaml %s` succeeded — a template block carries neither "+
			"id: nor alias:, so this is a different defect:\n%s", uniqueID, out)
	}
	if !strings.Contains(out, "tpl cat "+uniqueID) {
		t.Errorf("the failure did not steer to the command that can answer:\n%s", out)
	}

	if catOut, catErr := runHactlE2E(t, "tpl", "cat", uniqueID); catErr != nil {
		t.Errorf("the referral names a command that fails for the same id: %v\n%s", catErr, catOut)
	}

	// An id that exists nowhere keeps the plain not-found: the referral is
	// evidence about that id, not a decoration on every failure.
	typo, err := runHactlE2E(t, "config", "block", "template.yaml", uniqueID+"_typo")
	if err == nil {
		t.Fatalf("a fabricated id succeeded:\n%s", typo)
	}
	if strings.Contains(typo, "tpl cat") {
		t.Errorf("an id that resolves nowhere was sent to tpl cat anyway:\n%s", typo)
	}
}

// TestE2ECompanionErrorNamesTheRouteCLI is finding #23 against a real
// companion: the failure names the route it failed on and nothing about how
// the request got there.
//
// This tier reaches the companion directly rather than through Ingress, so the
// prefix it must not print is empty here and the case cannot see the original
// leak. What it CAN see — and what the unit test beside it cannot — is that
// stripping the prefix left a real error still naming its real route.
func TestE2ECompanionErrorNamesTheRouteCLI(t *testing.T) {
	out, err := runHactlE2E(t, "config", "file", "e2e_no_such_file.yaml")
	if err == nil {
		t.Fatalf("reading a nonexistent config file succeeded:\n%s", out)
	}
	if !strings.Contains(out, "GET /v1/config/file") {
		t.Errorf("the error does not name the route it failed on:\n%s", out)
	}
	if strings.Contains(out, "hassio_ingress") {
		t.Errorf("a transport prefix reached the caller:\n%s", out)
	}
}
