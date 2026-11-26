package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// docExampleFiles are the documents that show complete example policies.
var docExampleFiles = []string{
	"../../docs/policy.md",
	"../../docs/setup.md",
	"../../docs/skills/policy-setup.md",
	"../../docs/manual.md",
}

// docExampleIntended are the findings an example is written to draw, by the
// document and the example's first line in it: none today. An example that
// shows a finding on purpose is listed here with the finding's code.
var docExampleIntended = map[string][]string{}

var fenceRe = regexp.MustCompile("^(\\s*)```(\\S*)\\s*$")

// docExamples returns every fenced block of a document that is a complete
// policy file — it declares `version:` and `profiles:` — keyed by
// "<file>:<line of its first line>".
func docExamples(t *testing.T, file string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(file) //nolint:gosec // a fixed document of the repository (docExampleFiles)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	lines := strings.Split(string(data), "\n")
	out := map[string]string{}
	for i := 0; i < len(lines); i++ {
		m := fenceRe.FindStringSubmatch(lines[i])
		if m == nil || m[2] == "" {
			continue
		}
		indent, start := m[1], i+1
		var body []string
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "```"; i++ {
			body = append(body, strings.TrimPrefix(lines[i], indent))
		}
		text := strings.Join(body, "\n") + "\n"
		if regexp.MustCompile(`(?m)^version:`).MatchString(text) && regexp.MustCompile(`(?m)^profiles:`).MatchString(text) {
			out[fmt.Sprintf("%s:%d", filepath.Base(file), start+1)] = text
		}
	}
	return out
}

// TestDocumentedExamplePoliciesLintClean — every complete example policy the
// documentation shows loads and draws no finding `policy lint` can make
// without an instance (redundant allows, verbs that apply nowhere, a control
// without its service, a part that blocks an allow, template reads in a
// profile that denies reads), unless the example is written to draw it
// (docExampleIntended). An owner copies these: an example the linter
// complains about teaches the wrong thing.
func TestDocumentedExamplePoliciesLintClean(t *testing.T) {
	seen := 0
	for _, file := range docExampleFiles {
		for at, text := range docExamples(t, file) {
			seen++
			path := filepath.Join(t.TempDir(), "policy.yaml")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			pol, _, err := LoadDraft(path)
			if err != nil {
				t.Errorf("%s: the example does not load: %v", at, err)
				continue
			}
			findings := Lint(pol, nil).Findings
			codes := make([]string, 0, len(findings))
			for _, f := range findings {
				if !slices.Contains(docExampleIntended[at], f.Code) {
					t.Errorf("%s: finding %s on line %d (%s): %s", at, f.Code, f.Line, f.Rule, f.Message)
				}
				codes = append(codes, f.Code)
			}
			for _, want := range docExampleIntended[at] {
				if !slices.Contains(codes, want) {
					t.Errorf("%s: the example no longer draws the %s it is written to show", at, want)
				}
			}
		}
	}
	if seen < 4 {
		t.Errorf("found %d complete example policies in the documentation, want at least the 4 it shows", seen)
	}
}
