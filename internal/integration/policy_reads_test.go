//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/cmd"
	"github.com/hemm-ems/hactl/internal/mcpserver"
)

// ============================================================================
// Read enforcement against a real Home Assistant (W-2, manual "Enforcement
// (reads)", D-138..D-147, D-153, D-154). The oracle reads HA over plain HTTP (pgHTTP), never
// through hactl, so a filter can neither hide what HA holds from the test nor
// invent what it does not.
// ============================================================================

// pgReads runs one hactl command in process with stdout and stderr apart, as
// the CLI and the MCP server do.
func pgReads(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("HACTL_DIR", dir)
	var out, errOut bytes.Buffer
	full := append(append([]string{"hactl"}, args...), "--tokensmax", "0")
	err = cmd.RunWithOutputContext(mcpserver.WithStderr(context.Background(), &errOut), full, &out)
	return out.String(), errOut.String(), err
}

func pgLastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// TestPolicyReadsListingDropsMasksAndCounts — `ent ls` under a profile that
// denies reading one light and the state of another: the first is not listed,
// the second's state reads <denied>, and the last stdout line says so; under
// --json the array keeps its shape and the same line goes to stderr. HA, asked
// directly, still holds both with their real state.
func TestPolicyReadsListingDropsMasksAndCounts(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	hidden, masked := rig.Lights[0], rig.Lights[1]
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:%s": [read]
      "entity:%s/state": [read]
`, pgProfile, pgProfile, hidden, masked)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	// The oracle: both lights exist, with a real state.
	for _, l := range []string{hidden, masked} {
		if st := pgStateOf(t, inst, l).State; st != "on" && st != "off" {
			t.Fatalf("oracle: %s is %q — the case would prove nothing", l, st)
		}
	}

	trailer := fmt.Sprintf("hidden by policy: 1, masked: 1 (profile %s)", pgProfile)
	out, errOut, err := pgReads(t, dir, "ent", "ls", "--domain", "light")
	if err != nil {
		t.Fatalf("ent ls: %v\n%s", err, out)
	}
	if strings.Contains(out, hidden) {
		t.Errorf("ent ls lists %s, which the profile may not read:\n%s", hidden, out)
	}
	var maskedRow string
	for l := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(l, masked+" ") {
			maskedRow = l
		}
	}
	if !strings.Contains(maskedRow, "<denied>") {
		t.Errorf("%s's state is not masked <denied>:\n%s", masked, out)
	}
	if got := pgLastLine(out); got != trailer {
		t.Errorf("last stdout line %q, want %q", got, trailer)
	}
	if errOut != "" {
		t.Errorf("prose wrote to stderr: %q", errOut)
	}

	out, errOut, err = pgReads(t, dir, "ent", "ls", "--domain", "light", "--json")
	if err != nil {
		t.Fatalf("ent ls --json: %v\n%s", err, out)
	}
	var rows []map[string]any
	if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil {
		t.Fatalf("--json does not keep its shape: %v\n%s", jerr, out)
	}
	seenMasked := false
	for _, r := range rows {
		switch r["entity_id"] {
		case hidden:
			t.Errorf("--json lists %s", hidden)
		case masked:
			seenMasked = true
			if r["state"] != "<denied>" {
				t.Errorf("--json state of %s = %v, want <denied>", masked, r["state"])
			}
		}
	}
	if !seenMasked {
		t.Errorf("--json dropped %s, whose entity is readable — a masked part is shown, not hidden", masked)
	}
	if strings.TrimRight(errOut, "\n") != trailer {
		t.Errorf("--json stderr %q, want the trailer %q", errOut, trailer)
	}
	if st := pgStateOf(t, inst, masked).State; st == "<denied>" {
		t.Errorf("the mask reached Home Assistant: %s is %q", masked, st)
	}
}

// TestPolicyReadsOneDeniedThingIsRefused — `ent show` of an entity the
// profile may not read is refused like a denied write (D-155): exit 1, the
// D-119 count as the error line, the refusal report (or under --json its
// envelope) on stdout naming the rule and its line, nothing of the entity's
// state.
func TestPolicyReadsOneDeniedThingIsRefused(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	denied := rig.Lights[2]
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:%s": [read]
`, pgProfile, pgProfile, denied)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	if st := pgStateOf(t, inst, denied).State; st == "" {
		t.Fatalf("oracle: %s has no state", denied)
	}
	for _, args := range [][]string{{"ent", "show", denied}, {"ent", "show", denied, "--json"}} {
		out, errOut, err := pgReads(t, dir, args...)
		if pgExitCode(err) != 1 {
			t.Fatalf("%v: exit %d (%v), want 1\n%s", args, pgExitCode(err), err, out)
		}
		summary := fmt.Sprintf("policy_denied: 1 of 1 resources denied (profile %s)", pgProfile)
		if !strings.HasPrefix(err.Error(), summary) {
			t.Errorf("%v: error line %q, want it to start %q", args, err.Error(), summary)
		}
		rule := fmt.Sprintf(`deny \"entity:%s\" [read]`, denied)
		if len(args) == 3 {
			rule = fmt.Sprintf(`  denied: read entity:%s — deny "entity:%s" [read] (line %d)`, denied, denied,
				policyLineOf(t, policy, `"entity:`+denied+`"`))
		}
		if !strings.Contains(out, rule) || !strings.Contains(out, "policy_denied") {
			t.Errorf("%v: the refusal report does not name %q:\n%s", args, rule, out)
		}
		if strings.Contains(out, `"state"`) || strings.Contains(out, "last_changed") || strings.Contains(out+errOut, "hidden by policy") {
			t.Errorf("%v: a refused read printed something of the entity, or a trailer:\nstdout %q\nstderr %q", args, out, errOut)
		}
	}
}

// TestPolicyReadsDeniedAttributeIsMasked — an entity allowed with a denied
// attribute is shown, the attribute's key kept and its value <denied>, in
// prose and --json; HA still holds the real value.
func TestPolicyReadsDeniedAttributeIsMasked(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	light := rig.Lights[0]
	actual, _ := pgStateOf(t, inst, light).Attributes["friendly_name"].(string)
	if actual == "" {
		t.Fatalf("oracle: %s has no friendly_name — the case would prove nothing", light)
	}
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:%s/attr/friendly_name": [read]
`, pgProfile, pgProfile, light)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	out, _, err := pgReads(t, dir, "ent", "show", light, "--full")
	if err != nil {
		t.Fatalf("ent show: %v\n%s", err, out)
	}
	if strings.Contains(out, actual) || !strings.Contains(out, "<denied>") {
		t.Errorf("prose shows the denied attribute's value %q, or no <denied>:\n%s", actual, out)
	}
	if got := pgLastLine(out); got != fmt.Sprintf("hidden by policy: 0, masked: 1 (profile %s)", pgProfile) {
		t.Errorf("trailer %q", got)
	}

	out, errOut, err := pgReads(t, dir, "ent", "show", light, "--json")
	if err != nil {
		t.Fatalf("ent show --json: %v\n%s", err, out)
	}
	if strings.Contains(out, actual) {
		t.Errorf("--json carries the denied value %q:\n%s", actual, out)
	}
	var doc map[string]any
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("--json does not parse: %v\n%s", jerr, out)
	}
	attrs, _ := doc["attributes"].(map[string]any)
	if v, has := attrs["friendly_name"]; !has || v != "<denied>" {
		t.Errorf("--json attributes.friendly_name = %v (present %v), want the key kept reading <denied>:\n%s", v, has, out)
	}
	if !strings.HasPrefix(errOut, "hidden by policy: 0, masked: 1") {
		t.Errorf("--json stderr %q, want the trailer", errOut)
	}
	if got := pgStateOf(t, inst, light).Attributes["friendly_name"]; got != actual {
		t.Errorf("HA's friendly_name changed to %v", got)
	}
}

// TestPolicyReadsTplEvalNeedsSystemTemplate — D-143: under a profile that
// denies some read, `tpl eval` is refused without an allow of
// system:template [read], and runs with it.
func TestPolicyReadsTplEvalNeedsSystemTemplate(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	base := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:%s": [read]
`, pgProfile, pgProfile, rig.Lights[0])
	tpl := "{{ states('" + rig.Lights[0] + "') }}"
	refused, _, err := pgReads(t, pgInstanceDir(t, inst.URL(), inst.Token(), base), "tpl", "eval", tpl)
	if pgExitCode(err) != 1 || !strings.Contains(refused, "  denied: read system:template — ") {
		t.Fatalf("tpl eval under a read deny: %v, want refused on read system:template:\n%s", err, refused)
	}
	allowed := base + "    allow:\n      \"system:template\": [read]\n"
	out, _, err := pgReads(t, pgInstanceDir(t, inst.URL(), inst.Token(), allowed), "tpl", "eval", tpl)
	if err != nil {
		t.Fatalf("tpl eval with system:template allowed: %v\n%s", err, out)
	}
	if want := pgStateOf(t, inst, rig.Lights[0]).State; !strings.Contains(out, want) {
		t.Errorf("tpl eval answered %q, HA holds %q", out, want)
	}
}

// TestPolicyReadsNoPolicyIsByteIdentical — D-58 for reads: with no policy
// file, with the implicit allow-all, and under a profile that denies no read,
// every read prints the same bytes, and no trailer.
func TestPolicyReadsNoPolicyIsByteIdentical(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	dirs := map[string]string{
		"no file":            pgInstanceDir(t, inst.URL(), inst.Token(), ""),
		"implicit allow-all": pgInstanceDir(t, inst.URL(), inst.Token(), "version: 1\nprofiles:\n  other:\n    default: deny\n"),
		"no read denied": pgInstanceDir(t, inst.URL(), inst.Token(), fmt.Sprintf(
			"version: 1\nactive: %s\nprofiles:\n  %s:\n    deny:\n      \"entity:lock.*\": [control]\n", pgProfile, pgProfile)),
	}
	for _, args := range [][]string{
		{"ent", "show", rig.Lights[0], "--json"},
		{"ent", "ls", "--domain", "light", "--json"},
		{"area", "ls"},
		{"label", "ls"},
		{"tpl", "eval", "{{ 6 * 7 }}"},
	} {
		var first, firstName string
		for _, name := range []string{"no file", "implicit allow-all", "no read denied"} {
			out, errOut, err := pgReads(t, dirs[name], args...)
			if err != nil {
				t.Fatalf("%v (%s): %v\n%s", args, name, err, out)
			}
			if errOut != "" || strings.Contains(out, "hidden by policy") {
				t.Errorf("%v (%s): a trailer where nothing is hidden:\nstdout %q\nstderr %q", args, name, out, errOut)
			}
			if firstName == "" {
				first, firstName = out, name
				continue
			}
			if out != first {
				t.Errorf("%v: %s and %s answer differently:\n--- %s\n%s\n--- %s\n%s", args, firstName, name, firstName, first, name, out)
			}
		}
	}
}

// TestPolicyReadsWideReadIsFiltered — `log` is read system:logs as a whole
// (D-142), and a dashboard view a deny covers is dropped from `dash show`
// (D-140) while HA keeps it.
func TestPolicyReadsWideReadIsFiltered(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	ws := dialRawWS(t, inst.URL(), inst.Token())
	urlPath := "pg-reads-dash"
	pgCreateDashboard(t, inst, urlPath, map[string]any{"views": []any{
		map[string]any{"path": "open", "title": "Open view", "cards": []any{}},
		map[string]any{"path": "secret", "title": "Secret view", "cards": []any{}},
	}})
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "system:logs": [read]
      "dashboard:%s/views/secret": [read]
`, pgProfile, pgProfile, urlPath)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	logOut, _, err := pgReads(t, dir, "log")
	if pgExitCode(err) != 1 || !strings.Contains(logOut, `  denied: read system:logs — deny "system:logs" [read]`) {
		t.Errorf("log under a system:logs deny: %v, want refused:\n%s", err, logOut)
	}

	out, _, err := pgReads(t, dir, "dash", "show", urlPath, "--raw")
	if err != nil {
		t.Fatalf("dash show: %v\n%s", err, out)
	}
	if strings.Contains(out, "Secret view") || !strings.Contains(out, "Open view") {
		t.Errorf("dash show --raw: the denied view is shown, or the open one is not:\n%s", out)
	}
	env := ws.mustSucceed(t, map[string]any{"type": "lovelace/config", "url_path": urlPath})
	if !strings.Contains(string(env.Result), "Secret view") {
		t.Fatalf("oracle: HA no longer holds the secret view: %s", env.Result)
	}

	// Saving what was shown would delete the view the profile cannot read:
	// refused (D-153), and HA keeps it.
	file := filepath.Join(t.TempDir(), "dash.json")
	if werr := os.WriteFile(file, []byte(out), 0o600); werr != nil {
		t.Fatal(werr)
	}
	for _, args := range [][]string{{"dash", "save", urlPath, "-f", file}, {"dash", "save", urlPath, "-f", file, "--confirm"}} {
		sout, _, serr := pgReads(t, dir, args...)
		if pgExitCode(serr) != 1 || !strings.Contains(sout, "delete dashboard:"+urlPath+"/views/secret") {
			t.Errorf("%v: %v, want refused naming the view's delete\n%s", args, serr, sout)
		}
	}
	env = ws.mustSucceed(t, map[string]any{"type": "lovelace/config", "url_path": urlPath})
	if !strings.Contains(string(env.Result), "Secret view") {
		t.Errorf("the refused save deleted the secret view: %s", env.Result)
	}
}

// TestPolicyReadsWriteCommandsOwnReadsAreExempt — D-82: a profile that may
// edit an entity's labels and read nothing at all still labels it — `ent
// set-label` reads the entity and the label list before it writes, and those
// reads are the write's own, decided by its plan — while a read command under
// the same profile is refused (`ent show`) or filtered (`ent ls`, `label ls`).
// HA's registry, read directly, is the oracle.
func TestPolicyReadsWriteCommandsOwnReadsAreExempt(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	light := rig.Lights[2]
	ws := dialRawWS(t, inst.URL(), inst.Token())
	before := slices.Clone(pgEntityRegistry(t, ws)[light].Labels)
	if before == nil {
		before = []string{}
	}
	label := pgCreateLabel(t, ws, "pg_policy_d82_label")
	t.Cleanup(func() {
		cleanup := dialRawWS(t, inst.URL(), inst.Token())
		cleanup.send(t, map[string]any{"type": "config/entity_registry/update", "entity_id": light, "labels": before})
		cleanup.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": label})
	})
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    default: deny
    allow:
      "entity:%s/registry/labels": [edit]
`, pgProfile, pgProfile, light)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	// The write: preview and --confirm, no read allowed anywhere.
	assertPolicyAllowedPreview(t, dir, "ent", "set-label", light, label)
	if out, err := runHactlDirErr(t, dir, "ent", "set-label", light, label, "--confirm"); err != nil {
		t.Fatalf("set-label --confirm with edit but no read was refused: %v\n%s", err, out)
	} else if strings.Contains(out, "hidden by policy") {
		t.Errorf("a write command's own reads were counted as hidden:\n%s", out)
	}
	if !slices.Contains(pgEntityRegistry(t, ws)[light].Labels, label) {
		t.Fatalf("oracle: set-label --confirm did not label %s in HA's registry", light)
	}

	// The reads, under the same profile.
	out, _, err := pgReads(t, dir, "ent", "show", light)
	if pgExitCode(err) != 1 || !strings.Contains(out, "  denied: read entity:"+light+" — default: deny") {
		t.Errorf("ent show under a profile that reads nothing: %v, want refused:\n%s", err, out)
	}
	out, _, err = pgReads(t, dir, "ent", "ls", "--domain", "light")
	if err != nil {
		t.Fatalf("ent ls: %v\n%s", err, out)
	}
	if strings.Contains(out, light) || !strings.HasPrefix(pgLastLine(out), "hidden by policy: ") {
		t.Errorf("ent ls under a profile that reads nothing shows %s, or no trailer:\n%s", light, out)
	}
	out, _, err = pgReads(t, dir, "label", "ls")
	if err != nil {
		t.Fatalf("label ls: %v\n%s", err, out)
	}
	if strings.Contains(out, "pg_policy_d82_label") || strings.Contains(out, label+" ") {
		t.Errorf("label ls under a profile that reads nothing shows the label:\n%s", out)
	}
}
