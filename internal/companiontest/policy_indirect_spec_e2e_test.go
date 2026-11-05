//go:build companion

package companiontest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// W-1 (indirection) and W-3 (a required policy), blind spec tests written from
// the contract only — docs/manual.md "What a written automation, script or
// template would do", the profile key `indirect`, "An instance that must have
// a policy"; docs/decisions.md D-133..D-137; SPEC-policy-usp.md W-1 (and its
// "W-1 contract") and W-3. No implementation was read.
//
// Every refusal is driven through the binary — the dry run and --confirm — and
// then proven by reading Home Assistant (or the companion's raw file) back
// outside hactl. Every refused candidate has an allowed twin that differs only
// in the entity it switches; the twin passes under the same profile, so a
// refusal can only come from the guarded entity, never from a fixture the
// profile happens not to allow.
// ============================================================================

const (
	w1GuardedKey = "pg_w1_guarded"
	w1AllowedKey = "pg_w1_allowed"
	w1Guarded    = "input_boolean." + w1GuardedKey
	w1Allowed    = "input_boolean." + w1AllowedKey
	// w1Entity is the placeholder a candidate template switches; each test
	// fills it with w1Guarded for the refused candidate and w1Allowed for its
	// twin.
	w1Entity = "W1_ENTITY"
)

// w1Profile is the agent profile of these tests: default deny, every entity
// readable, the pg_w1_* booleans and switches controllable, their services
// allowed, and pg_w1_* automations, scripts and templates writable — and the
// guarded boolean denied guardedVerbs. extra is appended verbatim (deny lines
// at six spaces, profile keys at four).
func w1Profile(guardedVerbs, extra string) string {
	return pgE2EPolicy(`    default: deny
    allow:
      "entity:*": [read]
      "entity:input_boolean.pg_w1_*": [control]
      "entity:switch.pg_w1_*": [control]
      "service:input_boolean.*": [control]
      "automation:pg_w1_*": [read, edit, create]
      "entity:automation.pg_w1_*": [control, create]
      "script:pg_w1_*": [read, edit, create]
      "entity:script.pg_w1_*": [control, create]
      "template:pg_w1_*": [read, edit, create]
    deny:
      "entity:` + w1Guarded + `": [` + guardedVerbs + `]
` + extra)
}

// w1GuardedDenial is the denial a refused candidate must list: verb on the
// guarded boolean, decided by its deny rule on the policy's line.
// Every refusing profile here denies it `[read, write]`, shown as written.
func w1GuardedDenial(t *testing.T, policy, verb string) e2eDenied {
	t.Helper()
	return e2eDenied{
		Resource: "entity:" + w1Guarded, Verb: verb,
		Rule: fmt.Sprintf(`deny "entity:%s" [read write]`, w1Guarded),
		Line: policyLineOfE2E(t, policy, `"entity:`+w1Guarded+`"`),
	}
}

// seedW1Booleans creates the two booleans as the owner (the tier's own
// instance directory has no policy) and waits for HA to report them.
func seedW1Booleans(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, key := range []string{w1GuardedKey, w1AllowedKey} {
		_, _ = testClient.DeleteHelper(ctx, key, "input_boolean") // a leftover of an aborted run
		file := writeTempYAML(t, key+".yaml", key+":\n  name: PG W1 "+key+"\n")
		if out, err := runHactlE2E(t, "helper", "create", "input_boolean", "-f", file, "--confirm"); err != nil {
			t.Fatalf("fixture: seeding %s as the owner: %v\n%s", key, err, out)
		}
		t.Cleanup(func() { _, _ = testClient.DeleteHelper(context.Background(), key, "input_boolean") })
		waitForHAState(t, "input_boolean."+key)
	}
}

// w1AutoYAML is an automation with a time trigger (it mentions no entity)
// whose action list is actions, indented as list items under `action:`.
func w1AutoYAML(id, actions string) string {
	return fmt.Sprintf(`id: %s
alias: PG W1 %s
mode: single
trigger:
  - platform: time
    at: "03:00:00"
action:
%s`, id, id, actions)
}

// w1TurnOn is the plainest switching action, as one list item at indent 2.
const w1TurnOn = `  - action: input_boolean.turn_on
    target:
      entity_id: ` + w1Entity + `
`

func w1Fill(candidate, entity string) string {
	return strings.ReplaceAll(candidate, w1Entity, entity)
}

// assertW1TwinAllowed: the twin's dry run passes under the profile with the
// policy line — the precondition that makes the refused candidate's refusal
// mean something.
func assertW1TwinAllowed(t *testing.T, dir string, args ...string) {
	t.Helper()
	stdout, stderr, code := runPolicyE2E(t, dir, nil, args...)
	if code != 0 || !strings.Contains(stdout, "checked, allowed (profile "+pgE2EProfile+")") {
		t.Fatalf("fixture: the allowed twin's dry run hactl %v exit %d, want 0 with `policy: <n> checked, allowed (profile %s)` — the profile or the YAML is broken, not the feature\nstdout:\n%s\nstderr:\n%s",
			args, code, pgE2EProfile, stdout, stderr)
	}
}

// assertW1PreviewAllowed: the dry run passes under the profile — an
// expectation of the contract, not a fixture check.
func assertW1PreviewAllowed(t *testing.T, dir string, args ...string) {
	t.Helper()
	stdout, stderr, code := runPolicyE2E(t, dir, nil, args...)
	if code != 0 {
		t.Errorf("hactl %v: exit %d, want 0 — the contract allows this candidate\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
		return
	}
	if !strings.Contains(stdout, "checked, allowed (profile "+pgE2EProfile+")") {
		t.Errorf("hactl %v: allowed preview lacks `policy: <n> checked, allowed (profile %s)`:\n%s", args, pgE2EProfile, stdout)
	}
}

// haConfigStatus asks HA's own config API for an automation or script
// (kind "automation" / "script") and returns the HTTP status — 404 is "HA
// holds no such config", the proof a refused create wrote nothing.
func haConfigStatus(t *testing.T, kind, id string) (int, string) {
	t.Helper()
	code, body := w1HARest(t, http.MethodGet, "/api/config/"+kind+"/config/"+url.PathEscape(id), nil)
	return code, string(body)
}

func assertW1NotInHA(t *testing.T, kind, id string) {
	t.Helper()
	if code, body := haConfigStatus(t, kind, id); code != http.StatusNotFound {
		t.Errorf("a refused write left %s %s in HA: GET /api/config/%s/config/%s = HTTP %d %s", kind, id, kind, id, code, body)
	}
}

// w1HARest sends one request to HA's REST API with the owner's token, outside
// hactl, and returns the status and body.
func w1HARest(t *testing.T, method, path string, body any) (int, json.RawMessage) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, haURL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+haToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading body: %v", method, path, err)
	}
	return resp.StatusCode, raw
}

// waitForAutomationInHA polls HA's config API until it serves id.
func waitForAutomationInHA(t *testing.T, id string) []byte {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		body, err := haAutomationConfig(context.Background(), id)
		if err == nil {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture: HA never served automation %s: %v", id, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// seedW1Automation creates an automation as the owner, through the tier's own
// policy-free instance directory, and waits for HA to serve it.
func seedW1Automation(t *testing.T, id, content string) {
	t.Helper()
	_, _ = testClient.DeleteAutomationDef(context.Background(), id)
	file := writeTempYAML(t, id+".yaml", content)
	if out, err := runHactlE2E(t, "auto", "create", "-f", file, "--confirm"); err != nil {
		t.Fatalf("fixture: seeding automation %s as the owner: %v\n%s", id, err, out)
	}
	t.Cleanup(func() { _, _ = testClient.DeleteAutomationDef(context.Background(), id) })
	waitForAutomationInHA(t, id)
}

// cleanupW1Automation removes id should a refusal have let it through.
func cleanupW1Automation(t *testing.T, id string) {
	t.Helper()
	_, _ = testClient.DeleteAutomationDef(context.Background(), id)
	t.Cleanup(func() { _, _ = testClient.DeleteAutomationDef(context.Background(), id) })
}

// ---------------------------------------------------------------------------
// Case 1 — E-3 / D-56 / D-133: an automation whose action switches a denied
// entity is refused in the dry run and under --confirm.
// ---------------------------------------------------------------------------

func TestE2EPolicyIndirectSpecAutoCreateSwitchingDeniedEntityCLI(t *testing.T) {
	const id = "pg_w1_auto_direct"
	seedW1Booleans(t)
	cleanupW1Automation(t, id)
	policy := w1Profile("read, write", "")
	dir := policyE2EDir(t, policy)
	candidate := w1AutoYAML(id, w1TurnOn)

	twin := writeTempYAML(t, "twin.yaml", w1Fill(candidate, w1Allowed))
	assertW1TwinAllowed(t, dir, "auto", "create", "-f", twin)

	file := writeTempYAML(t, "auto.yaml", w1Fill(candidate, w1Guarded))
	assertE2ERefused(t, dir, []string{"auto", "create", "-f", file},
		w1GuardedDenial(t, policy, "control"))
	assertW1NotInHA(t, "automation", id)
}

// ---------------------------------------------------------------------------
// Case 2 — "every action in it — at any depth": choose, if/then, if/else,
// parallel, repeat, sequence; the legacy `service:` key; the entity named in
// data or at the action's top level instead of in target (W-1 contract:
// data ∪ target ∪ top-level entity_id).
// ---------------------------------------------------------------------------

func TestE2EPolicyIndirectSpecNestedActionsCLI(t *testing.T) {
	seedW1Booleans(t)
	policy := w1Profile("read, write", "")
	dir := policyE2EDir(t, policy)
	want := w1GuardedDenial(t, policy, "control")

	for _, tc := range []struct{ name, actions string }{
		{"choose", `  - choose:
      - conditions:
          - condition: template
            value_template: "{{ true }}"
        sequence:
          - action: input_boolean.turn_on
            target:
              entity_id: ` + w1Entity + `
`},
		{"if_then", `  - if:
      - condition: template
        value_template: "{{ true }}"
    then:
      - action: input_boolean.turn_on
        target:
          entity_id: ` + w1Entity + `
`},
		{"if_else", `  - if:
      - condition: template
        value_template: "{{ false }}"
    then:
      - delay: "00:00:01"
    else:
      - action: input_boolean.turn_on
        target:
          entity_id: ` + w1Entity + `
`},
		{"parallel", `  - parallel:
      - delay: "00:00:01"
      - action: input_boolean.turn_on
        target:
          entity_id: ` + w1Entity + `
`},
		{"repeat", `  - repeat:
      count: 2
      sequence:
        - action: input_boolean.toggle
          target:
            entity_id: ` + w1Entity + `
`},
		{"sequence", `  - sequence:
      - delay: "00:00:01"
      - action: input_boolean.turn_on
        target:
          entity_id: ` + w1Entity + `
`},
		{"choose_in_parallel_in_if", `  - if:
      - condition: template
        value_template: "{{ true }}"
    then:
      - parallel:
          - choose:
              - conditions:
                  - condition: template
                    value_template: "{{ true }}"
                sequence:
                  - action: input_boolean.turn_on
                    target:
                      entity_id: ` + w1Entity + `
`},
		{"legacy_service_key", `  - service: input_boolean.turn_on
    target:
      entity_id: ` + w1Entity + `
`},
		{"entity_in_data", `  - action: input_boolean.turn_on
    data:
      entity_id: ` + w1Entity + `
`},
		{"top_level_entity_id", `  - action: input_boolean.turn_on
    entity_id: ` + w1Entity + `
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "pg_w1_nest_" + tc.name
			cleanupW1Automation(t, id)
			candidate := w1AutoYAML(id, tc.actions)
			twin := writeTempYAML(t, "twin.yaml", w1Fill(candidate, w1Allowed))
			assertW1TwinAllowed(t, dir, "auto", "create", "-f", twin)

			file := writeTempYAML(t, "auto.yaml", w1Fill(candidate, w1Guarded))
			assertE2ERefused(t, dir, []string{"auto", "create", "-f", file}, want)
			assertW1NotInHA(t, "automation", id)
		})
	}
}

// ---------------------------------------------------------------------------
// Case 3 — reach: `target: {area_id: …}` naming an area that holds the denied
// entity is refused like naming the entity (E-1 reach, the svc call path).
// ---------------------------------------------------------------------------

// createW1Area creates an area as the owner over HA's WebSocket API, puts
// entityID into it, and removes the area on cleanup.
func createW1Area(t *testing.T, name, entityID string) string {
	t.Helper()
	var area struct {
		AreaID string `json:"area_id"`
	}
	if err := json.Unmarshal(haWSCommand(t, "config/area_registry/create", map[string]any{"name": name}), &area); err != nil || area.AreaID == "" {
		t.Fatalf("fixture: creating area %q: %v (id %q)", name, err, area.AreaID)
	}
	t.Cleanup(func() { haWSCommand(t, "config/area_registry/delete", map[string]any{"area_id": area.AreaID}) })
	haWSCommand(t, "config/entity_registry/update", map[string]any{"entity_id": entityID, "area_id": area.AreaID})
	return area.AreaID
}

func TestE2EPolicyIndirectSpecAreaTargetCLI(t *testing.T) {
	const id = "pg_w1_auto_area"
	seedW1Booleans(t)
	cleanupW1Automation(t, id)
	guardedArea := createW1Area(t, "PG W1 Guarded Area", w1Guarded)
	allowedArea := createW1Area(t, "PG W1 Allowed Area", w1Allowed)
	policy := w1Profile("read, write", "")
	dir := policyE2EDir(t, policy)
	candidate := w1AutoYAML(id, `  - action: input_boolean.turn_on
    target:
      area_id: `+w1Entity+`
`)

	twin := writeTempYAML(t, "twin.yaml", w1Fill(candidate, allowedArea))
	assertW1TwinAllowed(t, dir, "auto", "create", "-f", twin)

	file := writeTempYAML(t, "auto.yaml", w1Fill(candidate, guardedArea))
	assertE2ERefused(t, dir, []string{"auto", "create", "-f", file},
		w1GuardedDenial(t, policy, "control"))
	assertW1NotInHA(t, "automation", id)
}

// ---------------------------------------------------------------------------
// Case 4 — scripts and template entities are candidates too: a script whose
// sequence switches the denied entity, and a template switch / lock whose
// turn_on / unlock action does.
// ---------------------------------------------------------------------------

func TestE2EPolicyIndirectSpecScriptCreateCLI(t *testing.T) {
	const id = "pg_w1_script"
	seedW1Booleans(t)
	_, _ = testClient.DeleteScriptDef(context.Background(), id)
	t.Cleanup(func() { _, _ = testClient.DeleteScriptDef(context.Background(), id) })
	policy := w1Profile("read, write", "")
	dir := policyE2EDir(t, policy)
	candidate := id + `:
  alias: PG W1 Script
  mode: single
  sequence:
    - delay: "00:00:01"
    - action: input_boolean.turn_off
      target:
        entity_id: ` + w1Entity + `
`

	twin := writeTempYAML(t, "twin.yaml", w1Fill(candidate, w1Allowed))
	assertW1TwinAllowed(t, dir, "script", "create", "-f", twin)

	file := writeTempYAML(t, "script.yaml", w1Fill(candidate, w1Guarded))
	assertE2ERefused(t, dir, []string{"script", "create", "-f", file},
		w1GuardedDenial(t, policy, "control"))
	assertW1NotInHA(t, "script", id)
}

func TestE2EPolicyIndirectSpecTemplateCreateCLI(t *testing.T) {
	seedW1Booleans(t)
	policy := w1Profile("read, write", "")
	dir := policyE2EDir(t, policy)

	// Full blocks: a bare entity item whose own keys include `lock:` reads as
	// a block (observed 2026-11-05: `tpl create --domain lock` refused it as
	// `template entity without unique_id`), so both are written as blocks.
	for _, tc := range []struct{ domain, uniqueID, item string }{
		{"switch", "pg_w1_tpl_switch", `switch:
  - name: PG W1 Tpl Switch
    unique_id: pg_w1_tpl_switch
    turn_on:
      - action: input_boolean.turn_on
        target:
          entity_id: ` + w1Entity + `
    turn_off:
      - action: input_boolean.turn_off
        target:
          entity_id: ` + w1Allowed + `
`},
		{"lock", "pg_w1_tpl_lock", `lock:
  - name: PG W1 Tpl Lock
    unique_id: pg_w1_tpl_lock
    lock:
      - action: input_boolean.turn_on
        target:
          entity_id: ` + w1Allowed + `
    unlock:
      - action: input_boolean.turn_off
        target:
          entity_id: ` + w1Entity + `
`},
	} {
		t.Run(tc.domain, func(t *testing.T) {
			_, _ = testClient.DeleteTemplate(context.Background(), tc.uniqueID)
			t.Cleanup(func() { _, _ = testClient.DeleteTemplate(context.Background(), tc.uniqueID) })

			twin := writeTempYAML(t, "twin.yaml", w1Fill(tc.item, w1Allowed))
			assertW1TwinAllowed(t, dir, "tpl", "create", "-f", twin)

			file := writeTempYAML(t, "tpl.yaml", w1Fill(tc.item, w1Guarded))
			assertE2ERefused(t, dir, []string{"tpl", "create", "-f", file},
				w1GuardedDenial(t, policy, "control"))
			if content := readConfigFileE2E(t, "template.yaml"); strings.Contains(content, tc.uniqueID) {
				t.Errorf("a refused tpl create wrote %s into template.yaml:\n%s", tc.uniqueID, content)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Case 5 — M-7 / D-133: an entity the candidate only mentions (a state
// trigger) needs read, not control. Denied read: refused, naming read.
// Allowed read but denied control: the candidate is allowed.
// ---------------------------------------------------------------------------

func TestE2EPolicyIndirectSpecTriggerNeedsReadNotControlCLI(t *testing.T) {
	const id = "pg_w1_auto_watch"
	seedW1Booleans(t)
	cleanupW1Automation(t, id)
	candidate := fmt.Sprintf(`id: %s
alias: PG W1 Watch
mode: single
trigger:
  - platform: state
    entity_id: %s
    to: "on"
action:
  - action: input_boolean.turn_on
    target:
      entity_id: %s
`, id, w1Entity, w1Allowed)
	file := writeTempYAML(t, "auto.yaml", w1Fill(candidate, w1Guarded))

	readDenied := w1Profile("read, write", "")
	readDeniedDir := policyE2EDir(t, readDenied)
	// The twin watches the allowed boolean instead: same profile, allowed.
	twin := writeTempYAML(t, "twin.yaml", w1Fill(candidate, w1Allowed))
	assertW1TwinAllowed(t, readDeniedDir, "auto", "create", "-f", twin)

	assertE2ERefused(t, readDeniedDir, []string{"auto", "create", "-f", file},
		w1GuardedDenial(t, readDenied, "read"))
	assertW1NotInHA(t, "automation", id)

	// Watching is not switching: with read allowed (entity:* [read]) and only
	// control denied, the same candidate passes its dry run.
	controlDenied := w1Profile("control", "")
	assertW1PreviewAllowed(t, policyE2EDir(t, controlDenied), "auto", "create", "-f", file)
}

// ---------------------------------------------------------------------------
// Case 6 — D-134: the whole candidate is checked. An unrelated edit (the
// alias) of a stored automation that already switches the denied entity is
// refused.
// ---------------------------------------------------------------------------

func TestE2EPolicyIndirectSpecAutoApplyChecksWholeCandidateCLI(t *testing.T) {
	const id = "pg_w1_auto_apply"
	seedW1Booleans(t)
	stored := w1AutoYAML(id, w1Fill(w1TurnOn, w1Guarded))
	seedW1Automation(t, id, stored)
	aliasInHA := func() string {
		raw, err := haAutomationConfig(context.Background(), id)
		if err != nil {
			t.Fatalf("reading %s from HA: %v", id, err)
		}
		var cfg struct {
			Alias string `json:"alias"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("automation config did not parse: %v", err)
		}
		return cfg.Alias
	}
	storedAlias := aliasInHA()

	policy := w1Profile("read, write", "")
	dir := policyE2EDir(t, policy)
	edited := func(entity string) string {
		return strings.Replace(w1AutoYAML(id, w1Fill(w1TurnOn, entity)), "alias: PG W1 "+id, "alias: PG W1 Apply Edited", 1)
	}

	// The twin also moves the action to the allowed boolean: a candidate that
	// no longer switches the denied entity is an allowed edit.
	twin := writeTempYAML(t, "twin.yaml", edited(w1Allowed))
	assertW1TwinAllowed(t, dir, "auto", "apply", id, "-f", twin)

	file := writeTempYAML(t, "apply.yaml", edited(w1Guarded))
	assertE2ERefused(t, dir, []string{"auto", "apply", id, "-f", file},
		w1GuardedDenial(t, policy, "control"))
	if got := aliasInHA(); got != storedAlias {
		t.Errorf("a refused apply changed %s's alias from %q to %q", id, storedAlias, got)
	}
}

// ---------------------------------------------------------------------------
// Case 7 — what hactl cannot read off the candidate refuses it: a templated
// target or a templated service name is `fail-closed: action target is a
// template`.
// ---------------------------------------------------------------------------

func TestE2EPolicyIndirectSpecTemplatedTargetFailsClosedCLI(t *testing.T) {
	seedW1Booleans(t)
	dir := policyE2EDir(t, w1Profile("read, write", ""))

	// The manual names one rule per undecidable part: a templated target and
	// a templated service name (clarified 2026-11-05 after this test found
	// the contract gave one text for both).
	for _, tc := range []struct{ name, actions, rule string }{
		{"expand_target", `  - action: input_boolean.turn_on
    target:
      entity_id: "{{ expand('group.pg_w1_group') | map(attribute='entity_id') | list }}"
`, "fail-closed: action target is a template"},
		{"templated_service", `  - action: "{{ 'input_boolean.turn_' ~ 'on' }}"
    target:
      entity_id: ` + w1Allowed + `
`, "fail-closed: action service is a template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "pg_w1_tmpl_" + tc.name
			cleanupW1Automation(t, id)
			file := writeTempYAML(t, "auto.yaml", w1AutoYAML(id, tc.actions))
			args := []string{"auto", "create", "-f", file}

			assertE2ERefused(t, dir, args)
			for _, confirm := range []bool{false, true} {
				a := append(slices.Clone(args), "--json")
				if confirm {
					a = append(a, "--confirm")
				}
				out, _, code := runPolicyE2E(t, dir, nil, a...)
				if code != 1 {
					t.Errorf("hactl %v: exit %d, want 1\n%s", a, code, out)
					continue
				}
				r := decodeE2ERefusal(t, fmt.Sprintf("hactl %v", a), out, !confirm)
				if !slices.ContainsFunc(r.Details.Policy.Denied, func(d e2eDenied) bool {
					return strings.HasPrefix(d.Rule, tc.rule) && d.Line == 0
				}) {
					t.Errorf("hactl %v: no `%s` denial (line 0) in %+v", a, tc.rule, r.Details.Policy.Denied)
				}
			}
			assertW1NotInHA(t, "automation", id)
		})
	}
}

// ---------------------------------------------------------------------------
// Case 8 — D-137: a device action (device_id, domain, type, no `action:`)
// needs control on the entity it names. HA names it by the entity registry's
// uuid; an entity_id is taken as it is; a uuid hactl cannot resolve refuses.
//
// The tier's stock HA has no switchable entity on a device, so the owner makes
// one: a template switch config entry (HA's own template helper flow) attached
// to an existing device.
// ---------------------------------------------------------------------------

type w1DeviceSwitch struct {
	entityID, uuid, deviceID string
}

// w1AnyDevice returns the id of an enabled device of the tier's HA.
func w1AnyDevice(t *testing.T) string {
	t.Helper()
	var devices []struct {
		ID         string  `json:"id"`
		DisabledBy *string `json:"disabled_by"`
	}
	if err := json.Unmarshal(haWSCommand(t, "config/device_registry/list", nil), &devices); err != nil {
		t.Fatalf("fixture: device registry: %v", err)
	}
	for _, d := range devices {
		if d.ID != "" && d.DisabledBy == nil {
			return d.ID
		}
	}
	t.Fatalf("fixture: the tier's HA has no device to attach a template switch to")
	return ""
}

// w1FlowStep posts one step of a config flow to HA's REST API.
func w1FlowStep(t *testing.T, path string, body map[string]any) map[string]any {
	t.Helper()
	code, raw := w1HARest(t, http.MethodPost, path, body)
	var m map[string]any
	if code != http.StatusOK || json.Unmarshal(raw, &m) != nil {
		t.Fatalf("fixture: POST %s %v: HTTP %d %s", path, body, code, raw)
	}
	return m
}

// w1TemplateSwitchEntry runs HA's template helper flow for a switch attached
// to deviceID and returns the config entry id, deleted on cleanup.
func w1TemplateSwitchEntry(t *testing.T, deviceID string) string {
	t.Helper()
	start := w1FlowStep(t, "/api/config/config_entries/flow", map[string]any{"handler": "template", "show_advanced_options": false})
	flowID, _ := start["flow_id"].(string)
	if flowID == "" {
		t.Fatalf("fixture: template flow did not start: %v", start)
	}
	path := "/api/config/config_entries/flow/" + flowID
	if step := w1FlowStep(t, path, map[string]any{"next_step_id": "switch"}); step["step_id"] != "switch" {
		t.Fatalf("fixture: template flow has no switch step: %v", step)
	}
	done := w1FlowStep(t, path, map[string]any{"name": "PG W1 Device Switch", "device_id": deviceID})
	result, _ := done["result"].(map[string]any)
	entryID, _ := result["entry_id"].(string)
	if done["type"] != "create_entry" || entryID == "" {
		t.Fatalf("fixture: template switch flow did not create an entry: %v", done)
	}
	t.Cleanup(func() {
		w1HARest(t, http.MethodDelete, "/api/config/config_entries/entry/"+entryID, nil)
	})
	return entryID
}

// w1EntryEntity polls HA's entity registry for the entity entryID owns.
func w1EntryEntity(t *testing.T, entryID string) w1DeviceSwitch {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var rows []struct {
			ID            string `json:"id"`
			EntityID      string `json:"entity_id"`
			DeviceID      string `json:"device_id"`
			ConfigEntryID string `json:"config_entry_id"`
		}
		if err := json.Unmarshal(haWSCommand(t, "config/entity_registry/list", nil), &rows); err != nil {
			t.Fatalf("fixture: entity registry: %v", err)
		}
		for _, r := range rows {
			if r.ConfigEntryID == entryID {
				return w1DeviceSwitch{entityID: r.EntityID, uuid: r.ID, deviceID: r.DeviceID}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture: the template switch entry %s never registered an entity", entryID)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func createW1DeviceSwitch(t *testing.T) w1DeviceSwitch {
	t.Helper()
	deviceID := w1AnyDevice(t)
	sw := w1EntryEntity(t, w1TemplateSwitchEntry(t, deviceID))
	// HA prefixes the device's name (switch.sun_pg_w1_…); the owner renames
	// it to a stable id. The registry uuid does not change.
	const stable = "switch.pg_w1_device_switch"
	if sw.entityID != stable {
		haWSCommand(t, "config/entity_registry/update", map[string]any{"entity_id": sw.entityID, "new_entity_id": stable})
		sw.entityID = stable
	}
	if !strings.HasPrefix(sw.entityID, "switch.pg_w1_") || sw.deviceID != deviceID || sw.uuid == "" {
		t.Fatalf("fixture: template switch registered as %+v, want switch.pg_w1_* on device %s with a uuid", sw, deviceID)
	}
	// What HA itself offers as the device action's entity_id (D-137's oracle),
	// logged so a reader sees the uuid form is the real one.
	var actions []map[string]any
	if err := json.Unmarshal(haWSCommand(t, "device_automation/action/list", map[string]any{"device_id": deviceID}), &actions); err == nil {
		for _, a := range actions {
			if a["domain"] == "switch" {
				t.Logf("HA offers device action %v (%s has uuid %s)", a, sw.entityID, sw.uuid)
			}
		}
	}
	return sw
}

func TestE2EPolicyIndirectSpecDeviceActionCLI(t *testing.T) {
	seedW1Booleans(t)
	sw := createW1DeviceSwitch(t)
	deviceAction := func(ref string) string {
		return fmt.Sprintf(`  - device_id: %s
    domain: switch
    entity_id: %s
    type: turn_on
`, sw.deviceID, ref)
	}

	base := w1Profile("read, write", "")
	baseDir := policyE2EDir(t, base)
	policy := w1Profile("read, write", fmt.Sprintf("      %q: [write]\n", "entity:"+sw.entityID))
	dir := policyE2EDir(t, policy)
	want := e2eDenied{
		Resource: "entity:" + sw.entityID, Verb: "control",
		Rule: fmt.Sprintf("deny %q [write]", "entity:"+sw.entityID),
		Line: policyLineOfE2E(t, policy, fmt.Sprintf("%q", "entity:"+sw.entityID)),
	}

	for _, tc := range []struct{ name, ref string }{
		{"registry_uuid", sw.uuid},
		{"entity_id", sw.entityID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "pg_w1_device_" + tc.name
			cleanupW1Automation(t, id)
			file := writeTempYAML(t, "auto.yaml", w1AutoYAML(id, deviceAction(tc.ref)))
			// The same candidate without the switch's deny: allowed.
			assertW1TwinAllowed(t, baseDir, "auto", "create", "-f", file)
			assertE2ERefused(t, dir, []string{"auto", "create", "-f", file}, want)
			assertW1NotInHA(t, "automation", id)
		})
	}

	t.Run("unresolvable_uuid", func(t *testing.T) {
		const id = "pg_w1_device_unknown"
		cleanupW1Automation(t, id)
		file := writeTempYAML(t, "auto.yaml", w1AutoYAML(id, deviceAction("0123456789abcdef0123456789abcdef")))
		args := []string{"auto", "create", "-f", file, "--json"}
		out, errOut, code := runPolicyE2E(t, baseDir, nil, args...)
		if code != 1 {
			t.Fatalf("hactl %v: exit %d, want 1 — a device action naming no entity HA knows is refused (D-137)\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
		// HA's own validation already rejects an unknown uuid in the dry run
		// (observed 2026-11-05 before W-1: "HA rejected the action config:
		// Unknown entity …", empty stdout). The contract does not order that
		// check against the policy's, so either refusal satisfies D-137; a
		// policy refusal must be the fail-closed one, not a rule's.
		if strings.Contains(out, `"policy_denied"`) {
			r := decodeE2ERefusal(t, fmt.Sprintf("hactl %v", args), out, true)
			if !slices.ContainsFunc(r.Details.Policy.Denied, func(d e2eDenied) bool {
				return strings.HasPrefix(d.Rule, "fail-closed: ") && d.Line == 0
			}) {
				t.Errorf("hactl %v: no fail-closed denial in %+v", args, r.Details.Policy.Denied)
			}
		}
		assertW1NotInHA(t, "automation", id)
	})
}

// ---------------------------------------------------------------------------
// Case 9 — D-135: `indirect: unchecked` switches the check off for its
// profile, and `policy show` says so on its own line.
// ---------------------------------------------------------------------------

func TestE2EPolicyIndirectSpecUncheckedOptOutCLI(t *testing.T) {
	const id = "pg_w1_auto_unchecked"
	seedW1Booleans(t)
	cleanupW1Automation(t, id)
	policy := w1Profile("read, write", "    indirect: unchecked\n")
	dir := policyE2EDir(t, policy)

	file := writeTempYAML(t, "auto.yaml", w1AutoYAML(id, w1Fill(w1TurnOn, w1Guarded)))
	assertW1PreviewAllowed(t, dir, "auto", "create", "-f", file)

	stdout, stderr, code := runPolicyE2E(t, dir, nil, "policy", "show")
	if code != 0 {
		t.Fatalf("policy show: exit %d, want 0 — `indirect: unchecked` is a valid profile key (D-135)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !slices.ContainsFunc(strings.Split(stdout, "\n"), func(l string) bool {
		return strings.Contains(l, "indirect") && strings.Contains(l, "unchecked") && !strings.HasPrefix(l, "policy_broken")
	}) {
		t.Errorf("policy show has no line saying indirect: unchecked (D-135):\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// Case 10 — W-1: `ref replace` needs control on the new id wherever a change
// lies inside an automation, script or template entry.
// ---------------------------------------------------------------------------

func TestE2EPolicyIndirectSpecRefReplaceNeedsControlOnNewIDCLI(t *testing.T) {
	const id = "pg_w1_auto_rr"
	seedW1Booleans(t)
	seedW1Automation(t, id, w1AutoYAML(id, w1Fill(w1TurnOn, w1Allowed)))

	// No default: deny — the rewrite's file and typed edits are allowed, so
	// only the new id's control can refuse it.
	policy := pgE2EPolicy(fmt.Sprintf("    deny:\n      %q: [read, write]\n", "entity:"+w1Guarded))
	dir := policyE2EDir(t, policy)
	assertE2ERefused(t, dir, []string{"ref", "replace", w1Allowed, w1Guarded},
		w1GuardedDenial(t, policy, "control"))

	cfg, err := haAutomationConfig(context.Background(), id)
	if err != nil {
		t.Fatalf("reading %s from HA: %v", id, err)
	}
	if !strings.Contains(string(cfg), w1Allowed) || strings.Contains(string(cfg), w1Guarded) {
		t.Errorf("a refused ref replace rewrote %s: %s", id, cfg)
	}
	if content := readConfigFileE2E(t, "automations.yaml"); strings.Contains(content, w1Guarded) {
		t.Errorf("a refused ref replace wrote %s into automations.yaml:\n%s", w1Guarded, content)
	}
}

// ---------------------------------------------------------------------------
// Case 11 — D-136: HACTL_POLICY=required in .env. A missing policy.yaml, or
// one that selects no profile, refuses every command but the D-60 set.
// ---------------------------------------------------------------------------

// requiredPolicyDir is an instance directory whose .env says
// HACTL_POLICY=required, holding policy as policy.yaml (none when "").
func requiredPolicyDir(t *testing.T, policy string) string {
	t.Helper()
	dir, err := createE2EInstanceDir(haURL, haToken, compURL, companionToken)
	if err != nil {
		t.Fatalf("creating instance dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	env := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=%s\nCOMPANION_URL=%s\nCOMPANION_TOKEN=%s\nHACTL_POLICY=required\n",
		haURL, haToken, compURL, companionToken)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if policy != "" {
		writePolicyE2E(t, dir, policy)
	}
	return dir
}

// assertPolicyMissing: exit 1 and the policy_missing line on stderr.
func assertPolicyMissing(t *testing.T, dir string, args ...string) {
	t.Helper()
	stdout, stderr, code := runPolicyE2E(t, dir, nil, args...)
	if code != 1 {
		t.Errorf("hactl %v: exit %d, want 1 — this instance requires a policy\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
		return
	}
	if !slices.ContainsFunc(strings.Split(stderr, "\n"), func(l string) bool {
		return strings.HasPrefix(l, "policy_missing: ") && strings.Contains(l, "policy.yaml") &&
			strings.HasSuffix(l, " — this instance requires a policy; the owner restores it")
	}) {
		t.Errorf("hactl %v: stderr has no `policy_missing: <path> — this instance requires a policy; the owner restores it` line:\n%s", args, stderr)
	}
}

func TestE2EPolicyIndirectSpecRequiredPolicyCLI(t *testing.T) {
	seedW1Booleans(t)
	turnOff := func() {
		w1HARest(t, http.MethodPost, "/api/services/input_boolean/turn_off", map[string]any{"entity_id": w1Guarded})
	}
	t.Cleanup(turnOff)
	data := `{"entity_id":"` + w1Guarded + `"}`

	for _, tc := range []struct{ name, policy string }{
		{"no policy.yaml", ""},
		{"no profile selected", "version: 1\nprofiles:\n  other:\n    deny:\n      \"entity:lock.*\": [write]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turnOff() // an earlier subtest's svc call may have switched it
			if state, _, _ := haStateOf(t, w1Guarded); state != "off" {
				t.Fatalf("fixture: %s is %q before the subtest, want off", w1Guarded, state)
			}
			dir := requiredPolicyDir(t, tc.policy)
			assertPolicyMissing(t, dir, "svc", "call", "input_boolean.turn_on", "-d", data)
			assertPolicyMissing(t, dir, "svc", "call", "input_boolean.turn_on", "-d", data, "--confirm")
			assertPolicyMissing(t, dir, "ent", "ls")
			if state, _, _ := haStateOf(t, w1Guarded); state != "off" {
				t.Errorf("a refused svc call switched %s: state %q, want off", w1Guarded, state)
			}

			// The D-60 set still runs.
			if stdout, stderr, code := runPolicyE2E(t, dir, nil, "version"); code != 0 {
				t.Errorf("version: exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if stdout, stderr, _ := runPolicyE2E(t, dir, nil, "policy", "show"); strings.TrimSpace(stdout) == "" {
				t.Errorf("policy show printed no report — it is exempt from the refusal\nstderr:\n%s", stderr)
			}
		})
	}
}
