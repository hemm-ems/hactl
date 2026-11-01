package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// typedFiles are the candidate files the typed write commands read, written
// into each rig's instance directory under these names.
var typedFiles = map[string]string{
	"auto_edit.yaml":     "id: heating\nalias: Heating (eco)\ntriggers: []\nactions: []\n",
	"auto_rename.yaml":   "id: heating_v2\nalias: Heating\ntriggers: []\nactions: []\n",
	"auto_noid.yaml":     "alias: Heating\ntriggers: []\nactions: []\n",
	"auto_new.yaml":      "id: hall\nalias: Hall\ntriggers: []\nactions: []\n",
	"script_edit.yaml":   "wakeup:\n  alias: Wake Up (slow)\n  sequence:\n  - delay: 00:00:02\n",
	"script_new.yaml":    "morning:\n  alias: Morning\n  sequence:\n  - delay: 00:00:01\n",
	"tpl_new.yaml":       "unique_id: hall_temp\nname: Hall Temp\nstate: '{{ 20 }}'\n",
	"tpl_block.yaml":     "sensor:\n- unique_id: a_temp\n  name: A\n  state: '{{ 1 }}'\n- unique_id: b_temp\n  name: B\n  state: '{{ 2 }}'\nbinary_sensor:\n- unique_id: c_on\n  name: C\n  state: '{{ true }}'\n",
	"tpl_nouid.yaml":     "sensor:\n- unique_id: a_temp\n  name: A\n  state: '{{ 1 }}'\n- name: B\n  state: '{{ 2 }}'\n",
	"helper_new.yaml":    "party_mode:\n  name: Party Mode\n",
	"backup_rename.yaml": "id: heating_old\nalias: Heating\ntriggers: []\nactions: []\n",
}

// typedCommand is one invocation of a typed write command, without --confirm;
// a `{f}` argument is replaced by the named file's path in the rig.
type typedCommand struct {
	name string
	args []string
}

// typedCommands are the typed write commands this family wires (manual,
// Enforcement: "What each write command checks"), once each in their main
// shapes.
var typedCommands = []typedCommand{
	{"auto apply", []string{"auto", "apply", "heating", "-f", "{auto_edit.yaml}"}},
	{"auto apply rename", []string{"auto", "apply", "heating", "-f", "{auto_rename.yaml}"}},
	{"auto apply without id", []string{"auto", "apply", "heating", "-f", "{auto_noid.yaml}"}},
	{"auto create", []string{"auto", "create", "-f", "{auto_new.yaml}"}},
	{"auto delete", []string{"auto", "delete", "heating"}},
	{"auto delete without config id", []string{"auto", "delete", "legacy"}},
	{"auto rollback", []string{"auto", "rollback", "heating"}},
	{"rollback", []string{"rollback", "heating"}},
	{"script apply", []string{"script", "apply", "wakeup", "-f", "{script_edit.yaml}"}},
	{"script create", []string{"script", "create", "-f", "{script_new.yaml}"}},
	{"script delete", []string{"script", "delete", "wakeup"}},
	{"tpl create", []string{"tpl", "create", "-f", "{tpl_new.yaml}"}},
	{"tpl create block", []string{"tpl", "create", "-f", "{tpl_block.yaml}"}},
	{"tpl create without unique_id", []string{"tpl", "create", "-f", "{tpl_nouid.yaml}"}},
	{"tpl delete", []string{"tpl", "delete", "room_temp"}},
	{"helper create", []string{"helper", "create", "input_boolean", "-f", "{helper_new.yaml}"}},
	{"helper delete", []string{"helper", "delete", "guest_mode"}},
}

// argsIn resolves the command's file arguments in rig r, plus extra.
func (c typedCommand) argsIn(t *testing.T, r *typedRig, extra ...string) []string {
	t.Helper()
	out := make([]string, 0, len(c.args)+len(extra))
	for _, a := range c.args {
		if name, ok := strings.CutPrefix(a, "{"); ok {
			name = strings.TrimSuffix(name, "}")
			a = r.write(t, name, typedFiles[name])
		}
		out = append(out, a)
	}
	return append(out, extra...)
}

var typedStampRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}(\.\d+)?_`)

// typedTranscript runs c in a fresh rig — the preview, or with confirm the
// confirmed run — and renders what it printed, its error, and every request
// it sent, with the rig's paths and stamps normalised.
func typedTranscript(t *testing.T, c typedCommand, policyBody string, confirm bool) (string, *typedRig) {
	t.Helper()
	t.Setenv("HACTL_MANUAL_MODE", "off")
	r := newTypedRig(t)
	if policyBody != "" {
		writePolicyFile(t, r.ts.dir, policyBody)
	}
	var extra []string
	if confirm {
		extra = []string{"--confirm"}
	}
	args := c.argsIn(t, r, extra...)
	r.requests()
	r.sentBodies()
	r.resetWS()
	out, err := runPolicyCLI(t, r.ts.dir, args...)
	var b strings.Builder
	b.WriteString("== " + c.name)
	if confirm {
		b.WriteString(" --confirm")
	}
	b.WriteString("\n-- stdout\n" + out)
	errText := "<nil>"
	if err != nil {
		errText = err.Error()
	}
	b.WriteString("-- error\n" + errText + "\n-- http\n")
	for _, l := range r.requests() {
		b.WriteString(l + "\n")
	}
	b.WriteString("-- ws\n")
	for _, l := range r.wsCounts() {
		if !strings.HasPrefix(l, "auth") {
			b.WriteString(l + "\n")
		}
	}
	s := strings.ReplaceAll(b.String(), r.ts.dir, "<dir>")
	s = strings.ReplaceAll(s, r.ts.srv.URL, "<url>")
	return typedStampRE.ReplaceAllString(s, "<stamp>_"), r
}

const typedNoProfileGolden = "testdata/policy/typed_no_profile.golden"

// assertSameBodies holds two no-profile runs to the same request bodies — what
// the golden's method-and-path lines cannot see (D-58).
func assertSameBodies(t *testing.T, what string, noFile, implicit []string) {
	t.Helper()
	a, b := strings.Join(noFile, "\n--\n"), strings.Join(implicit, "\n--\n")
	if a != b {
		t.Errorf("%s: the request bodies differ between no file and the implicit allow-all:\n--- no file\n%s\n--- implicit\n%s", what, a, b)
	}
}

// TestTypedWritesWithoutAProfileAreUnchanged (D-58): with no policy file and
// with a file that puts no profile in effect, every typed write command —
// preview and confirmed run — prints, fails and sends exactly what it did
// before the policy existed, byte for byte, and no audit log appears. The
// golden was recorded from the commands as they were before stage 2b wired
// them (HACTL_UPDATE_GOLDEN=1 rewrites it; never do that to make this pass).
// One line was changed by hand, on purpose: the confirmed `helper delete` now
// names its domain with or without a policy (D-123).
func TestTypedWritesWithoutAProfileAreUnchanged(t *testing.T) {
	var got strings.Builder
	for _, c := range typedCommands {
		for _, confirm := range []bool{false, true} {
			noFile, r := typedTranscript(t, c, "", confirm)
			got.WriteString(noFile)
			if _, err := os.Stat(filepath.Join(r.ts.dir, "cache", enforce.AuditFileName)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s (confirm %v): an audit log was written without a policy", c.name, confirm)
			}
			noFileBodies := r.sentBodies()
			implicit, r := typedTranscript(t, c, policyImplicitAllow, confirm)
			assertSameBodies(t, fmt.Sprintf("%s (confirm %v)", c.name, confirm), noFileBodies, r.sentBodies())
			if implicit != noFile {
				t.Errorf("%s (confirm %v): the implicit allow-all differs from no file:\n--- no file\n%s\n--- implicit\n%s", c.name, confirm, noFile, implicit)
			}
			if _, err := os.Stat(filepath.Join(r.ts.dir, "cache", enforce.AuditFileName)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s (confirm %v): an audit log was written under the implicit allow-all", c.name, confirm)
			}
		}
	}
	if os.Getenv("HACTL_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(typedNoProfileGolden), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(typedNoProfileGolden, []byte(got.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(typedNoProfileGolden)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		gl, wl := strings.Split(got.String(), "\n"), strings.Split(string(want), "\n")
		for i := range max(len(gl), len(wl)) {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("no-profile transcript differs from the recorded one at line %d:\n got: %q\nwant: %q", i+1, g, w)
			}
		}
	}
}
