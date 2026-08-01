//go:build livefire

package livefire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WP5, the dash family. Findings #3 #8 #57 #59 #60, plus the default-dashboard
// mode premise the reproduction of #3 turned up.
//
// Three of the five are one question asked of a dashboard hactl had only ever
// asked of the default: what does Home Assistant hold for this url_path? The
// answers "nothing at all" (#3) and "a document with no views" (#8) are both
// real, both stock, and both used to come out as either a raw wire error or a
// silent exit 0.

// unsavedDashboard is the registered dashboard with no stored config on each
// profile: the rig's is a fixture (R12), the live one is pg-w5-fresh, created
// during WP5's reproduction and left in place deliberately — saving a config
// over one of Jan's dashboards is the only other way to ask the question.
func unsavedDashboard(tgt Target) string {
	if tgt.Profile == Live {
		return "pg-w5-fresh"
	}
	return "rig-unsaved"
}

// strategyDashboard is the dashboard whose stored config generates its views
// instead of listing them. Home Assistant creates it during onboarding, so it
// is the same object on both profiles.
const strategyDashboard = "map"

// Finding #3: `dash show <url_path>` on a dashboard Home Assistant holds no
// config for returned `fetching dashboard config: lovelace/config failed: No
// config found.` at exit 1, in plain AND --json form — while the identical
// condition on the DEFAULT dashboard produced a documented report and exit 0.
//
// The code's own comment asserted the premise: "the default dashboard has a
// state a named dashboard cannot have: auto-generated, where HA holds no config
// at all". Every dashboard is in that state between `dash create` and its first
// `dash save`, which is the family's own documented next step.
func TestSweepDashShowStatesThatNoConfigIsStored(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		target := unsavedDashboard(tgt)

		plain, err := tgt.Read(t, "dash", "show", target)
		if err != nil {
			t.Fatalf("`dash show %s` exited %d; a dashboard with no stored config is a state to "+
				"report, not a failure:\n%s", target, ExitCode(err), plain)
		}
		if strings.Contains(plain, "lovelace/config") {
			t.Errorf("`dash show %s` leaks the wire error to the reader:\n%s", target, plain)
		}

		out, jsonErr := tgt.Read(t, "dash", "show", target, "--json")
		if jsonErr != nil {
			t.Fatalf("`dash show %s --json` exited %d:\n%s", target, ExitCode(jsonErr), out)
		}
		var report struct {
			URLPath string `json:"url_path"`
			State   string `json:"state"`
			Detail  string `json:"detail"`
		}
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatalf("`dash show %s --json` does not parse: %v\n%s", target, err, truncate(out))
		}
		// The discriminator, not the wording: a stored answer is the config
		// document itself and never carries a top-level `state`, so a caller
		// tells the two apart by looking at the object (H-10).
		if report.State == "" {
			t.Errorf("`dash show %s --json` carries no `state` discriminator:\n%s", target, out)
		}
		if report.URLPath != target {
			t.Errorf("`dash show %s --json` reports url_path %q", target, report.URLPath)
		}
		if report.Detail == "" {
			t.Errorf("`dash show %s --json` says nothing about what the state means:\n%s", target, out)
		}

		// --raw and --yaml write a DOCUMENT, and there is none. They must refuse
		// — as they already did for the auto-generated default — rather than
		// emit the wire error under a flag whose help says "for LLM round-trip
		// editing".
		for _, flag := range []string{"--raw", "--yaml"} {
			doc, docErr := tgt.Read(t, "dash", "show", target, flag)
			if docErr == nil {
				t.Errorf("`dash show %s %s` exited 0 with no document to write:\n%s", target, flag, doc)
			}
			if doc != "" {
				t.Errorf("`dash show %s %s` wrote to stdout with nothing stored: %q", target, flag, doc)
			}
		}
	})
}

// Finding #3, the half the report did not reach: a dashboard with no stored
// config counted as UNSCANNED in every dashboard walk, so `dash grep` called a
// complete answer partial and `ref validate` — a gate, by D-7 — refused to
// certify anything at all on an instance holding one unsaved dashboard.
//
// Nothing is stored, so nothing can be missed: that is a complete answer about
// that dashboard, exactly as it already was for the auto-generated default.
func TestSweepADashboardWithNoConfigIsScannedNotSkipped(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		target := unsavedDashboard(tgt)

		// Both walks report their scope on stderr, so the assertion reads there
		// (see Target.ReadDiagnostic): asserting on stdout would run against a
		// stream the message never reaches and pass.
		grep, _ := tgt.ReadDiagnostic(t, "dash", "grep", "sensor.wp5_absent_probe")
		if strings.Contains(grep, target) {
			t.Errorf("`dash grep` names %s as a dashboard it could not scan:\n%s", target, grep)
		}
		if strings.Contains(grep, "this answer is partial") {
			t.Errorf("`dash grep` calls its answer partial:\n%s", grep)
		}

		// `ref validate` legitimately exits 1 on an instance with dangling
		// references — the reference instance has hundreds — so the assertion is
		// on the SCOPE refusal, never on the exit code.
		validate, _ := tgt.ReadDiagnostic(t, "ref", "validate", "--exit-code")
		if strings.Contains(validate, "cannot certify anything") {
			t.Errorf("`ref validate` certifies nothing because a dashboard holds no config:\n%s",
				truncate(validate))
		}
	})
}

// Finding #8: `dash show <path> --view <name>` on a dashboard with no views
// printed "no views" and exited 0, while the same nonexistent view name on a
// dashboard that has views exits 1. The `len(cfg.Views) == 0` early return
// fires before flagDashView is ever read, so a script gating on `--view`
// existence by exit code gets a false success on exactly the dashboards whose
// views it cannot enumerate.
//
// The contrast case is what makes it a case: without it, "always exit 1" is
// satisfied by a command that refuses every --view.
func TestSweepDashShowViewNotFoundIsAlwaysAnError(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		// Both output branches: --raw already exits 1 here and the plain branch
		// did not, which is the divergence — one command, one question, two
		// answers depending on which flag the caller happened to pass.
		for _, args := range [][]string{
			{"dash", "show", strategyDashboard, "--view", "wp5-absent-view"},
			{"dash", "show", strategyDashboard, "--view", "wp5-absent-view", "--raw"},
		} {
			out, err := tgt.Read(t, args...)
			if err == nil {
				t.Errorf("%v exited 0 for a view that does not exist:\n%s", args, out)
			}
			if strings.Contains(out, "no views") {
				t.Errorf("%v answered the question it was not asked:\n%s", args, out)
			}
		}

		// The control: a dashboard that HAS views still answers a --view that
		// names one.
		withViews := dashboardWithViews(t, tgt)
		if withViews.urlPath == "" {
			t.Skip("no dashboard with a named view on this profile")
		}
		out := tgt.MustRead(t, "dash", "show", withViews.urlPath, "--view", withViews.view)
		if !json.Valid([]byte(out)) {
			t.Errorf("`dash show %s --view %s` did not emit the view:\n%s",
				withViews.urlPath, withViews.view, truncate(out))
		}
	})
}

// Finding #8's other half. A strategy dashboard's stored config really does
// carry zero views, so "no views" is true — and it reads as "this dashboard is
// empty" when the truth is "Home Assistant generates its views in the frontend
// at view time". The manual's rule for the auto-generated default is the same
// rule: say what the state is, never fabricate what is not stored.
func TestSweepDashShowNamesTheStrategyThatBuildsTheViews(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		out := tgt.MustRead(t, "dash", "show", strategyDashboard)
		if !strings.Contains(out, "strategy") {
			t.Errorf("`dash show %s` reports zero views without saying what generates them:\n%s",
				strategyDashboard, out)
		}
	})
}

// Finding #57: `dash delete --confirm` on a dashboard whose url_path is long
// enough exits 1 with `deleting dashboard: lovelace/dashboards/delete failed:
// Unknown error` — and the dashboard is gone.
//
// Home Assistant removes the item from its collection FIRST and then, from a
// post-removal listener, unlinks `.storage/lovelace.<id>`; on a 300-character
// url_path that path exceeds the filesystem's 255-byte filename limit and the
// unlink raises OSError, which the websocket layer reports as "Unknown error"
// (traceback captured live 2026-07-31). A caller trusting the exit code
// believes the dashboard still exists.
//
// A write case, and it is safe on the live profile because the object it
// creates and destroys is its own.
func TestSweepDashDeleteReportsWhetherTheObjectIsGone(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		// 300+ characters so `.storage/lovelace.<id>` is past the limit. The
		// prefix is pg_ so the live profile's guard accepts it.
		target := "pg-w5-toolong-" + strings.Repeat("y", 290)
		vocab := []string{"dash", "create", "delete", "--url-path", "--title", "--confirm", "--json"}
		title := "PG W5 Too Long"

		if plan, err := tgt.Read(t, "dash", "create", "--url-path", target, "--title", title); err != nil {
			t.Fatalf("dry run of the create failed: %v\n%s", err, plan)
		}
		created, err := tgt.Write(t, []string{target}, vocab,
			[]string{"dash", "create", "--url-path", target, "--title", title, "--confirm"})
		if err != nil {
			t.Fatalf("creating the long-url_path dashboard failed: %v\n%s", err, created)
		}

		if plan, planErr := tgt.Read(t, "dash", "delete", target); planErr != nil {
			t.Fatalf("dry run of the delete failed: %v\n%s", planErr, plan)
		}
		out, delErr := tgt.Write(t, []string{target}, vocab,
			[]string{"dash", "delete", target, "--confirm", "--json"})

		gone := !dashboardIsListed(t, tgt, target)
		if !gone {
			// Not the shape this case exists for — the filename limit did not
			// bite here — but then the exit code must say the delete failed.
			if delErr == nil {
				t.Fatalf("`dash delete %s --confirm` reported success and the dashboard is still "+
					"listed:\n%s", truncate(target), out)
			}
			t.Skipf("this filesystem accepted the delete's storage path; the shape needs a "+
				"filename limit shorter than %d bytes", len(target))
		}
		if delErr != nil {
			t.Fatalf("`dash delete --confirm` exited %d and the dashboard IS gone; a caller "+
				"trusting the code believes it still exists:\n%s", ExitCode(delErr), out)
		}
		var result struct {
			OK       bool     `json:"ok"`
			Warnings []string `json:"warnings"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("`dash delete --confirm --json` does not parse: %v\n%s", err, truncate(out))
		}
		if !result.OK {
			t.Errorf("the delete removed the dashboard and reported ok=false:\n%s", truncate(out))
		}
		// Home Assistant did report an error, and hiding that would be the
		// opposite mistake: the outcome is "gone, and HA complained".
		if len(result.Warnings) == 0 {
			t.Errorf("the delete swallowed Home Assistant's own error report:\n%s", truncate(out))
		}
	})
}

// Finding #59: `dash ls --json` reported `"admin": "true"` and `"sidebar":
// "true"` — the strings, not the JSON literals. A consumer writing the obvious
// `if row["admin"]` reads every dashboard as admin-only, because "false" is a
// non-empty string.
//
// The case is deliberately not about `dash ls`. `format.Table.SetMachine`
// exists for exactly this and two commands already use it, so the defect is a
// law with unclassified sites — the sweep asks every command that renders a
// boolean into a table.
func TestSweepBooleanColumnsReachJSONAsBooleans(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, tc := range []struct {
			args    []string
			columns []string
		}{
			{[]string{"dash", "ls", "--json"}, []string{"admin", "sidebar"}},
			{[]string{"auto", "ls", "--restored", "--json"}, []string{"restored"}},
			{[]string{"ent", "ls", "--restored", "--json"}, []string{"restored"}},
			{[]string{"config", "entries", "--json"}, []string{"options"}},
			{[]string{"issues", "--json"}, []string{"fixable", "ignored"}},
		} {
			out, err := tgt.Read(t, tc.args...)
			if err != nil {
				t.Errorf("%v failed: %v\n%s", tc.args, err, truncate(out))
				continue
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				// An empty listing is an object, not an array, and proves
				// nothing either way.
				continue
			}
			for _, row := range rows {
				for _, column := range tc.columns {
					value, present := row[column]
					if !present {
						continue
					}
					if _, ok := value.(bool); !ok {
						t.Errorf("%v reports %s as %T (%v); a boolean column is a JSON boolean",
							tc.args, column, value, value)
					}
				}
			}
		}
	})
}

// Finding #60: `dash show <path> --raw --yaml` emitted raw JSON and said
// nothing about --yaml. Three flags name the output format (--raw, --yaml, the
// global --json) and the command silently picked a winner by the order of its
// own if-statements.
//
// Noting the winner on stdout is not available as a fix: under --json that
// would break H-10's "nothing else on stdout". So the combination is refused.
func TestSweepDashShowRefusesTwoOutputFormats(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		target := strategyDashboard
		for _, flags := range [][]string{
			{"--raw", "--yaml"},
			{"--raw", "--json"},
			{"--yaml", "--json"},
		} {
			args := append([]string{"dash", "show", target}, flags...)
			out, err := tgt.Read(t, args...)
			if err == nil {
				t.Errorf("%v exited 0; one of the two flags was silently discarded:\n%s",
					args, truncate(out))
			}
			if out != "" {
				t.Errorf("%v wrote a document for a format request it could not honour:\n%s",
					args, truncate(out))
			}
		}
		// The control: each format alone still works.
		for _, flag := range []string{"--raw", "--yaml", "--json"} {
			if out, err := tgt.Read(t, "dash", "show", target, flag); err != nil {
				t.Errorf("`dash show %s %s` was refused on its own: %v\n%s", target, flag, err, out)
			}
		}
	})
}

// The default-dashboard mode premise, found while reproducing #3 and confirmed
// against Home Assistant's source: since HA 2026.x, `_async_migrate_default_config`
// moves a stored default into the dashboards collection at boot, so the default
// IS listed — under url_path "lovelace", with mode "storage".
//
// hactl read "the default is listed" as "the default is YAML-mode", because the
// only state that had ever produced a listed default was the YAML one. On the
// reference instance `dash save` and `dash replace` therefore refused every
// write to the default, citing a `lovelace: mode: yaml` line its
// configuration.yaml does not contain.
//
// Read-only: a dry run resolves and gates, and writes nothing.
func TestSweepDefaultDashboardWriteFollowsItsOwnMode(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		var rows []struct {
			URLPath string `json:"url_path"`
			Mode    string `json:"mode"`
		}
		listing := tgt.MustRead(t, "dash", "ls", "--json")
		if err := json.Unmarshal([]byte(listing), &rows); err != nil {
			t.Fatalf("dash ls --json: %v\n%s", err, truncate(listing))
		}
		mode := ""
		for _, r := range rows {
			if r.URLPath == "lovelace" {
				mode = r.Mode
			}
		}
		if mode != "storage" {
			t.Skipf("the default dashboard is not listed as storage-mode here (mode=%q)", mode)
		}

		config := filepath.Join(t.TempDir(), "dashboard.json")
		if err := os.WriteFile(config, []byte(`{"views":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := tgt.Read(t, "dash", "save", "--file", config)
		if err != nil {
			t.Fatalf("a dry-run save against a storage-mode default was refused: %v\n%s", err, out)
		}
		if strings.Contains(out, "YAML-mode") {
			t.Errorf("the plan calls a storage-mode default YAML-mode:\n%s", out)
		}
		replace, replaceErr := tgt.Read(t, "dash", "replace",
			"sensor.wp5_absent_probe", "sensor.wp5_absent_probe_2")
		if replaceErr != nil {
			t.Fatalf("a dry-run replace against a storage-mode default was refused: %v\n%s",
				replaceErr, replace)
		}
	})
}

// dashboardView names a dashboard on the target and one view path within it.
type dashboardView struct{ urlPath, view string }

// dashboardWithViews finds a dashboard that has at least one named view, for
// the control half of the --view case.
func dashboardWithViews(t *testing.T, tgt Target) dashboardView {
	t.Helper()
	var rows []struct {
		URLPath string `json:"url_path"`
	}
	listing := tgt.MustRead(t, "dash", "ls", "--json")
	if err := json.Unmarshal([]byte(listing), &rows); err != nil {
		t.Fatalf("dash ls --json: %v\n%s", err, truncate(listing))
	}
	for _, row := range rows {
		raw, err := tgt.Read(t, "dash", "show", row.URLPath, "--raw")
		if err != nil {
			continue
		}
		var config struct {
			Views []struct {
				Path string `json:"path"`
			} `json:"views"`
		}
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			continue
		}
		for _, view := range config.Views {
			if view.Path != "" {
				return dashboardView{urlPath: row.URLPath, view: view.Path}
			}
		}
	}
	return dashboardView{}
}

// dashboardIsListed asks Home Assistant, through `dash ls`, whether a url_path
// is still registered.
func dashboardIsListed(t *testing.T, tgt Target, urlPath string) bool {
	t.Helper()
	var rows []struct {
		URLPath string `json:"url_path"`
	}
	listing := tgt.MustRead(t, "dash", "ls", "--json")
	if err := json.Unmarshal([]byte(listing), &rows); err != nil {
		t.Fatalf("dash ls --json: %v\n%s", err, truncate(listing))
	}
	for _, row := range rows {
		if row.URLPath == urlPath {
			return true
		}
	}
	return false
}
