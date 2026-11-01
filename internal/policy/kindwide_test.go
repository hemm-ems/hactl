package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestDecideKindWide holds D-75's kind-wide check: create on every id of a
// kind is allowed only if no deny for the verb names any id of the kind and an
// allow covers them all or the default allows; a deny on a part of every id
// makes it partial, hence denied.
func TestDecideKindWide(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		kind    Kind
		sub     string
		verb    Verb
		allowed bool
		rule    string
		line    int
		partial bool
	}{
		{"a literal deny on one id refuses every create", `version: 1
active: a
profiles:
  a:
    deny:
      "area:secret": [create]
`, KindArea, "", Create, false, `deny "area:secret" [create]`, 6, false},
		{"a deny for another verb does not decide", `version: 1
active: a
profiles:
  a:
    deny:
      "area:secret": [delete]
`, KindArea, "", Create, true, RuleDefaultAllow, 0, false},
		{"a deny on another kind does not decide", `version: 1
active: a
profiles:
  a:
    deny:
      "floor:*": [create]
`, KindArea, "", Create, true, RuleDefaultAllow, 0, false},
		{"the bare * deny decides", `version: 1
active: a
profiles:
  a:
    deny:
      "*": [create]
`, KindLabel, "", Create, false, `deny "*" [create]`, 6, false},
		{"default deny with an allow on every id grants", `version: 1
active: a
profiles:
  a:
    default: deny
    allow:
      "area:*": [create]
`, KindArea, "", Create, true, `allow "area:*" [create]`, 7, false},
		{"default deny with an allow on some ids does not grant", `version: 1
active: a
profiles:
  a:
    default: deny
    allow:
      "area:k*": [create]
`, KindArea, "", Create, false, RuleDefaultDeny, 0, false},
		{"a helper allow must span both id segments", `version: 1
active: a
profiles:
  a:
    default: deny
    allow:
      "helper:*/*": [create]
`, KindHelper, "", Create, true, `allow "helper:*/*" [create]`, 7, false},
		{"a descendant edit-deny makes editing every config entry partial", `version: 1
active: a
profiles:
  a:
    deny:
      "config_entry:*/options": [edit]
`, KindConfigEntry, "", Edit, false, `deny "config_entry:*/options" [edit]`, 6, true},
		{"a descendant deny whose verb does not apply there makes nothing partial (D-110)", `version: 1
active: a
profiles:
  a:
    deny:
      "config_entry:*/options": [create]
`, KindConfigEntry, "", Create, true, RuleDefaultAllow, 0, false},
		{"a sub-scoped kind-wide check is covered by a deny on that sub", `version: 1
active: a
profiles:
  a:
    deny:
      "config_entry:abc/options": [edit]
`, KindConfigEntry, "options", Edit, false, `deny "config_entry:abc/options" [edit]`, 6, false},
		{"a deny on a sibling sub does not touch the checked sub", `version: 1
active: a
profiles:
  a:
    deny:
      "config_entry:abc/flow": [edit]
`, KindConfigEntry, "options", Edit, true, RuleDefaultAllow, 0, false},
		{"an allow narrowed by where never covers every id", `version: 1
active: a
profiles:
  a:
    default: deny
    allow:
      "entity:*": [edit]
    where:
      "entity:*": { area: kitchen }
`, KindEntity, "registry/labels", Edit, false, RuleDefaultDeny, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustLoad(t, tc.body)
			v := p.DecideKindWide(tc.kind, tc.sub, tc.verb)
			if v.Allowed != tc.allowed || v.Rule != tc.rule || v.Line != tc.line || v.Partial != tc.partial {
				t.Errorf("DecideKindWide(%s, %q, %s) = allowed %v rule %q line %d partial %v; want %v %q %d %v",
					tc.kind, tc.sub, tc.verb, v.Allowed, v.Rule, v.Line, v.Partial, tc.allowed, tc.rule, tc.line, tc.partial)
			}
			if v.Level != Level || v.Notes == nil || v.PartialDenies == nil {
				t.Errorf("verdict shape: level %q notes %v partial_denies %v", v.Level, v.Notes, v.PartialDenies)
			}
		})
	}
}

// TestDecideKindWideWithoutAProfileAllows: no file and the implicit allow-all
// decide every kind-wide check as they decide every other (D-58, D-61).
func TestDecideKindWideWithoutAProfileAllows(t *testing.T) {
	none, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if v := none.DecideKindWide(KindArea, "", Create); !v.Allowed || v.Rule != RuleNoFile {
		t.Errorf("no file: %+v", v)
	}
	implicit := mustLoad(t, "version: 1\nprofiles:\n  agent:\n    default: deny\n")
	if v := implicit.DecideKindWide(KindArea, "", Create); !v.Allowed || v.Rule != RuleImplicit {
		t.Errorf("implicit allow-all: %+v", v)
	}
	if none.InEffect() || implicit.InEffect() {
		t.Error("InEffect true without a profile in effect")
	}
}

// TestPolicyHashIsTheFileContent: the hash every verified fact is tagged with
// (D-72, R-7) is the SHA-256 of the bytes loaded — another byte, another hash
// — and "" without a file; a what-if view carries its file's hash.
func TestPolicyHashIsTheFileContent(t *testing.T) {
	body := "version: 1\nactive: a\nprofiles:\n  a: {}\n  b: {}\n"
	dir := writePolicy(t, body)
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	if want := hex.EncodeToString(sum[:]); p.Hash() != want {
		t.Errorf("Hash = %q, want %q", p.Hash(), want)
	}
	if !p.InEffect() {
		t.Error("InEffect false with active: a")
	}
	as, err := p.As("b")
	if err != nil {
		t.Fatal(err)
	}
	if as.Hash() != p.Hash() {
		t.Errorf("what-if hash %q, want the file's %q", as.Hash(), p.Hash())
	}
	if werr := os.WriteFile(filepath.Join(dir, FileName), []byte(body+"# x\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	q, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if q.Hash() == p.Hash() {
		t.Error("an edited file kept the same hash")
	}
	none, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if none.Hash() != "" {
		t.Errorf("no file: Hash = %q, want empty", none.Hash())
	}
}
