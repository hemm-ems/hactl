//go:build integration

package integration

import (
	"strings"
	"testing"
)

// Error-path tests verify that hactl produces descriptive errors
// instead of panics or empty output when given bad input.

func TestErrorTplEvalInvalidJinja(t *testing.T) {
	_, err := runHactlErr(t, "tpl", "eval", "{{ invalid jinja ( }}")
	if err == nil {
		t.Error("tpl eval with invalid Jinja expected error")
	}
}

func TestErrorTplEvalNoTemplate(t *testing.T) {
	_, err := runHactlErr(t, "tpl", "eval")
	if err == nil {
		t.Error("tpl eval with no template expected error")
	}
}

func TestErrorTplEvalMissingFile(t *testing.T) {
	_, err := runHactlErr(t, "tpl", "eval", "-f", "/nonexistent/template.j2")
	if err == nil {
		t.Error("tpl eval with missing file expected error")
	}
}

func TestErrorEntShowNoArgs(t *testing.T) {
	_, err := runHactlErr(t, "ent", "show")
	if err == nil {
		t.Error("ent show with no args expected error")
	}
}

func TestErrorEntHistNoArgs(t *testing.T) {
	_, err := runHactlErr(t, "ent", "hist")
	if err == nil {
		t.Error("ent hist with no args expected error")
	}
}

func TestErrorAutoShowNoArgs(t *testing.T) {
	_, err := runHactlErr(t, "auto", "show")
	if err == nil {
		t.Error("auto show with no args expected error")
	}
}

func TestErrorTraceShowNoArgs(t *testing.T) {
	_, err := runHactlErr(t, "trace", "show")
	if err == nil {
		t.Error("trace show with no args expected error")
	}
}

func TestErrorInvalidSinceDuration(t *testing.T) {
	_, err := runHactlErr(t, "changes", "--since", "invalid_duration")
	if err == nil {
		t.Error("changes with invalid --since expected error")
	}
}

func TestErrorHelpOutputs(t *testing.T) {
	// All top-level commands should show help without error
	cmds := []string{"auto", "ent", "trace", "cache", "cc"}
	for _, c := range cmds {
		t.Run(c, func(t *testing.T) {
			out := runHactl(t, c, "--help")
			assertNotContains(t, out, "panic")
			if !strings.Contains(out, "Usage:") && !strings.Contains(out, "Available Commands:") {
				t.Errorf("%s --help missing usage info: %s", c, out)
			}
		})
	}
}

func TestErrorRootHelp(t *testing.T) {
	out := runHactl(t, "--help")
	assertContains(t, out, "hactl")
	assertContains(t, out, "Available Commands:")
}
