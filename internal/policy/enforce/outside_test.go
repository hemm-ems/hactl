package enforce_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// TestWrittenOutsidePlanIsDeniedAuditedAndListed (D-77, A-7): each file a
// confirmed write changed outside its checked plan is a denied `edit
// file:<path>` with the fail-closed rule, listed in Written, and one audit line
// at the command layer; none is no refusal.
func TestWrittenOutsidePlanIsDeniedAuditedAndListed(t *testing.T) {
	pol, dir := loadPolicy(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n")
	sess, err := enforce.New(enforce.Options{Policy: pol, InstanceDir: dir, Command: "hactl ref replace a b"})
	if err != nil {
		t.Fatal(err)
	}
	if ref := sess.WrittenOutsidePlan("rename", nil); ref != nil {
		t.Fatalf("nothing outside the plan, yet refused: %v", ref)
	}
	ref := sess.WrittenOutsidePlan("rename", []string{"packages/a.yaml", "b.yaml"})
	if ref == nil || ref.Layer != enforce.LayerCommand || ref.DryRun || ref.Checked != 2 {
		t.Fatalf("refusal = %+v", ref)
	}
	if !slices.Equal(ref.Written, []string{"edit file:packages/a.yaml", "edit file:b.yaml"}) {
		t.Errorf("written = %q", ref.Written)
	}
	if ref.Error() != "policy_denied: 2 of 2 resources denied (profile a)" {
		t.Errorf("stderr line = %q", ref.Error())
	}
	f, err := os.Open(filepath.Clean(filepath.Join(dir, "cache", enforce.AuditFileName)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var lines []enforce.AuditLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l enforce.AuditLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	if len(lines) != 2 || lines[0].Resource != "file:packages/a.yaml" || lines[0].Allowed || lines[0].Rule != enforce.RuleWrittenOutsidePlan || lines[0].Layer != enforce.LayerCommand {
		t.Errorf("audit = %+v", lines)
	}
}
