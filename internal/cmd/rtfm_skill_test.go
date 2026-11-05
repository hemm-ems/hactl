package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/docs"
)

// D-165: `rtfm --skill policy-setup` prints the skill shipped in the binary,
// byte for byte, uncapped — the same file docs/skills/policy-setup.md holds.
func TestRTFMSkillPrintsTheShippedSkillVerbatim(t *testing.T) {
	want, err := os.ReadFile("../../docs/skills/policy-setup.md")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "rtfm", "--skill", "policy-setup"}, &buf); err != nil {
		t.Fatalf("rtfm --skill policy-setup: %v", err)
	}
	if buf.String() != string(want) {
		t.Errorf("rtfm --skill policy-setup printed %d bytes, the skill file has %d — not verbatim (a cap or a wrapper?)", buf.Len(), len(want))
	}
	// The skill drafts and never names policy.yaml as something to write.
	for _, must := range []string{"policy.draft.yaml", "hactl policy lint --file policy.draft.yaml", "hactl policy activate"} {
		if !strings.Contains(buf.String(), must) {
			t.Errorf("the skill does not mention %q", must)
		}
	}
}

func TestRTFMSkillRefusesAnUnknownNameWithTheList(t *testing.T) {
	var buf bytes.Buffer
	err := RunWithOutput([]string{"hactl", "rtfm", "--skill", "policy-setupp"}, &buf)
	if err == nil || !strings.Contains(err.Error(), `unknown skill "policy-setupp"`) || !strings.Contains(err.Error(), "policy-setup") {
		t.Fatalf("an unknown skill: err = %v, want refused with the list", err)
	}
	if buf.Len() != 0 {
		t.Errorf("an unknown skill printed %q", buf.String())
	}
	for _, bad := range []string{"", "../manual", "skills/policy-setup", "policy-setup.md"} {
		if _, ok := docs.Skill(bad); ok {
			t.Errorf("docs.Skill(%q) found a skill", bad)
		}
	}
}

func TestRTFMSkillStandsAlone(t *testing.T) {
	for _, other := range [][]string{{"--core"}, {"--family", "policy"}, {"--reference", "policy"}, {"--families"}} {
		var buf bytes.Buffer
		args := append([]string{"hactl", "rtfm", "--skill", "policy-setup"}, other...)
		if err := RunWithOutput(args, &buf); err == nil {
			t.Errorf("rtfm --skill with %v was accepted", other)
		}
	}
}
