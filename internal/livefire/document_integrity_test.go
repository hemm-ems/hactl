//go:build livefire

package livefire

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// WP2 — document integrity. One case per finding, both profiles where both
// profiles can answer.

// requireCompanion skips a case on a profile whose instance has no companion.
//
// The rig has none: hatest boots Home Assistant alone and writes a .env with no
// COMPANION_URL, so `config file`, `config block`, `tpl cat`, `helper show` and
// `ref scan` cannot run there at all. That is rig capability debt R11, recorded
// with its reason in rigshapes_test.go — not a silent skip, which is the thing
// the debt ledger exists to prevent.
//
// The skip is decided by asking the instance rather than by branching on the
// profile name: if the rig ever gains a companion, these cases start running
// there without an edit, and the day they do is the day the R11 row must go.
func requireCompanion(t *testing.T, tgt Target) {
	t.Helper()
	if _, err := tgt.Read(t, "config", "files", "--top", "1"); err != nil {
		t.Skipf("%s has no companion, so this command cannot run here — rig capability debt R11", tgt.Profile)
	}
}

// Finding #21: `--full` means "show full/raw output" and reached exactly one
// cap, the table's `--top`. The 500-token prose cap behind it was untouched, so
// `config entries --full` on the reference instance dropped the row cap,
// produced 213 rows, and was cut at SEVEN — three fewer than the same command
// without the flag, the last one severed mid-field.
func TestSweepFullShowsMoreNotLess(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		base := tgt.MustRead(t, "config", "entries")
		full := tgt.MustRead(t, "config", "entries", "--full")

		if strings.Contains(full, "output capped at") {
			t.Errorf("`config entries --full` is truncated:\n%s", truncate(full))
		}
		if got, want := countEntryRows(full), countEntryRows(base); got < want {
			t.Errorf("`config entries --full` showed %d rows where the default showed %d — "+
				"asking for everything returned less", got, want)
		}
		// An explicit cap is still the caller's, not the flag's.
		capped := tgt.MustRead(t, "config", "entries", "--full", "--tokensmax", "20")
		if !strings.Contains(capped, "output capped at 20 tok") {
			t.Errorf("--full discarded the --tokensmax the caller typed:\n%s", truncate(capped))
		}
	})
}

// entryRowRE matches a data row of `config entries` on either instance.
//
// Both id shapes Home Assistant has minted: a 32-character hex uuid for older
// entries and a 26-character ULID for newer ones. The reference instance holds
// both — a `[0-9a-f]{32}` rule counted 111 of its 213 rows, which is exactly
// the kind of quietly-wrong measurement that makes a comparison pass for the
// wrong reason. The header cell is "entry_id", which is eight characters and
// matches neither.
var entryRowRE = regexp.MustCompile(`(?m)^[0-9A-Za-z]{26,32} `)

func countEntryRows(out string) int { return len(entryRowRE.FindAllString(out, -1)) }

// Finding #22: `config entries --json` reported `disabled_by: "-"` for every
// entry that was not disabled — 212 of 213 on the reference instance — because
// the JSON rendering re-uses the table row and the cell came from a renderer.
// Its sibling `config show --json` reported `""` for the same field of the same
// entry, so `if entry["disabled_by"]` answered "all of them" from one command
// and the truth from the other.
func TestSweepConfigEntriesJSONCarriesValuesNotRenderings(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		out := tgt.MustRead(t, "config", "entries", "--json", "--tokensmax", "0")
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("config entries --json is not a JSON array: %v\n%s", err, truncate(out))
		}
		if len(rows) == 0 {
			t.Fatal("no config entries on this instance — the case cannot say anything")
		}
		for i, row := range rows {
			if s, ok := row["disabled_by"].(string); ok && s == "-" {
				t.Errorf("row %d: disabled_by is the rendered dash, which a machine reads as a value", i)
			}
			switch v := row["options"].(type) {
			case bool: // what a machine asked for
			default:
				t.Errorf("row %d: options is %T (%v), not a bool", i, v, v)
			}
		}
		// The sibling the divergence was measured against still agrees.
		id, _ := rows[0]["entry_id"].(string)
		show := tgt.MustRead(t, "config", "show", id, "--json", "--timeout", "60s")
		var shown map[string]any
		if err := json.Unmarshal([]byte(show), &shown); err != nil {
			t.Fatalf("config show --json is not an object: %v\n%s", err, truncate(show))
		}
		entry, _ := shown["entry"].(map[string]any)
		if got, want := entry["disabled_by"], rows[0]["disabled_by"]; got != want {
			t.Errorf("the two commands still disagree about one field of one entry: "+
				"config show says %#v, config entries says %#v", got, want)
		}
	})
}

// Findings #55/#77: an explicit `--dir` or `$HACTL_DIR` that does not resolve
// printed the same generic four-step discovery block as passing nothing at all
// — the one message that knew a path had been typed was the one that would not
// repeat it back.
//
// It needs no instance, which is why it runs identically on both profiles: the
// command never gets far enough to talk to Home Assistant.
func TestSweepExplicitDirNamesThePathItTried(t *testing.T) {
	const missing = "/nonexistent/hactl/instance/xyz"
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		// Target.ReadDiagnostic supplies --dir itself, so the override is
		// passed as a second one: pflag takes the last occurrence.
		msg, err := tgt.ReadDiagnostic(t, "--dir", missing, "health")
		if err == nil {
			t.Fatal("a nonexistent instance directory succeeded")
		}
		if code := ExitCode(err); code != 2 {
			t.Errorf("exit code %d, want 2 — a configuration error has its own code", code)
		}
		if !strings.Contains(msg, missing) {
			t.Errorf("the error does not name the path the caller gave:\n%s", truncate(msg))
		}
		if !strings.Contains(msg, "--dir") {
			t.Errorf("the error does not say where the path came from:\n%s", truncate(msg))
		}
	})
}

// Finding #23: every failing companion call printed the whole request path,
// which under Ingress carries the add-on's Supervisor token — stable per
// install, and in the text of every 404 a user might paste into a bug report.
func TestSweepCompanionErrorsNameTheRouteNotTheURL(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		requireCompanion(t, tgt)

		msg, err := tgt.ReadDiagnostic(t, "config", "file", "hactl_livefire_no_such_file.yaml")
		if err == nil {
			t.Fatal("reading a nonexistent config file succeeded")
		}
		if !strings.Contains(msg, "reading config file") {
			t.Fatalf("the failure this case reads is not the one it meant to read:\n%s", truncate(msg))
		}
		if strings.Contains(msg, "hassio_ingress") {
			t.Errorf("the transport prefix reached the caller:\n%s", truncate(msg))
		}
		if !strings.Contains(msg, "/v1/config/file") {
			t.Errorf("the error no longer names the route it failed on:\n%s", truncate(msg))
		}
	})
}

// Finding #24: `config block template.yaml <unique_id>` answered `Block not
// found`, word for word what a typo gets, while the command's own --help
// promised "template.yaml blocks carry neither [id: nor alias:] — read those
// with 'tpl cat <unique_id>'".
//
// The id is discovered from the instance's own template.yaml rather than
// written here: an id that exists on one house is not a property, and the
// property is that an id the file carries is one the tool can route.
func TestSweepConfigBlockRoutesATemplateUniqueID(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		requireCompanion(t, tgt)

		raw, err := tgt.Read(t, "config", "file", "template.yaml", "--raw", "--tokensmax", "0")
		if err != nil {
			t.Skipf("this instance has no template.yaml, so there is no unique_id to route: %v", err)
		}
		id := firstUniqueID(raw)
		if id == "" {
			t.Skip("template.yaml carries no unique_id, so there is nothing to route")
		}

		msg, blockErr := tgt.ReadDiagnostic(t, "config", "block", "template.yaml", id)
		if blockErr == nil {
			t.Fatalf("`config block template.yaml %s` succeeded — template blocks carry no id "+
				"or alias, so this is a different defect", id)
		}
		if !strings.Contains(msg, "tpl cat "+id) {
			t.Errorf("`config block template.yaml %s` did not steer to the command that can "+
				"answer:\n%s", id, truncate(msg))
		}
		// And the command it names actually answers.
		if _, catErr := tgt.Read(t, "tpl", "cat", id); catErr != nil {
			t.Errorf("the referral points at a command that fails for the same id: %v", catErr)
		}
	})
}

var uniqueIDRE = regexp.MustCompile(`(?m)^\s*unique_id:\s*["']?([A-Za-z0-9_]+)["']?\s*$`)

func firstUniqueID(yamlText string) string {
	if m := uniqueIDRE.FindStringSubmatch(yamlText); m != nil {
		return m[1]
	}
	return ""
}
