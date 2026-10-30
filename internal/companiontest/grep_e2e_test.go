//go:build companion

package companiontest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// `hactl grep` end to end (hactl #115, D-48..D-50): the config half answered by
// a real companion's GET /v1/ref/grep over a real config directory, the
// dashboard half by a real Home Assistant's lovelace/config — the only tier
// where the two engines that must agree (Python on one side, Go on the other)
// both run.

const (
	grepE2EAutomationID = "grep_e2e_probe"
	grepE2EDashboard    = "grep-e2e"
)

// The seeded values. Each D-49 pin needs a value on BOTH halves, in a case or
// spelling that differs from the other half's, so one assertion asks both
// engines the same question:
//
//   - `sensor.balkony_p_ac` in a trigger (config) and a card (dashboard): AC-1,
//     the field report, as a fragment search.
//   - `Küche` in the alias (config), `KÜCHE` in a view title (dashboard): the
//     non-ASCII lowercase pin.
//   - `Straße` (config) and `STRASSE` (dashboard): no case folding on either
//     side, so `straße` finds only the first and `strasse` only the second.
//   - `grep_e2e_tail` closing a value that ends in a newline on both halves —
//     a block scalar in config, a markdown body in the dashboard: `$` is read
//     the same way by both engines.
//   - `ıstanbul` (dotless ı, config) and `İSTANBUL` (dotted İ, dashboard): the
//     characters Python's re.IGNORECASE folds wider than Go's (?i), so a
//     case-insensitive --regex asks both engines Python's question.
const grepE2EAutomation = `id: grep_e2e_probe
alias: Grep E2E Küche Probe
description: |
  Straße notes grep_e2e_tail
mode: single
variables:
  grep_e2e_city: ıstanbul grep_e2e_turkic
trigger:
  - platform: state
    entity_id: sensor.balkony_p_ac
action:
  - delay: "00:00:01"
`

const grepE2EDashboardConfig = `{"views":[{"title":"KÜCHE grep view","path":"grep",
 "cards":[{"type":"entity","entity":"sensor.balkony_p_ac"},
          {"type":"markdown","content":"STRASSE notes grep_e2e_tail\n"},
          {"type":"markdown","content":"İSTANBUL grep_e2e_turkic"}]}]}`

// grepE2EDoc is the part of `grep --json` the assertions read.
type grepE2EDoc struct {
	Total   int  `json:"total"`
	Partial bool `json:"partial"`
	Hits    []struct {
		Source   string `json:"source"`
		Location string `json:"location"`
		Path     string `json:"path"`
		Key      string `json:"key"`
		Kind     string `json:"kind"`
		Value    string `json:"value"`
	} `json:"hits"`
}

// has reports whether the document carries a hit from source/location whose
// value is exactly value.
func (d grepE2EDoc) has(source, location, value string) bool {
	for _, h := range d.Hits {
		if h.Source == source && h.Location == location && h.Value == value {
			return true
		}
	}
	return false
}

var seedGrepE2EOnce sync.Once

// seedGrepE2E writes the probe automation and the probe dashboard through
// hactl, then confirms both landed by asking the instance directly (H-12): the
// grep assertions below are only as good as the ground truth they compare to.
func seedGrepE2E(t *testing.T) {
	t.Helper()
	var failure string
	seedGrepE2EOnce.Do(func() {
		dir := t.TempDir()
		auto := filepath.Join(dir, "auto.yaml")
		dash := filepath.Join(dir, "dash.json")
		if err := os.WriteFile(auto, []byte(grepE2EAutomation), 0o600); err != nil {
			failure = err.Error()
			return
		}
		if err := os.WriteFile(dash, []byte(grepE2EDashboardConfig), 0o600); err != nil {
			failure = err.Error()
			return
		}
		if out, err := runHactlE2E(t, "auto", "create", "--confirm", "-f", auto); err != nil {
			failure = "auto create: " + err.Error() + "\n" + out
			return
		}
		if out, err := runHactlE2E(t, "dash", "create", "--url-path", grepE2EDashboard,
			"--title", "Grep E2E", "--confirm"); err != nil && !strings.Contains(out, "already exists") {
			failure = "dash create: " + err.Error() + "\n" + out
			return
		}
		if out, err := runHactlE2E(t, "dash", "save", grepE2EDashboard, "-f", dash, "--confirm"); err != nil {
			failure = "dash save: " + err.Error() + "\n" + out
			return
		}
	})
	if failure != "" {
		t.Fatalf("seeding the grep probe failed: %s", failure)
	}

	if cfg := readConfigFileE2E(t, "automations.yaml"); !strings.Contains(cfg, grepE2EAutomationID) {
		t.Fatalf("the probe automation is not in automations.yaml:\n%s", cfg)
	}
	ws := haapi.NewWSClient(haURL, haToken)
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("connecting to HA: %v", err)
	}
	defer ws.Close() //nolint:errcheck // best-effort close in test helper
	raw, err := ws.DashboardConfigRaw(context.Background(), grepE2EDashboard)
	if err != nil {
		t.Fatalf("the probe dashboard is not stored: %v", err)
	}
	if !strings.Contains(string(raw), "sensor.balkony_p_ac") {
		t.Fatalf("the probe dashboard does not hold the seeded value:\n%s", raw)
	}
}

// grepE2E runs `hactl grep … --json` and decodes the document.
func grepE2E(t *testing.T, args ...string) grepE2EDoc {
	t.Helper()
	full := append([]string{"grep"}, args...)
	full = append(full, "--json", "--tokensmax", "0")
	stdout, stderr, err := runHactlE2EStreams(t, full...)
	if err != nil {
		t.Fatalf("hactl %v failed: %v\nstdout:\n%s\nstderr:\n%s", full, err, stdout, stderr)
	}
	var doc grepE2EDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("hactl %v --json does not parse: %v\n%s", full, err, stdout)
	}
	if doc.Partial {
		t.Fatalf("hactl %v swept partially on the E2E instance:\n%s", full, stdout)
	}
	return doc
}

// TestE2EGrepFindsATermInBothHalves is AC-1 against a real companion and a real
// HA: a fragment of an entity id finds it in a config trigger AND in a
// dashboard card, in one call, with the key it sits under. The plain-text
// answer leads with the per-source summary.
func TestE2EGrepFindsATermInBothHalves(t *testing.T) {
	seedGrepE2E(t)

	doc := grepE2E(t, "balkony")
	if !doc.has("config", "automations.yaml", "sensor.balkony_p_ac") {
		t.Errorf("config half missing the trigger's sensor.balkony_p_ac: %+v", doc.Hits)
	}
	if !doc.has("dashboard", grepE2EDashboard, "sensor.balkony_p_ac") {
		t.Errorf("dashboard half missing the card's sensor.balkony_p_ac — the field report: %+v", doc.Hits)
	}
	for _, h := range doc.Hits {
		if h.Source == "dashboard" && h.Location == grepE2EDashboard && h.Path != "views[0].cards[0].entity" {
			t.Errorf("dashboard hit at %q, want views[0].cards[0].entity", h.Path)
		}
		if h.Source == "config" && h.Value == "sensor.balkony_p_ac" && h.Key != "entity_id" {
			t.Errorf("config hit carries key %q, want entity_id", h.Key)
		}
	}

	out, err := runHactlE2E(t, "grep", "balkony", "--tokensmax", "0")
	if err != nil {
		t.Fatalf("hactl grep balkony: %v\n%s", err, out)
	}
	for _, want := range []string{`grep "balkony" (substring, case-insensitive)`, "automations.yaml", grepE2EDashboard} {
		if !strings.Contains(out, want) {
			t.Errorf("plain answer missing %q:\n%s", want, out)
		}
	}

	// The exact-id commands are untouched (AC-7): the fragment is still not
	// a whole value anywhere.
	if out, err := runHactlE2E(t, "dash", "grep", "balkony"); err != nil || !strings.Contains(out, "not referenced as a whole value") {
		t.Errorf("dash grep changed behaviour (err %v):\n%s", err, out)
	}
}

// TestE2EGrepNonASCIITermThroughBothHalves is D-49's pin through both engines:
// one lowercase rule (simple Unicode lowercase, never case folding) on the
// Python half and the Go half alike.
func TestE2EGrepNonASCIITermThroughBothHalves(t *testing.T) {
	seedGrepE2E(t)

	kueche := grepE2E(t, "küche")
	if !kueche.has("config", "automations.yaml", "Grep E2E Küche Probe") {
		t.Errorf("config half (Python) missed Küche for küche: %+v", kueche.Hits)
	}
	if !kueche.has("dashboard", grepE2EDashboard, "KÜCHE grep view") {
		t.Errorf("dashboard half (Go) missed KÜCHE for küche: %+v", kueche.Hits)
	}

	// No ß→ss on either side: each spelling finds only itself.
	strasse := grepE2E(t, "straße")
	if !strasse.has("config", "automations.yaml", "Straße notes grep_e2e_tail\n") {
		t.Errorf("straße must find Straße in config: %+v", strasse.Hits)
	}
	if strasse.has("dashboard", grepE2EDashboard, "STRASSE notes grep_e2e_tail\n") {
		t.Errorf("straße found STRASSE on the dashboard half: that is case folding, which D-49 rules out")
	}
	ss := grepE2E(t, "strasse")
	if !ss.has("dashboard", grepE2EDashboard, "STRASSE notes grep_e2e_tail\n") {
		t.Errorf("strasse must find STRASSE in the dashboard: %+v", ss.Hits)
	}
	if ss.has("config", "automations.yaml", "Straße notes grep_e2e_tail\n") {
		t.Errorf("strasse found Straße on the config half: that is case folding, which D-49 rules out")
	}

	// --regex folds case as Python's re.IGNORECASE does, on BOTH halves: I, i,
	// İ and ı are one letter there, while Go's (?i) alone keeps İ and ı apart
	// from i — which made the dashboard half miss what the config half found.
	for _, pattern := range []string{`istanbul grep_e2e_turkic`, `^[a-z]stanbul grep_e2e_turkic$`} {
		folded := grepE2E(t, "--regex", pattern)
		if !folded.has("config", "automations.yaml", "ıstanbul grep_e2e_turkic") {
			t.Errorf("--regex %q: config half (Python) missed ıstanbul: %+v", pattern, folded.Hits)
		}
		if !folded.has("dashboard", grepE2EDashboard, "İSTANBUL grep_e2e_turkic") {
			t.Errorf("--regex %q: dashboard half (Go) missed İSTANBUL, which Python's IGNORECASE matches: %+v", pattern, folded.Hits)
		}
	}
	if exact := grepE2E(t, "--regex", "istanbul grep_e2e_turkic", "--case-sensitive"); exact.Total != 0 {
		t.Errorf("--case-sensitive --regex istanbul must find neither spelling: %+v", exact.Hits)
	}

	// --case-sensitive: compares as written on both halves.
	exact := grepE2E(t, "KÜCHE", "--case-sensitive")
	if exact.has("config", "automations.yaml", "Grep E2E Küche Probe") || !exact.has("dashboard", grepE2EDashboard, "KÜCHE grep view") {
		t.Errorf("--case-sensitive KÜCHE must find the title and not the alias: %+v", exact.Hits)
	}
}

// TestE2EGrepDollarAgreesAcrossHalves: Python's `$` matches before one final
// newline and Go's does not; both seeded values end in one. Without the
// dashboard half's Python reading of `$`, the config half answers and the
// dashboard half silently does not.
func TestE2EGrepDollarAgreesAcrossHalves(t *testing.T) {
	seedGrepE2E(t)

	doc := grepE2E(t, "--regex", "grep_e2e_tail$")
	if !doc.has("config", "automations.yaml", "Straße notes grep_e2e_tail\n") {
		t.Errorf("config half (Python) missed the block scalar: %+v", doc.Hits)
	}
	if !doc.has("dashboard", grepE2EDashboard, "STRASSE notes grep_e2e_tail\n") {
		t.Errorf("dashboard half (Go) missed the markdown body the config half found: %+v", doc.Hits)
	}

	// A construct the engines read differently is refused before anything is
	// asked of either (AC-6).
	out, err := runHactlE2E(t, "grep", "--regex", `grep_e2e_\w+`)
	if err == nil {
		t.Fatalf(`grep --regex '\w' must refuse; it answered:\n%s`, out)
	}
	if !strings.Contains(out, "ASCII-only in Go") {
		t.Errorf("the refusal must say why:\n%s", out)
	}
}
