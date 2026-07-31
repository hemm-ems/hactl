//go:build livefire

package livefire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// H-2's first half, on the two commands that shipped as `proven` on the
// confirm surface while accepting values the confirmed run cannot use. Each
// case asserts the PARITY rather than a message: what the preview refuses and
// what --confirm refuses are one set, and a case that only pinned the wording
// would pass against a preview that refuses everything.

// Finding #96: five malformed or cross-domain new ids each previewed as
// "dry-run: would rename entity … references: 2" at exit 0, and
// `config/entity_registry/update` answers "Invalid entity ID" or "New entity
// ID should be same domain" to every one of them.
//
// The corpus is HA's regex clause by clause, not the five reported strings:
// the report found the shapes its author happened to type.
func TestSweepEntRenameRefusesWhatHomeAssistantRefuses(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		source := renameSource(t, tgt)
		if source == "" {
			t.Skip("no pg_ registry entity on this profile to rename from")
		}
		domain, _, _ := strings.Cut(source, ".")
		for _, bad := range []string{
			domain + ".pg w5 bad",  // a space
			domain + ".PG_w5_Bad!", // uppercase and punctuation
			domain + ".pg_w5_🔥bad", // a multi-byte character
			domain + ".pg.w5.bad",  // more than one dot
			domain + ".pg__w5",     // a doubled underscore
			domain + ".pg_w5_",     // a trailing underscore
			"switch.pg_w5_renamed", // a different domain
		} {
			out, err := tgt.Read(t, "ent", "rename", source, bad)
			if err == nil {
				t.Errorf("`ent rename %s %q` previewed at exit 0; HA refuses this id at confirm time:\n%s",
					source, bad, out)
			}
			if strings.Contains(out, "dry-run") {
				t.Errorf("`ent rename %s %q` printed a PLAN before refusing:\n%s", source, bad, out)
			}
		}
		// The control. Without it every assertion above is satisfied by a
		// preview that refuses everything, which is H-2 pointing the other way.
		//
		// It asserts on the REFUSAL, not on the plan, because `ent rename`
		// needs the companion for its reference half and the rig has none
		// (R11): there the valid id gets as far as the connection and fails
		// on that instead. Either way the shape gate must not have fired.
		out, err := tgt.ReadDiagnostic(t, "ent", "rename", source, domain+".pg_wp4_probe_target")
		for _, refusal := range []string{"not one Home Assistant accepts", "same domain"} {
			if strings.Contains(out, refusal) {
				t.Errorf("a valid same-domain rename was refused as %q — the gate is now too wide:\n%s",
					refusal, out)
			}
		}
		if err != nil && !strings.Contains(out, "companion") {
			t.Errorf("a valid rename failed for a reason that is neither the companion nor the shape gate:\n%s", out)
		}
	})
}

// Finding #42: `svc call automation.trigger --data '{"target":{…}}'` printed a
// clean plan and --confirm answered 400. HA validates service data with
// PREVENT_EXTRA, so an undeclared key is refused — and `target:` is the shape
// HA's own YAML documentation shows, which is why an agent writes it.
//
// The second half of the case is the one that keeps the fix honest: three
// payloads HA answers 200 to must still preview.
func TestSweepSvcCallRefusesWhatHomeAssistantRefuses(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, tc := range []struct {
			service, data string
		}{
			{"automation.trigger", `{"target":{"entity_id":["automation.pg_core_auto_counter"]}}`},
			{"input_boolean.toggle", `{"entity_id":"input_boolean.pg_probe","bogus_key_xyz":1}`},
			{"input_boolean.toggle", `{"entity_id":"not_an_entity_id"}`},
		} {
			out, err := tgt.Read(t, "svc", "call", tc.service, "--data", tc.data)
			if err == nil {
				t.Errorf("`svc call %s --data %s` previewed at exit 0; HA answers 400:\n%s",
					tc.service, tc.data, out)
			}
			if strings.Contains(out, "dry-run") {
				t.Errorf("`svc call %s --data %s` printed a PLAN before refusing:\n%s", tc.service, tc.data, out)
			}
		}
		for _, tc := range []struct {
			name, service, data string
		}{
			{"an entity that does not exist", "input_boolean.toggle", `{"entity_id":"input_boolean.pg_absent_probe"}`},
			{"a declared field", "automation.trigger", `{"entity_id":"automation.pg_absent","skip_condition":true}`},
			{"no data at all", "input_boolean.toggle", `{}`},
		} {
			out, err := tgt.Read(t, "svc", "call", tc.service, "--data", tc.data)
			if err != nil {
				t.Errorf("%s: `svc call %s --data %s` was refused, and HA answers 200 to it:\n%s",
					tc.name, tc.service, tc.data, out)
			}
		}
	})
}

// Finding #44: a targetless call previewed identically to a single-entity one.
// The report read that as an unflagged domain-wide broadcast; HA's target
// extraction says the opposite — no selector selects no entity — and
// `entity_id: all` is the broadcast. The preview states both.
func TestSweepSvcCallPreviewStatesItsReach(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		none := tgt.MustRead(t, "svc", "call", "input_boolean.toggle")
		if !strings.Contains(none, "targets:") || !strings.Contains(none, "reach nothing") {
			t.Errorf("a targetless call does not say what it would reach:\n%s", none)
		}
		all := tgt.MustRead(t, "svc", "call", "input_boolean.toggle", "--data", `{"entity_id":"all"}`)
		if !strings.Contains(all, "EVERY entity") {
			t.Errorf("`entity_id: all` does not say it reaches the whole domain:\n%s", all)
		}
		// A single-entity call has nothing notable to say, and saying something
		// anyway would make the two lines above unreadable as warnings.
		one := tgt.MustRead(t, "svc", "call", "input_boolean.toggle", "--data",
			`{"entity_id":"input_boolean.pg_absent_probe"}`)
		if strings.Contains(one, "targets:") {
			t.Errorf("an ordinary targeted call carries a reach line it does not need:\n%s", one)
		}
	})
}

// Finding #43: a confirmed call reported "called input_boolean.toggle" at exit
// 0 whether it had changed an entity or matched nothing, because the response
// body — HA's own list of the states it attributed to the call — was discarded.
//
// This is a WRITE case: it toggles a pg_ input_boolean twice, so the instance
// ends where it started. The rig profile has no pg_ helper of its own, so it
// runs against whichever the fixture provides.
func TestSweepSvcCallReportsWhatChanged(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		target := pgInputBoolean(t, tgt)
		if target == "" {
			t.Skip("no pg_ input_boolean on this profile")
		}
		vocab := []string{"svc", "call", "input_boolean.toggle", "--data", "--confirm", "--json"}
		payload := `{"entity_id":"` + target + `"}`

		// Dry run first: it is the sequence the manual teaches, and it is what
		// delivers the family how-to that confirmGuard requires before any
		// --confirm. A case that skipped it would be testing the guard.
		if plan, planErr := tgt.Read(t, "svc", "call", "input_boolean.toggle", "--data", payload); planErr != nil {
			t.Fatalf("dry run of the confirmed call failed: %v\n%s", planErr, plan)
		}

		out, err := tgt.Write(t, []string{target}, vocab,
			[]string{"svc", "call", "input_boolean.toggle", "--data", payload, "--confirm", "--json"})
		if err != nil {
			t.Fatalf("confirmed call failed: %v\n%s", err, out)
		}
		var result struct {
			Details struct {
				Changed []string `json:"changed_entities"`
			} `json:"details"`
		}
		if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
			t.Fatalf("--json on a confirmed call is not parseable: %v\n%s", jsonErr, out)
		}
		if len(result.Details.Changed) != 1 || result.Details.Changed[0] != target {
			t.Errorf("a call that toggled %s reported changed_entities=%v", target, result.Details.Changed)
		}

		// Put it back, and use the second call to assert the other half: a call
		// against an entity that does not exist reports no change.
		back, backErr := tgt.Write(t, []string{target}, vocab,
			[]string{"svc", "call", "input_boolean.toggle", "--data", payload, "--confirm", "--json"})
		if backErr != nil {
			t.Fatalf("restoring %s failed: %v\n%s", target, backErr, back)
		}
		absent := "input_boolean.pg_absent_probe"
		miss, missErr := tgt.Write(t, []string{absent}, vocab,
			[]string{"svc", "call", "input_boolean.toggle", "--data",
				`{"entity_id":"` + absent + `"}`, "--confirm", "--json"})
		if missErr != nil {
			t.Fatalf("a call against an absent entity errored; HA answers 200: %v\n%s", missErr, miss)
		}
		result.Details.Changed = nil
		if jsonErr := json.Unmarshal([]byte(miss), &result); jsonErr != nil {
			t.Fatalf("--json on the miss is not parseable: %v\n%s", jsonErr, miss)
		}
		if len(result.Details.Changed) != 0 {
			t.Errorf("a call against %s reported changed_entities=%v", absent, result.Details.Changed)
		}
	})
}

// renameSource returns a pg_ registry entity the rename cases may name as the
// SOURCE of a preview. Every case using it is a dry run, so nothing is
// renamed; the pg_ requirement is the live profile's rule, not this case's.
func renameSource(t *testing.T, tgt Target) string {
	t.Helper()
	return pgInputBoolean(t, tgt)
}

// pgInputBoolean finds a pg_-namespaced input_boolean on the target, or "".
func pgInputBoolean(t *testing.T, tgt Target) string {
	t.Helper()
	out := tgt.MustRead(t, "ent", "ls", "--domain", "input_boolean", "--pattern", "pg_", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("ent ls --json is not an array: %v\n%s", err, truncate(out))
	}
	for _, row := range rows {
		if id, ok := row["entity_id"].(string); ok && pgPrefix.MatchString(id) {
			return id
		}
	}
	return ""
}

// Finding #93: `auto apply --confirm` reordered the edited automation's nested
// keys on disk — `(platform, entity_id, to)` becoming alphabetical — and the
// preview showed those lines as unchanged, because both sides of its diff had
// been marshalled through a Go map. Finding #94: the `changed_lines` it
// reported for a one-line edit was 14.
//
// Read-only on both profiles. The write half is proven where a write can be
// proven byte for byte — internal/companiontest's
// TestE2EAutoApplyWritesOnlyItsOwnEntryCLI, against a real HA and a real
// companion. What belongs HERE is the property that made #93 invisible: the
// preview must render the automation in the same key order the family prints
// it in, and must count only what changes.
func TestSweepAutoDiffSpeaksTheOrderAutoCatPrints(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		requireCompanion(t, tgt)
		autoID := pgAutomation(t, tgt)
		if autoID == "" {
			t.Skip("no pg_ automation on this profile")
		}

		stored := tgt.MustRead(t, "auto", "cat", autoID)
		lines := strings.Split(strings.TrimRight(stored, "\n"), "\n")
		if len(lines) < 3 {
			t.Fatalf("`auto cat %s` returned %d lines; nothing to diff:\n%s", autoID, len(lines), stored)
		}

		// Edit exactly one line — the description if there is one, else the
		// alias — and write it beside the instance dir.
		edited, changedLine := editOneLine(lines)
		candidate := filepath.Join(t.TempDir(), "candidate.yaml")
		if err := os.WriteFile(candidate, []byte(edited), 0o600); err != nil {
			t.Fatal(err)
		}

		out := tgt.MustRead(t, "auto", "diff", autoID, "--file", candidate)
		// The diff's context lines are lines of the stored document. Under the
		// old map round trip they were a re-sorted rendering of it, which is
		// how a confirmed write could move a line the diff called unchanged.
		for line := range strings.SplitSeq(out, "\n") {
			if !strings.HasPrefix(line, " ") {
				continue
			}
			context := strings.TrimSpace(line)
			if context == "" || strings.HasPrefix(context, "…") {
				continue
			}
			if !strings.Contains(stored, context) {
				t.Errorf("`auto diff` shows %q as an unchanged line of %s, and `auto cat` does not contain it",
					context, autoID)
			}
		}
		if !strings.Contains(out, "-"+changedLine) {
			t.Errorf("the diff does not show the line that changed (%q):\n%s", changedLine, out)
		}

		plan := tgt.MustRead(t, "auto", "apply", autoID, "--file", candidate, "--json")
		var preview struct {
			Details struct {
				ChangedLines int `json:"changed_lines"`
			} `json:"details"`
			DryRun bool `json:"dry_run"`
		}
		if err := json.Unmarshal([]byte(plan), &preview); err != nil {
			t.Fatalf("auto apply --json does not parse: %v\n%s", err, truncate(plan))
		}
		if !preview.DryRun {
			t.Error("a run without --confirm reported dry_run: false")
		}
		if preview.Details.ChangedLines != 2 {
			t.Errorf("changed_lines = %d for a one-line edit, want 2 — it used to count context lines too",
				preview.Details.ChangedLines)
		}
	})
}

// editOneLine changes the value of the first `key: value` line that is safe to
// touch, and returns the document plus the original line.
func editOneLine(lines []string) (edited, original string) {
	for i, line := range lines {
		key, value, ok := strings.Cut(line, ": ")
		if !ok || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "-") {
			continue
		}
		switch strings.TrimSpace(key) {
		case "alias", "description":
			out := slices.Clone(lines)
			out[i] = key + ": " + strings.Trim(value, `"'`) + " wp4probe"
			return strings.Join(out, "\n") + "\n", line
		}
	}
	return strings.Join(lines, "\n") + "\n", ""
}

// pgAutomation finds a pg_-namespaced automation on the target, by the config
// id `auto ls` prints.
func pgAutomation(t *testing.T, tgt Target) string {
	t.Helper()
	out := tgt.MustRead(t, "auto", "ls", "--pattern", "pg_", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("auto ls --json is not an array: %v\n%s", err, truncate(out))
	}
	for _, row := range rows {
		if id, ok := row["id"].(string); ok && pgPrefix.MatchString(id) {
			return id
		}
	}
	return ""
}
