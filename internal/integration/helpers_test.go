//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/cmd"
)

// runHactl executes a hactl command against the shared HA instance
// and returns the captured stdout output.
// It sets HACTL_DIR to point to the HA instance directory so config
// discovery finds the .env automatically.
func runHactl(t *testing.T, args ...string) string {
	t.Helper()

	// Set HACTL_DIR so config.Load finds the .env
	t.Setenv("HACTL_DIR", ha.Dir())

	var buf bytes.Buffer
	err := cmd.RunWithOutput(append([]string{"hactl"}, args...), &buf)
	if err != nil {
		t.Fatalf("hactl %v failed: %v\noutput: %s", args, err, buf.String())
	}
	return buf.String()
}

// runHactlDir executes a hactl command against a specific instance directory.
func runHactlDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Setenv("HACTL_DIR", dir)

	var buf bytes.Buffer
	err := cmd.RunWithOutput(append([]string{"hactl"}, args...), &buf)
	if err != nil {
		t.Fatalf("hactl %v failed: %v\noutput: %s", args, err, buf.String())
	}
	return buf.String()
}

// runHactlErr executes a hactl command and returns both output and error.
// Unlike runHactl, it does NOT t.Fatalf on error â€” use for error-path tests.
func runHactlErr(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HACTL_DIR", ha.Dir())

	var buf bytes.Buffer
	err := cmd.RunWithOutput(append([]string{"hactl"}, args...), &buf)
	return buf.String(), err
}

// runHactlDirErr executes a hactl command against a specific instance directory
// and returns both output and error.
func runHactlDirErr(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HACTL_DIR", dir)

	var buf bytes.Buffer
	err := cmd.RunWithOutput(append([]string{"hactl"}, args...), &buf)
	return buf.String(), err
}

// runHactlJSON executes a hactl command with --json and unmarshals the output.
func runHactlJSON[T any](t *testing.T, args ...string) T {
	t.Helper()
	fullArgs := make([]string, len(args), len(args)+1)
	copy(fullArgs, args)
	fullArgs = append(fullArgs, "--json")
	out := runHactl(t, fullArgs...)
	var result T
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("hactl %v --json returned invalid JSON: %v\noutput: %s", args, err, out)
	}
	return result
}

// runHactlLines executes a hactl command and returns output split into trimmed non-empty lines.
func runHactlLines(t *testing.T, args ...string) []string {
	t.Helper()
	out := runHactl(t, args...)
	raw := strings.Split(out, "\n")
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		trimmed := strings.TrimRight(l, " \t\r")
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// stripTokenHeader removes the leading "[~N tok]\n" header line if present.
// Use in tests that assert exact scalar output (e.g. tpl eval, version).
func stripTokenHeader(s string) string {
	if idx := strings.Index(s, "\n"); idx >= 0 && strings.HasPrefix(s, "[~") {
		return s[idx+1:]
	}
	return s
}

// assertContains fails if s does not contain substr.
func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("output missing %q:\n%s", substr, s)
	}
}

// assertNotContains fails if s contains substr.
func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("output unexpectedly contains %q:\n%s", substr, s)
	}
}

// Regex patterns for sanitizing dynamic values in golden-file comparisons.
var (
	reTimestamp  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2})?(\.\d+)?([+-]\d{2}:?\d{2}|Z)?`)
	reShortTime  = regexp.MustCompile(`\b\d{2}:\d{2}\b`)
	reShortDate  = regexp.MustCompile(`\b\d{2}-\d{2} \d{2}:\d{2}\b`)
	reSunState   = regexp.MustCompile(`above_horizon|below_horizon`)
	reHAVersion  = regexp.MustCompile(`HA \d{4}\.\d+\.\d+(\.\w+)?`)
	rePort       = regexp.MustCompile(`localhost:\d{4,5}`)
	reTempPath   = regexp.MustCompile(`(?:[A-Z]:[^\s]*?|/[^\s]*?)hatest-\d+`)
	reErrors     = regexp.MustCompile(`errors=\d+`)
	reHAState    = regexp.MustCompile(`state=(RUNNING|NOT_RUNNING|STARTING|STOPPING)`)
	reCacheSize   = regexp.MustCompile(`\d+(\.\d+)? (KB|MB|GB|B)\b`)
	reTokenHeader = regexp.MustCompile(`\[~\d+ tok\]`)
)

// sanitizeGolden replaces dynamic values with stable placeholders for golden-file comparison.
func sanitizeGolden(s string) string {
	s = reTokenHeader.ReplaceAllString(s, "[~XXX tok]")
	s = reTimestamp.ReplaceAllString(s, "<TIMESTAMP>")
	s = reShortDate.ReplaceAllString(s, "<DATE_TIME>")
	s = reShortTime.ReplaceAllString(s, "<TIME>")
	s = reHAVersion.ReplaceAllString(s, "HA <VERSION>")
	s = rePort.ReplaceAllString(s, "localhost:<PORT>")
	s = reTempPath.ReplaceAllString(s, "<TMPDIR>/hatest-<ID>")
	s = strings.ReplaceAll(s, "hatest-<ID>\\", "hatest-<ID>/")
	s = reErrors.ReplaceAllString(s, "errors=<N>")
	s = reHAState.ReplaceAllString(s, "state=<STATE>")
	s = reCacheSize.ReplaceAllString(s, "<SIZE>")
	s = reSunState.ReplaceAllString(s, "<SUN_STATE>")
	return s
}

func init() {
	// Suppress slog output during tests
	os.Setenv("HACTL_LOG_LEVEL", "error") //nolint:errcheck // test setup
}
