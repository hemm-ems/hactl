//go:build livefire

package livefire

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WP10 — the global flag contract (INVARIANTS.md H-25, docs/decisions.md D-41).
//
// Findings #6 #12 #13 #47 #48 #50 #53 #54 #56. Read-only on both profiles: every
// case here is either refused before a connection is opened, or asks the
// instance a question it already answers.
//
// The cases are on both profiles even though most of them never reach Home
// Assistant, because the subject is what the BINARY does with what the caller
// typed, and the live profile is where the reproductions were taken. A case
// that only ran on the rig would be asserting the rule against the tree the rig
// happens to build — which is the same shape as testing a fixture instead of an
// instance (§1).

// TestSweepAFlagACommandCannotActOnIsRefused is #54, and the reason it is a
// class rather than seven papercuts: `--since` was a root persistent flag on all
// 112 commands and read by nine, so `area ls --since garbage-value-xyz` exited 0
// with output byte-identical to `area ls` while `log` and `changes` refused the
// identical value.
//
// The commands that DO take it are asserted in the same loop. Without them the
// case is satisfied by a build in which `--since` works nowhere.
func TestSweepAFlagACommandCannotActOnIsRefused(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, cmd := range [][]string{
			{"area", "ls"}, {"device", "ls"}, {"cc", "ls"},
			{"energy", "show"}, {"helper", "ls"}, {"health"}, {"issues"},
		} {
			args := append(append([]string{}, cmd...), "--since", "garbage-value-xyz", "--json", "--tokensmax", "0")
			out, err := tgt.Read(t, args...)
			if err == nil {
				t.Errorf("hactl %s exited 0 — the flag did nothing and said nothing", strings.Join(args, " "))
				continue
			}
			if out != "" {
				t.Errorf("hactl %s wrote %.60q to stdout while refusing", strings.Join(args, " "), out)
			}
			stderr, _ := tgt.ReadDiagnostic(t, args...)
			if !strings.Contains(stderr, "it is declared by:") {
				t.Errorf("hactl %s refused without naming where --since lives:\n%s", strings.Join(args, " "), stderr)
			}
		}

		// The control: on a command that reads the window, a well-formed value
		// is accepted and a malformed one is refused for its own reason.
		if _, err := tgt.Read(t, "changes", "--since", "1h", "--json", "--tokensmax", "0"); err != nil {
			t.Errorf("changes --since 1h: %v", err)
		}
		stderr, err := tgt.ReadDiagnostic(t, "changes", "--since", "notaduration", "--json")
		if err == nil {
			t.Error("changes --since notaduration exited 0")
		}
		if !strings.Contains(stderr, "invalid duration") {
			t.Errorf("changes --since notaduration refused for the wrong reason:\n%s", stderr)
		}
	})
}

// TestSweepABoundThatCannotBoundIsRefused is #47, #53 and #56 together, because
// they are one question asked of three flags: what does a caller's number mean
// when the flag cannot honour it?
//
// `--timeout 0s` is the sharp one. H-23 says every connection hactl opens is
// bounded by the caller's `--timeout`, so a zero that removes the bound makes
// that law vacuous — and `--timeout -1s` reached net.Dialer as a deadline
// already in the past, so hactl reported `dial tcp: lookup <host>: i/o timeout`
// against a host that was up. The refusal is asserted to happen without a
// connection: an unreachable value must not produce a network diagnosis.
func TestSweepABoundThatCannotBoundIsRefused(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, tc := range []struct {
			args []string
			says string
		}{
			{[]string{"ent", "ls", "--top", "-1"}, "--top counts the rows"},
			{[]string{"ent", "ls", "--top", "-999"}, "--top counts the rows"},
			{[]string{"ent", "ls", "--tokensmax", "-5"}, "--tokensmax counts the tokens"},
			{[]string{"health", "--timeout", "0s"}, "bounds every connection"},
			{[]string{"health", "--timeout", "-1s"}, "bounds every connection"},
		} {
			out, err := tgt.Read(t, tc.args...)
			if err == nil {
				t.Errorf("hactl %s exited 0", strings.Join(tc.args, " "))
				continue
			}
			if out != "" {
				t.Errorf("hactl %s wrote %.60q to stdout while refusing", strings.Join(tc.args, " "), out)
			}
			stderr, _ := tgt.ReadDiagnostic(t, tc.args...)
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("hactl %s refused without saying why:\n%s", strings.Join(tc.args, " "), stderr)
			}
			// The reported symptom, in the negative: a flag value must never
			// come back as a transport failure.
			for _, network := range []string{"i/o timeout", "dial tcp", "context deadline exceeded"} {
				if strings.Contains(stderr, network) {
					t.Errorf("hactl %s blamed the network for a flag value: %s", strings.Join(tc.args, " "), stderr)
				}
			}
		}

		// The controls. `--top 0` means every row and is documented as such;
		// a positive timeout still works.
		capped := tgt.MustRead(t, "ent", "ls", "--top", "1", "--tokensmax", "0")
		all := tgt.MustRead(t, "ent", "ls", "--top", "0", "--tokensmax", "0")
		if len(strings.Split(all, "\n")) <= len(strings.Split(capped, "\n")) {
			t.Errorf("--top 0 answered with %d lines and --top 1 with %d — 0 must lift the row cap",
				len(strings.Split(all, "\n")), len(strings.Split(capped, "\n")))
		}
		tgt.MustRead(t, "health", "--timeout", "30s", "--json")
	})
}

// TestSweepAMistypedFlagOffersTheNearestOne is #48: a mistyped flag gets the
// help a mistyped subcommand has always got. The subcommand form is run beside
// it, because the finding was about the ASYMMETRY between the two.
func TestSweepAMistypedFlagOffersTheNearestOne(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"ent", "ls", "--tpo", "5"}, "did you mean --top?"},
			{[]string{"ent", "ls", "--jso"}, "did you mean --json?"},
		} {
			stderr, err := tgt.ReadDiagnostic(t, tc.args...)
			if err == nil {
				t.Errorf("hactl %s exited 0", strings.Join(tc.args, " "))
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("hactl %s did not offer %q:\n%s", strings.Join(tc.args, " "), tc.want, stderr)
			}
		}
		stderr, _ := tgt.ReadDiagnostic(t, "ento", "ls")
		if !strings.Contains(stderr, "Did you mean this?") {
			t.Errorf("the subcommand half of the asymmetry has changed:\n%s", stderr)
		}
	})
}

// TestSweepTwoSpellingsOfTheVersionAnswerTheSame is #13. Both flag orders are
// run because the report found the symptom identical for either.
func TestSweepTwoSpellingsOfTheVersionAnswerTheSame(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, mode := range [][]string{nil, {"--json"}} {
			flagForm := tgt.MustRead(t, append([]string{"--version"}, mode...)...)
			reversed := tgt.MustRead(t, append(append([]string{}, mode...), "--version")...)
			cmdForm := tgt.MustRead(t, append([]string{"version"}, mode...)...)
			if flagForm != cmdForm {
				t.Errorf("--version %v answered %.60q, version %v answered %.60q", mode, flagForm, mode, cmdForm)
			}
			if flagForm != reversed {
				t.Errorf("flag order changed the answer: %.60q vs %.60q", flagForm, reversed)
			}
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(tgt.MustRead(t, "--version", "--json")), &doc); err != nil {
			t.Errorf("--version --json is not a JSON document: %v", err)
		}
	})
}

// TestSweepTplEvalRefusesTwoTemplates is #6: two inputs naming one thing.
func TestSweepTplEvalRefusesTwoTemplates(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "probe.jinja")
		if err := os.WriteFile(path, []byte("{{ 99 + 1 }}\n"), 0o600); err != nil {
			t.Fatalf("writing the probe template: %v", err)
		}
		// Each input alone is the control: without them, "refuse both" is
		// satisfied by refusing either.
		if got := strings.TrimSpace(tgt.MustRead(t, "tpl", "eval", "{{ 1+1 }}")); got != "2" {
			t.Errorf("tpl eval with an inline template answered %q, want 2", got)
		}
		if got := strings.TrimSpace(tgt.MustRead(t, "tpl", "eval", "-f", path)); got != "100" {
			t.Errorf("tpl eval -f answered %q, want 100", got)
		}

		out, err := tgt.Read(t, "tpl", "eval", "{{ 1+1 }}", "-f", path)
		if err == nil {
			t.Fatalf("tpl eval accepted both and answered %q — one input was discarded in silence", strings.TrimSpace(out))
		}
		stderr, _ := tgt.ReadDiagnostic(t, "tpl", "eval", "{{ 1+1 }}", "-f", path)
		if !strings.Contains(stderr, "only one can be honoured") {
			t.Errorf("tpl eval refused for another reason:\n%s", stderr)
		}
	})
}

// TestSweepTheManualReachesAJSONCaller is #50, and it is the one case here that
// needs a real run rather than a refusal.
//
// A brand-new HACTL_SESSION is the whole experiment: with `--json`, `health` and
// `device ls` used to write ZERO bytes to stderr and record no session at all,
// while the same commands without the flag delivered ten kilobytes — so an agent
// reading only structured output never received the routing table. The plain
// form runs beside it as the control, because "stderr is non-empty" is otherwise
// satisfied by any warning the instance happens to emit.
func TestSweepTheManualReachesAJSONCaller(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, args := range [][]string{{"health"}, {"device", "ls"}} {
			withJSON := append(append([]string{}, args...), "--json")
			jsonBytes := len(manualDelivery(t, tgt, withJSON))
			plainBytes := len(manualDelivery(t, tgt, args))
			if plainBytes == 0 {
				t.Fatalf("hactl %s delivered no manual at all — the premise of this case is gone",
					strings.Join(args, " "))
			}
			if jsonBytes == 0 {
				t.Errorf("hactl %s delivered %d bytes of manual and hactl %s delivered none — "+
					"--json decides what is on stdout, not who is told how to use the tool",
					strings.Join(args, " "), plainBytes, strings.Join(withJSON, " "))
			}
		}
	})
}

// manualDelivery runs args in a session no hactl has ever seen and returns what
// reached stderr.
//
// The key carries the process id, and that is not decoration. A session's state
// lives in the instance's own cache under a 30-minute sliding TTL
// (manual.DefaultTTL), so a key derived from the test name alone is brand-new
// exactly once and "already delivered" for the next half hour — this case
// passed standalone and failed in the sweep twenty minutes later, on a live
// profile whose cache still held the first run's session. A test that poisons
// its own precondition is worse than no test: it is green on the run that
// created the state and red on every re-run, which is the opposite of the
// signal a gate exists to give.
func manualDelivery(t *testing.T, tgt Target, args []string) string {
	t.Helper()
	key := fmt.Sprintf("pg_wp10_%d_%s_%s", os.Getpid(),
		strings.ReplaceAll(strings.Join(args, "_"), "-", ""), t.Name())
	t.Setenv("HACTL_SESSION", key)
	t.Setenv("HACTL_MANUAL_MODE", "progressive")
	stderr, err := tgt.ReadDiagnostic(t, args...)
	if err != nil {
		t.Fatalf("hactl %s: %v", strings.Join(args, " "), err)
	}
	return stderr
}
