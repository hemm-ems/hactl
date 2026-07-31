package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ============================================================================
// `dash show` on a NAMED dashboard Home Assistant holds no config for (D-23,
// live-fire #3), and the resolve that keeps it distinguishable from a typo.
//
// The premise these tests exist to refute was written into the code: "the
// default dashboard has a state a named dashboard cannot have: auto-generated,
// where HA holds no config at all". Every dashboard is in that state between
// `dash create` and its first `dash save`.
//
// The wire cannot tell the two apart on its own. HA answers `config_not_found`
// both when a dashboard holds no config ("No config found.") and when there is
// no such dashboard ("Unknown config specified: x") — same code, different
// English (lovelace/websocket.py, HA 2026.7.4; measured on the reference
// instance 2026-07-31). So the report is only reachable after a resolve.
// ============================================================================

// namedDashboardListEntry is a registered storage-mode dashboard.
func namedDashboardListEntry(urlPath string) map[string]any {
	return map[string]any{
		"id": strings.ReplaceAll(urlPath, "-", "_"), "url_path": urlPath,
		"title": "Named", "mode": "storage", "require_admin": false, "show_in_sidebar": true,
	}
}

func TestRunDashShow_NamedDashboardWithNoStoredConfigReportsTheState(t *testing.T) {
	newServer := func(t *testing.T) {
		t.Helper()
		ts := startCmdServer(t, map[string]any{
			"lovelace/dashboards/list": []any{namedDashboardListEntry("fresh-dash")},
			"lovelace/config":          configNotFound,
		}, nil)
		withFlagDir(t, ts.dir)
	}

	t.Run("plain", func(t *testing.T) {
		newServer(t)
		var buf bytes.Buffer
		if err := runDashShow(context.Background(), &buf, "fresh-dash"); err != nil {
			t.Fatalf("a registered dashboard with no stored config is a state to report, not a failure: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "no stored config") {
			t.Errorf("output must name the state:\n%s", out)
		}
		if strings.Contains(out, "lovelace/config") {
			t.Errorf("output leaks the wire error to the reader:\n%s", out)
		}
		if !strings.Contains(out, "dash save fresh-dash") {
			t.Errorf("output must name the command that ends the state:\n%s", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		newServer(t)
		setFlagForTest(t, &flagJSON, true)
		var buf bytes.Buffer
		if err := runDashShow(context.Background(), &buf, "fresh-dash"); err != nil {
			t.Fatalf("dash show --json failed: %v", err)
		}
		var report struct {
			URLPath string `json:"url_path"`
			State   string `json:"state"`
			Detail  string `json:"detail"`
		}
		if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
			t.Fatalf("--json does not parse: %v\n%s", err, buf.String())
		}
		// The discriminator is per state, not per command: the default's
		// documented "auto-generated" is a different fact from a named
		// dashboard's, and a caller that branches on `state` must be able to
		// tell them apart.
		if report.State != "no-stored-config" {
			t.Errorf("state = %q, want %q", report.State, "no-stored-config")
		}
		if report.URLPath != "fresh-dash" {
			t.Errorf("url_path = %q", report.URLPath)
		}
		if report.Detail == "" {
			t.Error("the report says nothing about what the state means")
		}
	})

	// A document was asked for and there is none. Emitting `{}` would be a
	// config asserting the dashboard is empty, which is a different claim.
	for _, flag := range []struct {
		name string
		set  *bool
	}{{"--raw", &flagDashRaw}, {"--yaml", &flagDashYAML}} {
		t.Run(flag.name, func(t *testing.T) {
			newServer(t)
			setFlagForTest(t, flag.set, true)
			var buf bytes.Buffer
			err := runDashShow(context.Background(), &buf, "fresh-dash")
			if err == nil {
				t.Fatalf("%s emitted a document for a dashboard with none:\n%s", flag.name, buf.String())
			}
			if buf.Len() > 0 {
				t.Errorf("%s wrote to stdout before refusing: %q", flag.name, buf.String())
			}
			if !strings.Contains(err.Error(), "no stored config") {
				t.Errorf("%s: refusal does not name the state: %v", flag.name, err)
			}
		})
	}
}

// TestRunDashShow_UnknownDashboardIsStillAnError is the control for the case
// above. Both conditions arrive as `config_not_found`, so a classification that
// skipped the resolve would answer a typo with a serene "nothing is stored
// here" at exit 0.
func TestRunDashShow_UnknownDashboardIsStillAnError(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{namedDashboardListEntry("fresh-dash")},
		"lovelace/config":          configNotFound,
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	err := runDashShow(context.Background(), &buf, "typo-dash")
	if err == nil {
		t.Fatalf("a dashboard that does not exist was reported as a state:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error does not say the dashboard is unknown: %v", err)
	}
}

// TestRunDashReplace_UnknownDashboardIsRefused closes the `dash replace
// [target]` debt row in dev/surfaces/confirm.manifest.
//
// `dash replace` leaned on the config fetch failing to catch an unresolvable
// url_path. That stopped being a failure the moment "no stored config" became
// an answer, so without the resolve the command would report `"x" not found in
// dashboard typo-dash` at exit 0 — a preview for a target that does not exist,
// which is exactly what H-2 forbids.
func TestRunDashReplace_UnknownDashboardIsRefused(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{namedDashboardListEntry("real-dash")},
		"lovelace/config":          configNotFound,
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	err := runDashReplace(context.Background(), &buf, "light.old", "light.new", "typo-dash")
	if err == nil {
		t.Fatalf("a replace named a dashboard that does not exist and was previewed:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error does not say the dashboard is unknown: %v", err)
	}
	if strings.Contains(buf.String(), "dry-run") {
		t.Errorf("a plan was printed for an unresolvable dashboard:\n%s", buf.String())
	}
}

// TestRunDashShow_StrategyDashboardNamesItsStrategy — zero views is true for a
// strategy dashboard and "no views" alone reads as "this dashboard is empty".
// Home Assistant creates one during onboarding (`map`), so this is stock shape.
func TestRunDashShow_StrategyDashboardNamesItsStrategy(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{namedDashboardListEntry("map-dash")},
		"lovelace/config":          json.RawMessage(`{"strategy":{"type":"map"}}`),
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDashShow(context.Background(), &buf, "map-dash"); err != nil {
		t.Fatalf("dash show on a strategy dashboard failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "strategy") || !strings.Contains(out, "map") {
		t.Errorf("output does not name the strategy that builds the views:\n%s", out)
	}
}

// TestRunDashShow_ViewNotFoundOnAZeroViewDashboard — the `--view` request is
// answered whatever the dashboard's shape, so a script gating on view existence
// by exit code cannot get a false success from a dashboard with no views
// (live-fire #8). The control is one line down: a dashboard WITH views answers
// the same missing name the same way.
func TestRunDashShow_ViewNotFoundOnAZeroViewDashboard(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"strategy dashboard", `{"strategy":{"type":"map"}}`},
		{"dashboard with views", `{"views":[{"path":"real","cards":[]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := startCmdServer(t, map[string]any{
				"lovelace/dashboards/list": []any{namedDashboardListEntry("some-dash")},
				"lovelace/config":          json.RawMessage(tc.config),
			}, nil)
			withFlagDir(t, ts.dir)
			setFlagForTest(t, &flagDashView, "absent-view")

			var buf bytes.Buffer
			err := runDashShow(context.Background(), &buf, "some-dash")
			if err == nil {
				t.Fatalf("a missing view was reported as success:\n%s", buf.String())
			}
			if !strings.Contains(err.Error(), `--view "absent-view"`) {
				t.Errorf("error = %v, want the view-not-found message", err)
			}
			if strings.Contains(buf.String(), "no views") {
				t.Errorf("the command answered a question it was not asked:\n%s", buf.String())
			}
		})
	}
}
