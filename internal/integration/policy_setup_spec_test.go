//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// ============================================================================
// W-4: an agent drafts, only the owner activates. Blind spec tests written
// from the contract only — docs/manual.md "Setting up an agent: draft, review,
// activate", docs/skills/policy-setup.md, D-148/D-149, SPEC-policy-usp.md W-4
// and M-5. No implementation was read. Every claim that hactl wrote or did
// not write a file is checked on the file itself; every count `policy review`
// gives is checked against Home Assistant's own answer (H-12).
// ============================================================================

// setupLivePolicy is the owner's policy.yaml in the draft tests: it allows
// everything but triggering an automation.
const setupLivePolicy = `version: 1
active: pg_live
profiles:
  pg_live:
    deny:
      "service:automation.trigger": [control]
`

// setupDraftPolicy is the agent's draft: the opposite verdicts — automation
// triggers allowed, creating an area not — and one misspelt deny (a lint
// no-match) so that "lints the draft" is observable.
const setupDraftPolicy = `version: 1
active: pg_draft
profiles:
  pg_draft:
    default: deny
    allow:
      "entity:*": [read]
      "entity:automation.*": [control]
      "service:automation.trigger": [control]
    deny:
      "entity:lokc.*": [write]
`

const setupDraftName = "policy.draft.yaml"

// reDraftFileLine is the answer's file line for a draft (manual: `file: …
// (draft, not in effect)`).
var reDraftFileLine = regexp.MustCompile(`(?m)^file: .*` + regexp.QuoteMeta(setupDraftName) + `.*\(draft, not in effect\)\s*$`)

// draftDir is an instance directory for the shared HA holding setupLivePolicy
// as policy.yaml and draft as policy.draft.yaml.
func draftDir(t *testing.T, draft string) string {
	t.Helper()
	dir := setupInstanceDir(t, ha, setupLivePolicy, false)
	writeFileT(t, filepath.Join(dir, setupDraftName), draft)
	return dir
}

// skillPath is the shipped skill's source in this repository.
func skillPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "docs", "skills", "policy-setup.md"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------------------------------------------------------------------------
// The skill
// ---------------------------------------------------------------------------

// TestPolicySetupSpecRtfmSkillPrintsTheSkill — manual: "`hactl rtfm --skill
// policy-setup` prints the procedure for an agent"; W-4.1: "Skill shipped in
// the binary: `hactl rtfm --skill policy-setup`, the same delivery as the
// manual … It never names `policy.yaml` as a target." rtfm is in the D-60 set,
// so a new agent's directory (HACTL_POLICY=required, D-136) prints it too, and
// "the same delivery as the manual" includes MCP.
func TestPolicySetupSpecRtfmSkillPrintsTheSkill(t *testing.T) {
	want := strings.TrimSpace(readFileT(t, skillPath(t)))
	home := setupHome(t)

	plain := setupInstanceDir(t, ha, "", false)
	required := setupInstanceDir(t, ha, "", true)
	for name, dir := range map[string]string{"no policy": plain, "HACTL_POLICY=required, no policy.yaml": required} {
		t.Run(name, func(t *testing.T) {
			res := runSetupBin(t, home, dir, nil, "--dir", dir, "rtfm", "--skill", "policy-setup")
			if res.Code != 0 {
				t.Fatalf("rtfm --skill policy-setup: %s", res)
			}
			if got := strings.TrimSpace(res.Stdout); got != want {
				t.Errorf("rtfm --skill policy-setup does not print docs/skills/policy-setup.md verbatim (rtfm prints one document verbatim)\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}

	t.Run("over MCP", func(t *testing.T) {
		c := startMCPServer(t, home, plain)
		text, isErr := c.call(t, "rtfm --skill policy-setup")
		if isErr || !strings.Contains(text, "# Skill: draft a permission policy for yourself") {
			t.Errorf("rtfm --skill policy-setup over MCP: isError=%v, text lacks the skill:\n%.600s", isErr, text)
		}
	})

	// The skill itself: never names policy.yaml as something to write.
	assertSkillNeverWritesPolicyYAML(t, want)
}

var (
	// rePolicyYAML is `policy.yaml` that is not `policy.draft.yaml` / `.prev`.
	rePolicyYAML = regexp.MustCompile(`(^|[^.\w-])policy\.yaml(\b|$)`)
	reProhibit   = regexp.MustCompile(`(?i)\b(never|not|don't|do not)\b`)
)

// assertSkillNeverWritesPolicyYAML — W-4.1 "It never names `policy.yaml` as a
// target"; manual step 2 "The agent never writes `policy.yaml`". Every prose
// line naming policy.yaml must be a prohibition, no command in a code block
// may name it at all, and the draft it does name is policy.draft.yaml, linted
// with --file.
func assertSkillNeverWritesPolicyYAML(t *testing.T, skill string) {
	t.Helper()
	inCode := false
	i := 0
	for line := range strings.SplitSeq(skill, "\n") {
		i++
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if !rePolicyYAML.MatchString(line) {
			continue
		}
		if inCode {
			t.Errorf("skill line %d, inside a code block, names policy.yaml — a command the agent would run on the owner's file: %q", i, line)
			continue
		}
		if !reProhibit.MatchString(line) {
			t.Errorf("skill line %d names policy.yaml without forbidding it — the skill must never name it as a target: %q", i, line)
		}
	}
	for _, must := range []string{
		"policy.draft.yaml",
		"default: deny",
		"hactl policy lint --file policy.draft.yaml",
	} {
		if !strings.Contains(skill, must) {
			t.Errorf("skill lacks %q (W-4.1: write policy.draft.yaml with default: deny, lint it with --file)", must)
		}
	}
}

// TestPolicySetupSpecSkillExampleDraftIsAValidPolicy — the skill tells the
// agent "Write `policy.draft.yaml` … Start from deny and allow what the owner
// said" and shows the draft; the shipped example must itself be a valid policy
// file (`policy lint --file` reports no `broken` finding), or the procedure
// starts the agent off with a draft the owner cannot activate.
func TestPolicySetupSpecSkillExampleDraftIsAValidPolicy(t *testing.T) {
	skill := readFileT(t, skillPath(t))
	_, body, found := strings.Cut(skill, "```yaml\n")
	if !found {
		t.Fatal("the skill has no ```yaml example draft")
	}
	example, _, closed := strings.Cut(body, "```")
	if !closed {
		t.Fatal("the skill's yaml block is not closed")
	}

	dir := setupInstanceDir(t, ha, "", false)
	writeFileT(t, filepath.Join(dir, setupDraftName), example)
	res := runSetupBin(t, setupHome(t), dir, nil, "--dir", dir, "policy", "lint", "--file", setupDraftName, "--json")
	var doc struct {
		Valid    bool `json:"valid"`
		Findings []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatalf("policy lint --file --json is not JSON: %v\n%s", err, res)
	}
	if !doc.Valid {
		t.Errorf("the skill's example draft is not a valid policy file: %s", res)
	}
	for _, f := range doc.Findings {
		if f.Code == "broken" {
			t.Errorf("the skill's example draft is broken: %s", f.Message)
		}
	}
}

// ---------------------------------------------------------------------------
// --file: a draft is evaluated, never in effect
// ---------------------------------------------------------------------------

// TestPolicySetupSpecFileFlagReadsADraftNotInEffect — manual: "`policy show`,
// `policy check` and `policy lint` take `--file <path>` to read a draft
// instead of `policy.yaml`; the answer says so (`file: … (draft, not in
// effect)`)." W-4.2: "evaluate a draft without it being in effect." Every
// verdict is asked both ways and the two files give opposite answers, so an
// answer can only come from the file it names. Then a real command proves the
// draft is not in effect: it still obeys policy.yaml.
func TestPolicySetupSpecFileFlagReadsADraftNotInEffect(t *testing.T) {
	d := &draftRig{home: setupHome(t), dir: draftDir(t, setupDraftPolicy)}
	d.draftAbs = filepath.Join(d.dir, setupDraftName)
	before := treeSnapshot(t, d.dir, skipCache)

	t.Run("show", d.checkShow)
	t.Run("check", d.checkCheck)
	t.Run("lint", d.checkLint)
	t.Run("a command obeys policy.yaml, not the draft", d.checkNotInEffect)
	t.Run("a broken draft breaks nothing but its own answer", d.checkBrokenDraft)

	if diff := diffSnapshots(before, treeSnapshot(t, d.dir, skipCache)); len(diff) > 0 {
		t.Errorf("show/check/lint --file changed the instance directory: %v", diff)
	}
}

// draftRig is one instance directory holding setupLivePolicy as policy.yaml
// and setupDraftPolicy as policy.draft.yaml.
type draftRig struct {
	home, dir, draftAbs string
}

func (d *draftRig) run(t *testing.T, args ...string) procResult {
	t.Helper()
	return runSetupBin(t, d.home, d.dir, nil, append([]string{"--dir", d.dir}, args...)...)
}

func (d *draftRig) checkShow(t *testing.T) {
	res := d.run(t, "policy", "show", "--file", setupDraftName)
	if res.Code != 0 || !reDraftFileLine.MatchString(res.Stdout) {
		t.Errorf("policy show --file: want exit 0 and a `file: …%s (draft, not in effect)` line\n%s", setupDraftName, res)
	}
	var doc struct {
		File    string `json:"file"`
		Profile string `json:"profile"`
	}
	jres := d.run(t, "policy", "show", "--file", setupDraftName, "--json")
	if err := json.Unmarshal([]byte(jres.Stdout), &doc); err != nil {
		t.Fatalf("policy show --file --json: %v\n%s", err, jres)
	}
	if doc.Profile != "pg_draft" || doc.File != d.draftAbs {
		t.Errorf("policy show --file --json: file %q profile %q, want file %q profile pg_draft", doc.File, doc.Profile, d.draftAbs)
	}
	live := d.run(t, "policy", "show", "--json")
	if err := json.Unmarshal([]byte(live.Stdout), &doc); err != nil {
		t.Fatalf("policy show --json: %v\n%s", err, live)
	}
	if doc.Profile != "pg_live" {
		t.Errorf("policy show without --file: profile %q, want pg_live — the draft must not be in effect", doc.Profile)
	}
	if plain := d.run(t, "policy", "show"); strings.Contains(plain.Stdout, "(draft, not in effect)") {
		t.Errorf("policy show without --file calls policy.yaml a draft:\n%s", plain.Stdout)
	}
}

func (d *draftRig) checkCheck(t *testing.T) {
	for _, tc := range []struct {
		res, verb         string
		draftOK, liveOK   bool
		draftWhy, liveWhy string
	}{
		{"service:automation.trigger", "control", true, false, "the draft allows it", `policy.yaml denies it`},
		{"area:pg_setup_spec", "create", false, true, "the draft's default: deny", "policy.yaml allows it"},
	} {
		dr := d.run(t, "policy", "check", "--file", setupDraftName, tc.res, tc.verb)
		if got := dr.Code == 0; got != tc.draftOK {
			t.Errorf("policy check --file %s %s %s: exit %d, want allowed=%v (%s)\n%s", setupDraftName, tc.res, tc.verb, dr.Code, tc.draftOK, tc.draftWhy, dr)
		}
		if !strings.Contains(dr.Stdout+dr.Stderr, "(draft, not in effect)") {
			t.Errorf("policy check --file %s %s: the answer does not say it read a draft `(draft, not in effect)`\n%s", tc.res, tc.verb, dr)
		}
		l := d.run(t, "policy", "check", tc.res, tc.verb)
		if got := l.Code == 0; got != tc.liveOK {
			t.Errorf("policy check %s %s (no --file): exit %d, want allowed=%v (%s) — the draft must not be in effect\n%s", tc.res, tc.verb, l.Code, tc.liveOK, tc.liveWhy, l)
		}
	}
	var doc struct {
		Allowed bool   `json:"allowed"`
		Profile string `json:"profile"`
	}
	j := d.run(t, "policy", "check", "--file", setupDraftName, "service:automation.trigger", "control", "--json")
	if err := json.Unmarshal([]byte(j.Stdout), &doc); err != nil {
		t.Fatalf("policy check --file --json: %v\n%s", err, j)
	}
	if !doc.Allowed || doc.Profile != "pg_draft" {
		t.Errorf("policy check --file --json: %+v, want allowed under pg_draft", doc)
	}
}

func (d *draftRig) checkLint(t *testing.T) {
	res := d.run(t, "policy", "lint", "--file", setupDraftName, "--json")
	var doc struct {
		File     string        `json:"file"`
		Findings []lintFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatalf("policy lint --file --json: %v\n%s", err, res)
	}
	wantLine := policyLineOf(t, setupDraftPolicy, `"entity:lokc.*"`)
	if !slices.ContainsFunc(doc.Findings, func(f lintFinding) bool {
		return f.Code == "no-match" && f.Rule == `deny "entity:lokc.*" [write]` && f.Line == wantLine
	}) {
		t.Errorf("policy lint --file: no `no-match deny \"entity:lokc.*\" [write]` on the draft's line %d\n%s", wantLine, res)
	}
	if res.Code != 1 {
		t.Errorf("policy lint --file with a finding: exit %d, want 1", res.Code)
	}
	if doc.File != d.draftAbs {
		t.Errorf("policy lint --file --json: file %q, want %q", doc.File, d.draftAbs)
	}
	if text := d.run(t, "policy", "lint", "--file", setupDraftName); !reDraftFileLine.MatchString(text.Stdout) {
		t.Errorf("policy lint --file: no `file: …%s (draft, not in effect)` line\n%s", setupDraftName, text)
	}
	if live := d.run(t, "policy", "lint"); strings.Contains(live.Stdout, "lokc") {
		t.Errorf("policy lint without --file linted the draft:\n%s", live)
	}
}

func (d *draftRig) checkNotInEffect(t *testing.T) {
	auto := "automation." + automationIDsFromHA(t)["climate_schedule"]
	svc := d.run(t, "svc", "call", "automation.trigger", "-d", `{"entity_id":"`+auto+`"}`)
	if svc.Code != 1 || !strings.Contains(svc.Stdout, `deny "service:automation.trigger" [control]`) ||
		!strings.Contains(svc.Stdout, "profile: pg_live") {
		t.Errorf("svc call automation.trigger: want the refusal by policy.yaml's pg_live (the draft allows it, but is not in effect)\n%s", svc)
	}
	area := d.run(t, "area", "create", "PG Setup Spec Draft")
	if area.Code != 0 || !allowedPreviewLine("pg_live").MatchString(area.Stdout) {
		t.Errorf("area create (dry run): want allowed under pg_live (the draft denies it, but is not in effect)\n%s", area)
	}
}

func (d *draftRig) checkBrokenDraft(t *testing.T) {
	broken := "version: 1\nactive: pg_broken\nprofiles:\n  pg_broken:\n    deny:\n      \"entity:lock.*\": [wirte]\n"
	path := filepath.Join(d.dir, "broken.draft.yaml")
	writeFileT(t, path, broken)
	defer func() { _ = os.Remove(path) }()
	lint := d.run(t, "policy", "lint", "--file", "broken.draft.yaml")
	if lint.Code != 1 || !strings.Contains(lint.Stdout, "policy_broken: "+path+":") {
		t.Errorf("policy lint --file on a broken draft: want exit 1 and `policy_broken: <draft>:<line>: …`\n%s", lint)
	}
	if chk := d.run(t, "policy", "check", "--file", "broken.draft.yaml", "entity:lock.x", "control"); chk.Code != 1 || chk.Stdout != "" {
		t.Errorf("policy check --file on a broken draft: want exit 1, empty stdout\n%s", chk)
	}
	if area := d.run(t, "area", "create", "PG Setup Spec Draft"); area.Code != 0 {
		t.Errorf("a broken draft beside a valid policy.yaml refused a command — the draft is not in effect\n%s", area)
	}
}

// TestPolicySetupSpecRelativeDraftIsFoundBesideEnv — the skill: "Write
// `policy.draft.yaml` in your instance directory (beside `.env`)" and then
// "hactl policy lint --file policy.draft.yaml". An agent's working directory
// is not its instance directory (an MCP server is pinned to a --dir, the CLI
// finds its instance), so the skill's own command only works when a relative
// --file names the draft beside .env. STRICTER READING of an ambiguity: the
// manual does not say what a relative --file is relative to.
func TestPolicySetupSpecRelativeDraftIsFoundBesideEnv(t *testing.T) {
	home := setupHome(t)
	dir := draftDir(t, setupDraftPolicy)
	elsewhere := t.TempDir()
	res := runSetupBin(t, home, elsewhere, nil, "--dir", dir, "policy", "show", "--file", setupDraftName, "--json")
	var doc struct {
		File    string `json:"file"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatalf("policy show --file from another working directory: %v\n%s", err, res)
	}
	if doc.Profile != "pg_draft" || doc.File != filepath.Join(dir, setupDraftName) {
		t.Errorf("policy show --file policy.draft.yaml run outside the instance directory: file %q profile %q, want the draft beside .env (%s)",
			doc.File, doc.Profile, filepath.Join(dir, setupDraftName))
	}
}

// ---------------------------------------------------------------------------
// review: real counts on this instance, lint findings, nothing written
// ---------------------------------------------------------------------------

// reviewDraft lets the agent switch every light, and never see or touch a
// lock; one misspelt deny is a lint finding.
const reviewDraft = `version: 1
active: pg_review
profiles:
  pg_review:
    default: deny
    allow:
      "entity:*": [read]
      "entity:light.*": [control]
      "service:light.*": [control]
    deny:
      "entity:lock.*": [read, write]
      "entity:lokc.*": [write]
`

var reCountOf = regexp.MustCompile(`(\d+) of (\d+)`)

// TestPolicySetupSpecReviewReportsRealCounts — manual step 3: "`hactl policy
// review --file policy.draft.yaml` says in counts and names what the draft's
// active profile would let the agent do on *this* instance — which entities it
// may switch and see, … which services it may call, what it can never touch,
// whether what written automations do is checked (D-135), and every lint
// finding." W-4.3: "with real counts ('may switch 34 of 212 entities: …; can
// never touch: lock.* (2), …; sees everything except …'), plus lint findings.
// It reads only." Every number is checked against /api/states read straight
// from Home Assistant.
func TestPolicySetupSpecReviewReportsRealCounts(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	home := setupHome(t)
	other := "version: 1\nactive: pg_other\nprofiles:\n  pg_other:\n    deny:\n      \"entity:light.*\": [control]\n"
	dir := setupInstanceDir(t, inst, other, false)
	writeFileT(t, filepath.Join(dir, setupDraftName), reviewDraft)

	statesBefore := haStates(t, inst)
	before := treeSnapshot(t, dir, skipCacheButAudit)
	res := runSetupBin(t, home, dir, nil, "--dir", dir, "policy", "review", "--file", setupDraftName)
	if diff := diffSnapshots(before, treeSnapshot(t, dir, skipCacheButAudit)); len(diff) > 0 {
		t.Errorf("policy review wrote to the instance directory (it reads only): %v", diff)
	}
	if statesAfter := haStates(t, inst); !slices.Equal(statesBefore, statesAfter) {
		t.Fatalf("fixture: the entity set changed while review ran (%d → %d) — the counts cannot be checked", len(statesBefore), len(statesAfter))
	}
	if strings.TrimSpace(res.Stdout) == "" {
		t.Fatalf("policy review --file printed nothing\n%s", res)
	}
	out := res.Stdout

	lights := withPrefix(statesBefore, "light.")
	locks := withPrefix(statesBefore, "lock.")
	if len(lights) < 2 || len(locks) < 1 {
		t.Fatalf("fixture: %d lights, %d locks — the review would prove nothing", len(lights), len(locks))
	}
	// "of N entities": the states, or the states with the registry's
	// entities that have none — either is a real count of this instance.
	registry := pgEntityRegistry(t, dialRawWS(t, inst.URL(), inst.Token()))
	union := map[string]bool{}
	for _, id := range statesBefore {
		union[id] = true
	}
	for id := range registry {
		union[id] = true
	}
	totals := []int{len(statesBefore), len(union)}

	switchLine := lineMatching(out, `(?i)switch`)
	if switchLine == "" {
		t.Errorf("policy review names no entities it may switch\n%s", out)
	} else if m := reCountOf.FindStringSubmatch(switchLine); m == nil {
		t.Errorf("policy review's switch line gives no `<n> of <total>` count: %q", switchLine)
	} else {
		n, _ := strconv.Atoi(m[1])
		total, _ := strconv.Atoi(m[2])
		if n != len(lights) {
			t.Errorf("policy review: may switch %d, but Home Assistant has %d light.* entities the draft lets it switch: %q", n, len(lights), switchLine)
		}
		if !slices.Contains(totals, total) {
			t.Errorf("policy review: of %d entities, but Home Assistant has %d in its states (%d with the registry): %q", total, totals[0], totals[1], switchLine)
		}
	}
	if !slices.ContainsFunc(lights, func(l string) bool { return strings.Contains(out, l) }) {
		t.Errorf("policy review names none of the lights it may switch (%v) — counts *and names*\n%s", lights, out)
	}
	if !strings.Contains(out, "light.turn_on") {
		t.Errorf("policy review does not name the services it may call (light.turn_on)\n%s", out)
	}

	neverLine := lineMatching(out, `(?i)never`)
	if neverLine == "" || !strings.Contains(neverLine, "lock.") || !strings.Contains(neverLine, strconv.Itoa(len(locks))) {
		t.Errorf("policy review: want a `can never touch` line naming lock.* and its live count %d, got %q\n%s", len(locks), neverLine, out)
	}
	if seeLine := lineMatching(out, `(?i)\bsee`); seeLine == "" || !strings.Contains(seeLine, "lock.") {
		t.Errorf("policy review: want a line on what it may see that names the hidden lock.*, got %q\n%s", seeLine, out)
	}
	if l := lineMatching(out, `no-match`); l == "" || !strings.Contains(l, "entity:lokc.*") {
		t.Errorf("policy review omits the lint finding no-match on deny \"entity:lokc.*\"\n%s", out)
	}

	t.Run("D-135 indirect", func(t *testing.T) {
		if strings.Contains(out, "unchecked") {
			t.Errorf("a draft that does not switch the check off reports something unchecked:\n%s", out)
		}
		unchecked := strings.Replace(reviewDraft, "    default: deny\n", "    default: deny\n    indirect: unchecked\n", 1)
		writeFileT(t, filepath.Join(dir, "policy.unchecked.yaml"), unchecked)
		u := runSetupBin(t, home, dir, nil, "--dir", dir, "policy", "review", "--file", "policy.unchecked.yaml")
		if !strings.Contains(u.Stdout, "unchecked") {
			t.Errorf("policy review of a draft with `indirect: unchecked` does not say what written automations do is unchecked\n%s", u)
		}
	})

	t.Run("without --file it reviews policy.yaml", func(t *testing.T) {
		same := setupInstanceDir(t, inst, reviewDraft, false)
		r := runSetupBin(t, home, same, nil, "--dir", same, "policy", "review")
		if got, want := lineMatching(r.Stdout, `(?i)switch`), switchLine; got == "" || got != want {
			t.Errorf("policy review of policy.yaml (= the draft): switch line %q, want %q\n%s", got, want, r)
		}
		if reDraftFileLine.MatchString(r.Stdout) {
			t.Errorf("policy review without --file calls policy.yaml a draft:\n%s", r.Stdout)
		}
	})
}

// lintFinding is one entry of `policy lint --json` findings.
type lintFinding struct {
	Code string `json:"code"`
	Rule string `json:"rule"`
	Line int    `json:"line"`
}

// lineMatching returns the first line of s matching pattern, trimmed.
func lineMatching(s, pattern string) string {
	re := regexp.MustCompile(pattern)
	for l := range strings.SplitSeq(s, "\n") {
		if re.MatchString(l) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// activate: only at the owner's terminal
// ---------------------------------------------------------------------------

// assertNotActivated: policy.yaml is still setupLivePolicy byte for byte, no
// policy.yaml.prev appeared, and nothing new sits beside it.
func assertNotActivated(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	if got := readFileT(t, filepath.Join(dir, "policy.yaml")); got != setupLivePolicy {
		t.Errorf("policy.yaml changed although activation was refused:\n%s", got)
	}
	if fileExists(filepath.Join(dir, "policy.yaml.prev")) {
		t.Error("policy.yaml.prev exists although activation was refused")
	}
	if diff := diffSnapshots(before, treeSnapshot(t, dir, skipCacheButAudit)); len(diff) > 0 {
		t.Errorf("a refused activation left changes in the instance directory: %v", diff)
	}
}

// TestPolicySetupSpecActivateRefusedWithoutATerminal — manual step 4: "It
// refuses unless stdin and stdout are a terminal, so neither an agent nor
// Claude Code's `!` prefix can run it (both have none, M-5)"; D-149: "stdin
// and stdout must be a terminal". Each half alone is not enough, and a piped
// answer that is the right profile name changes nothing.
func TestPolicySetupSpecActivateRefusedWithoutATerminal(t *testing.T) {
	home := setupHome(t)
	answer := "pg_draft\n"
	for _, tc := range []struct {
		name      string
		stdinTTY  bool
		stdoutTTY bool
	}{
		{"no terminal (an agent's shell, the ! prefix)", false, false},
		{"stdin a terminal, stdout a pipe", true, false},
		{"stdout a terminal, stdin a pipe", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := draftDir(t, setupDraftPolicy)
			before := treeSnapshot(t, dir, skipCacheButAudit)
			args := []string{"--dir", dir, "policy", "activate", setupDraftName}

			var res procResult
			if !tc.stdinTTY && !tc.stdoutTTY {
				res = runSetupBin(t, home, dir, &answer, args...)
			} else {
				res = runHalfTerminal(t, home, dir, tc.stdinTTY, answer, args...)
			}
			if res.Code == 0 {
				t.Errorf("policy activate without a terminal on both stdin and stdout exited 0 (D-149)\n%s", res)
			}
			if !strings.Contains(strings.ToLower(res.Stdout+res.Stderr), "terminal") {
				t.Errorf("the refusal does not tell the caller it needs a terminal of its own\n%s", res)
			}
			assertNotActivated(t, dir, before)
		})
	}
}

// runHalfTerminal runs the binary with a pty on exactly one of stdin and
// stdout and a pipe on the other; stderr is a pipe. When stdin is the pipe it
// carries answer; when stdin is the pty, answer is typed into it.
func runHalfTerminal(t *testing.T, home, cwd string, stdinTTY bool, answer string, args ...string) procResult {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	cmd := execBin(t, home, cwd, args...)
	var out lockedBuffer
	var errOut strings.Builder
	cmd.Stderr = &errOut
	if stdinTTY {
		cmd.Stdin = tty
		cmd.Stdout = &out
	} else {
		cmd.Stdin = strings.NewReader(answer)
		cmd.Stdout = tty
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting hactl %v: %v", args, err)
	}
	_ = tty.Close()
	drained := make(chan struct{})
	go func() { // drain the terminal side so the child never blocks on it
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if !stdinTTY && n > 0 {
				_, _ = out.Write(buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()
	if stdinTTY {
		_, _ = ptmx.WriteString(strings.TrimSuffix(answer, "\n") + "\r")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("hactl %v with half a terminal did not finish within 60s\nstdout:\n%s\nstderr:\n%s", args, out.String(), errOut.String())
	}
	if !stdinTTY {
		select { // the child is gone; the terminal side ends once drained
		case <-drained:
		case <-time.After(2 * time.Second):
		}
	}
	return procResult{Stdout: out.String(), Stderr: errOut.String(), Code: cmd.ProcessState.ExitCode()}
}

// lockedBuffer is a bytes.Buffer two goroutines may share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPolicySetupSpecActivateBlockedOverMCP — manual step 4: "over MCP it is
// never offered"; W-4.4: "it is `alwaysBlocked` over MCP". Asked of a server
// that allows writes, so the refusal cannot be the read-only one.
func TestPolicySetupSpecActivateBlockedOverMCP(t *testing.T) {
	home := setupHome(t)
	dir := draftDir(t, setupDraftPolicy)
	before := treeSnapshot(t, dir, skipCacheButAudit)
	c := startMCPServer(t, home, dir)
	for _, line := range []string{
		"policy activate " + filepath.Join(dir, setupDraftName),
		"policy activate " + setupDraftName,
	} {
		text, isErr := c.call(t, line)
		if !isErr {
			t.Errorf("%q over MCP was not an error result:\n%s", line, text)
		}
		if !strings.Contains(text, "not available over MCP") {
			t.Errorf("%q over MCP: want the never-offered refusal (`… is not available over MCP`), got:\n%s", line, text)
		}
	}
	assertNotActivated(t, dir, before)
}

// TestPolicySetupSpecActivateInATerminal — manual step 4: "It lints the draft
// (a broken draft is refused), prints the review, asks for the profile name as
// confirmation, then writes `policy.yaml` atomically and keeps the previous
// one as `policy.yaml.prev`." D-148: the owner activates. A lint finding that
// is not `broken` (here a no-match) does not stop it, and the activated
// profile is the one in effect for the next command.
func TestPolicySetupSpecActivateInATerminal(t *testing.T) {
	home := setupHome(t)
	dir := draftDir(t, setupDraftPolicy)
	review := runSetupBin(t, home, dir, nil, "--dir", dir, "policy", "review", "--file", setupDraftName)
	if strings.TrimSpace(review.Stdout) == "" {
		t.Fatalf("policy review --file printed nothing — activate's review cannot be compared\n%s", review)
	}

	// A reader holding the old file open: with an atomic write (a new file
	// renamed over the old) it still reads the old policy in full.
	held, err := os.Open(filepath.Join(dir, "policy.yaml")) //nolint:gosec // test-owned instance directory
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	s := startPTY(t, home, dir, "--dir", dir, "policy", "activate", setupDraftName)
	if !s.waitQuiet(2500*time.Millisecond, 90*time.Second) {
		t.Fatalf("policy activate printed nothing and did not stop to ask within 90s\n%s", s.transcript())
	}
	if s.hasExited() {
		code, _ := s.wait(5 * time.Second)
		t.Fatalf("policy activate on a terminal exited %d without asking for the profile name\n%s", code, s.transcript())
	}
	asked := s.transcript()
	for l := range strings.SplitSeq(review.Stdout, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.Contains(asked, l) {
			t.Errorf("activate did not print the review before asking — missing %q\n--- transcript ---\n%s", l, asked)
		}
	}
	if !strings.Contains(asked, "lokc") {
		t.Errorf("activate did not show the draft's lint finding (no-match on entity:lokc.*) before asking\n%s", asked)
	}
	if got := readFileT(t, filepath.Join(dir, "policy.yaml")); got != setupLivePolicy {
		t.Errorf("policy.yaml changed before the owner confirmed:\n%s", got)
	}

	s.typeLine(t, "pg_draft")
	code, ok := s.wait(60 * time.Second)
	if !ok {
		t.Fatalf("policy activate did not finish within 60s after the profile name was typed\n%s", s.transcript())
	}
	if code != 0 {
		t.Fatalf("policy activate: exit %d after the right profile name\n%s", code, s.transcript())
	}

	if got := readFileT(t, filepath.Join(dir, "policy.yaml")); got != setupDraftPolicy {
		t.Errorf("policy.yaml after activate is not the draft:\n%s", got)
	}
	if got := readFileT(t, filepath.Join(dir, "policy.yaml.prev")); got != setupLivePolicy {
		t.Errorf("policy.yaml.prev is not the previous policy.yaml:\n%s", got)
	}
	if b, readErr := readAllFrom(held); readErr != nil || b != setupLivePolicy {
		t.Errorf("a reader that held the old policy.yaml open now reads %q (err %v) — the write was not atomic (a new file renamed into place)", b, readErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Name() {
		case ".env", "policy.yaml", "policy.yaml.prev", setupDraftName, "cache":
		default:
			t.Errorf("activate left %q in the instance directory — a temporary file of a non-atomic write?", e.Name())
		}
	}

	var doc struct {
		Profile    string `json:"profile"`
		SelectedBy string `json:"selected_by"`
	}
	show := runSetupBin(t, home, dir, nil, "--dir", dir, "policy", "show", "--json")
	if err := json.Unmarshal([]byte(show.Stdout), &doc); err != nil {
		t.Fatalf("policy show --json: %v\n%s", err, show)
	}
	if doc.Profile != "pg_draft" || doc.SelectedBy != "active" {
		t.Errorf("after activate: profile %q selected_by %q, want pg_draft by active", doc.Profile, doc.SelectedBy)
	}
	if area := runSetupBin(t, home, dir, nil, "--dir", dir, "area", "create", "PG Setup Spec Activated"); area.Code != 1 ||
		!strings.Contains(area.Stdout, "profile: pg_draft") {
		t.Errorf("after activate, area create (dry run) is not refused by the activated pg_draft\n%s", area)
	}
}

func readAllFrom(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	return string(b), err
}

// TestPolicySetupSpecActivateWrongNameAborts — manual step 4: "asks for the
// profile name as confirmation"; D-149: "the owner types the profile name".
// Any other answer — here the old profile's name, the likeliest slip — writes
// nothing.
func TestPolicySetupSpecActivateWrongNameAborts(t *testing.T) {
	home := setupHome(t)
	dir := draftDir(t, setupDraftPolicy)
	before := treeSnapshot(t, dir, skipCacheButAudit)
	s := startPTY(t, home, dir, "--dir", dir, "policy", "activate", setupDraftName)
	if !s.waitQuiet(2500*time.Millisecond, 90*time.Second) || s.hasExited() {
		code, _ := s.wait(5 * time.Second)
		t.Fatalf("policy activate did not stop to ask for the profile name (exit %d)\n%s", code, s.transcript())
	}
	s.typeLine(t, "pg_live")
	code, ok := s.wait(60 * time.Second)
	if !ok {
		t.Fatalf("policy activate did not finish within 60s after a wrong name\n%s", s.transcript())
	}
	if code == 0 {
		t.Errorf("policy activate exited 0 after the wrong profile name\n%s", s.transcript())
	}
	assertNotActivated(t, dir, before)
}

// TestPolicySetupSpecActivateRefusesABrokenDraft — manual step 4: "It lints
// the draft (a broken draft is refused)" — before it asks anything, since the
// review comes after the lint; problems are worded as everywhere,
// `policy_broken: <file>:<line>: <message>`.
func TestPolicySetupSpecActivateRefusesABrokenDraft(t *testing.T) {
	home := setupHome(t)
	broken := "version: 1\nactive: pg_broken\nprofiles:\n  pg_broken:\n    deny:\n      \"entity:lock.*\": [wirte]\n"
	dir := draftDir(t, broken)
	before := treeSnapshot(t, dir, skipCacheButAudit)
	out, code := runPTYNoInput(t, home, dir, "--dir", dir, "policy", "activate", setupDraftName)
	if code == 0 {
		t.Errorf("policy activate of a broken draft exited 0\n%s", out)
	}
	if want := "policy_broken: " + filepath.Join(dir, setupDraftName) + ":"; !strings.Contains(out, want) {
		t.Errorf("the refusal does not name the draft's problem as `%s<line>: …`\n%s", want, out)
	}
	assertNotActivated(t, dir, before)
}
