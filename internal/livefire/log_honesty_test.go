//go:build livefire

package livefire

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// WP3 — log/cc family honesty. Findings #14 #16 #17 #18, read-only on both
// profiles, standing on rig capability R6 (testdata/fixtures/realistic-instance/
// custom_components/shapewatch/logshapes.py).
//
// The two profiles ask the same questions of different material. The rig's
// records are four deliberate shapes; the reference instance's are 54 entries
// of zigpy tracebacks, multi-entity service warnings and German prose. Neither
// alone is the corpus: the rig can be asked for a shape on demand and the live
// instance is the only place the shapes were ever observed.

// logProbe names, per profile, a component filter that matches entries whose
// DISPLAYED segment is not the matched one, and how many rows to expect at
// least. That is finding #16's shape, and the two instances carry it for
// different reasons: the rig by construction (a four-segment logger under
// shapewatch), the reference instance because HA's own template integration
// logs from homeassistant.components.template.config and two siblings.
func logProbe(p Profile) string {
	if p == Live {
		return "template"
	}
	return "shapewatch"
}

// logRows runs a log-family command with --json and decodes it.
func logRows(t *testing.T, tgt Target, args ...string) []map[string]any {
	t.Helper()
	out := tgt.MustRead(t, append(args, "--json", "--tokensmax", "0")...)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v --json is not a JSON array: %v\n%s", args, err, truncate(out))
	}
	return rows
}

// Finding #14: every list-style renderer in the log family cut the message to
// 60 bytes before it reached the renderer that knows who is reading, so no
// combination of --full, --json and --tokensmax 0 could recover it. On the
// reference instance 43 of 54 entries came back exactly 60 characters long
// while `log show` revealed multi-kilobyte tracebacks underneath.
//
// The assertion is that the machine contract carries the message Home Assistant
// sent, tested against the longest entry each instance holds — a fixed expected
// string would only prove the rig repeats itself.
func TestSweepLogJSONCarriesTheWholeMessage(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, args := range [][]string{
			{"log", "--top", "0"},
			{"log", "--unique", "--top", "0"},
			{"cc", "logs", ccLogSubject(t, tgt), "--top", "0"},
			{"cc", "logs", ccLogSubject(t, tgt), "--unique", "--top", "0"},
		} {
			rows := logRows(t, tgt, args...)
			if len(rows) == 0 {
				t.Fatalf("%v returned no entries — this instance cannot answer the question", args)
			}
			var longest int
			for _, row := range rows {
				msg, _ := row["message"].(string)
				if len(msg) > longest {
					longest = len(msg)
				}
				if len(msg) == logShapeBudget && strings.HasSuffix(msg, "...") {
					t.Errorf("%v --json carries a display truncation: %q is exactly %d bytes and "+
						"ends in the marker — a caller cannot get the rest of it from any flag",
						args, msg, logShapeBudget)
				}
				// A byte slice through a multi-byte character leaves bytes Go's
				// JSON encoder writes as U+FFFD, so the damage survives decoding
				// and is visible here. R6 puts a two-byte character exactly on
				// the cut; the reference instance is German and gets there on
				// its own.
				if !utf8.ValidString(msg) || strings.ContainsRune(msg, utf8.RuneError) {
					t.Errorf("%v --json carries a message cut through a multi-byte character: %q",
						args, msg)
				}
			}
			if longest <= logShapeBudget {
				t.Errorf("%v: the longest message is %d bytes, which the %d-byte cut never "+
					"reaches — this case would pass against the defect", args, longest, logShapeBudget)
			}
		}
	})
}

// Finding #14, the half the report did not name: the cut was a length test, so
// a message whose FIRST line is under the budget passed through it untouched
// and put a newline in a table cell. The reference instance printed 58 lines
// for 54 rows plus a header, with three rows split across two lines and the
// second line carrying no columns at all.
//
// A row per line is what makes a table a table, and it is the property a reader
// and every line-oriented tool downstream depend on.
func TestSweepLogTextIsOneRowPerLine(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		// --top 0 asks for every row and suppresses the "…+N more" footer, so
		// the line count is exactly the header plus the rows.
		text := tgt.MustRead(t, "log", "--top", "0", "--tokensmax", "0")
		rows := logRows(t, tgt, "log", "--top", "0")

		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if want := len(rows) + 1; len(lines) != want {
			t.Errorf("`log` printed %d lines for %d rows plus a header (want %d) — a message's "+
				"own newline reached the cell and split a row:\n%s",
				len(lines), len(rows), want, truncate(text))
		}
	})
}

// Finding #16: --component matched the full dotted logger name while the value
// hactl reported was its last segment, so `log --component template --json`
// answered rows whose component field read "config", "state" and "trigger" —
// none of which contain the filter term. A caller could not audit the match
// from the answer, and grepping the answer for their own filter missed rows
// that were correctly matched.
func TestSweepLogJSONComponentIsWhatTheFilterMatched(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		probe := logProbe(tgt.Profile)
		var innerSegment bool
		for _, args := range [][]string{
			{"log", "--component", probe, "--top", "0"},
			{"log", "--component", probe, "--unique", "--top", "0"},
		} {
			rows := logRows(t, tgt, args...)
			if len(rows) == 0 {
				t.Fatalf("%v matched nothing — %q names no logger on this instance", args, probe)
			}
			for _, row := range rows {
				component, _ := row["component"].(string)
				if !strings.Contains(component, probe) {
					t.Errorf("%v --json reports component %q, which does not contain the filter "+
						"term — this is not the value the match was made against", args, component)
				}
				if last := component[strings.LastIndex(component, ".")+1:]; !strings.Contains(last, probe) {
					innerSegment = true
				}
			}
		}
		if !innerSegment {
			t.Errorf("every matched logger carries %q in its LAST segment, so the reported value "+
				"would contain the filter term even under the defect — this instance cannot fail "+
				"the case", probe)
		}
	})
}

// Finding #17: `cc logs <name>` emitted component/level/message/time and no id,
// while its own --unique variant and plain `log` both emitted one. Combined
// with the truncation above, the default view was the one place in the family
// where a shortened message could not be traced back to `log show <id>` — the
// route the manual prescribes.
//
// The assertion is schema agreement across the family rather than "id exists in
// cc logs": three renderers for one record type is the condition, and a fourth
// would inherit it.
func TestSweepLogFamilyAgreesOnItsSchema(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		subject := ccLogSubject(t, tgt)
		for _, args := range [][]string{
			{"log", "--top", "0"},
			{"log", "--unique", "--top", "0"},
			{"cc", "logs", subject, "--top", "0"},
			{"cc", "logs", subject, "--unique", "--top", "0"},
		} {
			rows := logRows(t, tgt, args...)
			if len(rows) == 0 {
				t.Fatalf("%v returned no entries", args)
			}
			for _, field := range []string{"id", "level", "component", "message"} {
				if _, ok := rows[0][field]; !ok {
					t.Errorf("%v --json rows carry no %q — the family's views disagree about "+
						"what a log entry is, and this one cannot be drilled into", args, field)
				}
			}
			id, _ := rows[0]["id"].(string)
			if !strings.HasPrefix(id, "log:") {
				t.Errorf("%v --json reports id %q, which `log show` does not accept", args, id)
				continue
			}
			// An id is a claim that the entry can be fetched. Following it is
			// what turns the claim into a route.
			if _, err := tgt.Read(t, "log", "show", id, "--tokensmax", "0"); err != nil {
				t.Errorf("%v minted id %q and `log show` cannot resolve it: %v", args, id, err)
			}
		}
	})
}

// Finding #18: `cc logs totally_bogus_xyz` printed "no log entries for
// totally_bogus_xyz" at exit 0 — byte-identical to the answer for a real,
// installed, error-free component — while the sibling `cc show` refuses the
// same name at exit 1. A typo was indistinguishable from a quiet component.
//
// The control is the other half and matters as much: a real component with
// nothing to report must still answer, or the fix has traded one wrong answer
// for another.
func TestSweepCCLogsRefusesAComponentThatIsNotInstalled(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		out, err := tgt.Read(t, "cc", "logs", "totally_bogus_xyz", "--tokensmax", "0")
		if err == nil {
			t.Errorf("`cc logs totally_bogus_xyz` exited 0 — a typo reads as a quiet component:\n%s", out)
		}
		if code := ExitCode(err); code != 1 {
			t.Errorf("exit code %d, want 1 — `cc show` refuses the same name with 1", code)
		}

		// The control: a component that IS installed answers, whether or not it
		// has anything to say.
		quiet := quietComponent(t, tgt)
		if _, quietErr := tgt.Read(t, "cc", "logs", quiet, "--tokensmax", "0"); quietErr != nil {
			t.Errorf("`cc logs %s` failed for an installed component: %v", quiet, quietErr)
		}
	})
}

// ccLogSubject names a custom component this instance has log entries for.
//
// It is discovered rather than hard-coded: the rig's shapewatch logs on every
// setup, but which of the reference instance's fourteen components is currently
// noisy is a property of the day. A case that names one is a case that goes red
// for a reason that is not a defect.
func ccLogSubject(t *testing.T, tgt Target) string {
	t.Helper()
	for _, domain := range ccDomains(t, tgt) {
		if rows := logRows(t, tgt, "cc", "logs", domain, "--top", "0"); len(rows) > 0 {
			return domain
		}
	}
	t.Fatalf("no custom component on this instance has log entries — the log family cannot be " +
		"exercised through cc here")
	return ""
}

// quietComponent names an installed custom component with no log entries, which
// is the answer `cc logs` must keep giving at exit 0.
func quietComponent(t *testing.T, tgt Target) string {
	t.Helper()
	domains := ccDomains(t, tgt)
	for _, domain := range domains {
		if rows := logRows(t, tgt, "cc", "logs", domain, "--top", "0"); len(rows) == 0 {
			return domain
		}
	}
	// Every component is noisy: the control still has something to assert, so
	// use the first one rather than skipping the case entirely.
	if len(domains) > 0 {
		return domains[0]
	}
	t.Fatal("the instance reports no custom components at all")
	return ""
}

func ccDomains(t *testing.T, tgt Target) []string {
	t.Helper()
	out := tgt.MustRead(t, "cc", "ls", "--top", "0", "--json", "--tokensmax", "0")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("cc ls --json: %v\n%s", err, truncate(out))
	}
	domains := make([]string, 0, len(rows))
	for _, row := range rows {
		if d, ok := row["domain"].(string); ok && d != "" {
			domains = append(domains, d)
		}
	}
	return domains
}
