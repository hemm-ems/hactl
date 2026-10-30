//go:build livefire

package livefire

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// `hactl grep` (hactl #115, D-48..D-50) on both profiles.
//
// The rig cannot reproduce the field report from its fixture: realdata replaces
// the house's vocabulary by design, so `balkony` hits nothing there and the
// rig's dashboards hold seven string leaves in total (lane B's measurement). So
// this case seeds its own dashboard value — in the playground namespace, so the
// live profile's guard accepts it and the run-level census treats it as the
// case's own — and removes it again.
//
// The config half runs against a stub companion on both profiles: the rig has
// none (R11), and the live add-on predates GET /v1/ref/grep until the companion
// release that carries it. Against a real companion the config half is proven
// by the companion tier (TestE2EGrepFindsATermInBothHalves). What this case
// owns is the half a real house's dashboards answer, and the refusals that
// happen before anything is contacted.

const (
	grepProbeDashboard = "pg-grep-probe"
	grepProbeValue     = "sensor.pg_grep_balkony_p_ac"
)

// TestSweepGrepFindsATermInsideADashboardValue is AC-1 on both profiles: a
// fragment of a dashboard value, in another case, is found in one call, with
// the dashboard, path, key and whole value — where `dash grep` (whole values
// only) still finds nothing for the same fragment.
func TestSweepGrepFindsATermInsideADashboardValue(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		seedGrepProbeDashboard(t, tgt)

		var calls atomic.Int32
		cc := withStubCompanion(t, tgt, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.URL.Path != "/v1/ref/grep" {
				t.Errorf("grep asked the companion for %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			term, _ := json.Marshal(r.URL.Query().Get("term"))
			_, _ = fmt.Fprintf(w, `{"term":%s,"regex":false,"case_sensitive":false,"hits":[],"total":0,"truncated":false,"sources":[]}`, term)
		})

		assertGrepRefusalsCostNoConnection(t, cc, &calls)

		out := cc.MustRead(t, "grep", "PG_GREP_BALKONY", "--json", "--tokensmax", "0")
		doc := parseGrepSweepDoc(t, out)
		found := false
		for _, h := range doc.Hits {
			if h.Source == "dashboard" && h.Location == grepProbeDashboard {
				found = true
				if h.Path != "views[0].cards[0].entity" || h.Key != "entity" || h.Kind != "value" || h.Value != grepProbeValue {
					t.Errorf("probe hit = %+v, want views[0].cards[0].entity / entity / value / %s", h, grepProbeValue)
				}
			}
		}
		if !found {
			t.Fatalf("grep missed the fragment inside %s's card value — the field report:\n%s",
				grepProbeDashboard, truncate(out))
		}
		// The counts reconcile with the rows (H-11): nothing here is capped.
		sum := 0
		for _, src := range doc.Sources {
			sum += src.ValueHits
		}
		if sum != doc.Total || len(doc.Hits) != doc.Total {
			t.Errorf("sources sum to %d, total says %d, rows number %d", sum, doc.Total, len(doc.Hits))
		}

		plain := cc.MustRead(t, "grep", "pg_grep_balkony", "--tokensmax", "0")
		for _, want := range []string{`grep "pg_grep_balkony" (substring, case-insensitive)`, grepProbeDashboard, grepProbeValue} {
			if !strings.Contains(plain, want) {
				t.Errorf("plain answer missing %q:\n%s", want, truncate(plain))
			}
		}

		// The exact-value command is unchanged (AC-7): a fragment is not a
		// whole value, so it is still not a hit there.
		if exact := tgt.MustRead(t, "dash", "grep", "pg_grep_balkony"); strings.Contains(exact, grepProbeDashboard) {
			t.Errorf("`dash grep` matched a fragment — its whole-value contract changed:\n%s", truncate(exact))
		}
	})
}

// assertGrepRefusalsCostNoConnection: a degenerate term or a pattern outside
// the two engines' common subset is refused before anything is contacted, so
// the stub companion sees no request for any of them (AC-5, AC-6).
func assertGrepRefusalsCostNoConnection(t *testing.T, cc Target, calls *atomic.Int32) {
	t.Helper()
	for _, args := range [][]string{
		{"grep", "a"},
		{"grep", "  "},
		{"grep", "--regex", `\d+`},
		{"grep", "--regex", "wp_(?=vl)"},
	} {
		if out, err := cc.Read(t, args...); err == nil {
			t.Errorf("`%s` must refuse, it answered:\n%s", strings.Join(args, " "), truncate(out))
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("a refused term reached the companion %d time(s)", n)
	}
}

// grepSweepDoc is the part of `grep --json` this case reads.
type grepSweepDoc struct {
	Total   int  `json:"total"`
	Partial bool `json:"partial"`
	Sources []struct {
		Source    string `json:"source"`
		Location  string `json:"location"`
		ValueHits int    `json:"value_hits"`
	} `json:"sources"`
	Hits []struct {
		Source   string `json:"source"`
		Location string `json:"location"`
		Path     string `json:"path"`
		Key      string `json:"key"`
		Kind     string `json:"kind"`
		Value    string `json:"value"`
	} `json:"hits"`
}

func parseGrepSweepDoc(t *testing.T, out string) grepSweepDoc {
	t.Helper()
	var doc grepSweepDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("`grep --json` does not parse: %v\n%s", err, truncate(out))
	}
	return doc
}

// seedGrepProbeDashboard stores a playground dashboard whose first card holds
// grepProbeValue, and removes it when the case ends. Every --confirm follows
// its own dry run, as the confirm guard requires of a non-interactive caller.
func seedGrepProbeDashboard(t *testing.T, tgt Target) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "pg-grep-probe.json")
	doc := fmt.Sprintf(`{"views":[{"title":"PG grep probe","path":"pg-grep",
		"cards":[{"type":"entity","entity":%q},{"type":"markdown","content":"PG grep probe body"}]}]}`, grepProbeValue)
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := []string{grepProbeDashboard}
	vocab := []string{"dash", "create", "save", "delete", "--url-path", "--title", "PG grep probe", "-f", file, "--confirm"}

	if !dashboardIsListed(t, tgt, grepProbeDashboard) {
		createArgs := []string{"dash", "create", "--url-path", grepProbeDashboard, "--title", "PG grep probe"}
		if out, err := tgt.Read(t, createArgs...); err != nil {
			t.Fatalf("dry run of the probe dashboard's create failed: %v\n%s", err, out)
		}
		if out, err := tgt.Write(t, targets, vocab, append(createArgs, "--confirm")); err != nil {
			t.Fatalf("creating the probe dashboard failed: %v\n%s", err, out)
		}
	}
	t.Cleanup(func() {
		deleteArgs := []string{"dash", "delete", grepProbeDashboard}
		if out, err := tgt.Read(t, deleteArgs...); err != nil {
			t.Errorf("dry run of the probe dashboard's delete failed: %v\n%s", err, out)
			return
		}
		if out, err := tgt.Write(t, targets, vocab, append(deleteArgs, "--confirm")); err != nil {
			t.Errorf("removing the probe dashboard failed: %v\n%s", err, out)
		}
	})

	saveArgs := []string{"dash", "save", grepProbeDashboard, "-f", file}
	if out, err := tgt.Read(t, saveArgs...); err != nil {
		t.Fatalf("dry run of the probe dashboard's save failed: %v\n%s", err, out)
	}
	if out, err := tgt.Write(t, targets, vocab, append(saveArgs, "--confirm")); err != nil {
		t.Fatalf("saving the probe dashboard failed: %v\n%s", err, out)
	}
	if raw := tgt.MustRead(t, "dash", "show", grepProbeDashboard, "--raw"); !strings.Contains(raw, grepProbeValue) {
		t.Fatalf("the probe dashboard does not hold %s after the save:\n%s", grepProbeValue, truncate(raw))
	}
}
