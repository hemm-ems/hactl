// Package surfaceaudit closes a rule against the set of places the rule has to
// reach, so that a fix can no longer be narrower than the property it claims.
//
// # The failure this defends against
//
// Every gate this repository had before it verifies the change that was made.
// None of them could say the change was too small. That is the difference
// between the two questions a fix has to answer:
//
//	"does the thing I fixed stay fixed?"     — the assertion floor, H-12, the tiers
//	"did I fix every place this applies?"    — nothing
//
// Four defects reported against v2026.7.12 were each the unfixed half of a fix
// shipped in the same release. In every case the fix's scope was an enumeration
// built by hand or by grepping the symptom, and the sites it missed left no
// trace anywhere: not in the code, not in the tests, not in INVARIANTS.md. A
// missing site was indistinguishable from a site that did not exist.
//
//   - `auto apply` was not among the thirteen write commands that learned to
//     resolve their target, because the scope was "commands printing
//     `dry-run: would …`" and `auto apply` prints `dry-run: no changes written`.
//     The E2E table that would have caught it lists `script apply` and four
//     deletes — five hand-written rows, and the sixth was the bug.
//   - `trace show` still rendered UTC after the timezone fix, because
//     `analyze.shortTimestamp` is a fourth, independent timestamp renderer that
//     never calls `time.Parse` at all, and `analyze.FormatShortTimestamp` is a
//     fifth. A test pinned the UTC-in/UTC-out behaviour as correct.
//   - `auto diff`/`auto apply` still refused identifiers `auto ls` prints,
//     although `resolveAutomation` existed and its own doc comment names
//     `diff`/`apply` as callers. INVARIANTS.md H-17 asserted they resolved
//     correctly as background fact.
//   - `device ls --pattern` lost case-insensitivity in a *consistency* commit,
//     harmonised toward the sibling with no stake in the answer, while the
//     three filter flags beside it in the same function kept it.
//
// # The mechanism
//
// A [Surface] is the set of [Site]s a rule must reach, derived mechanically
// from the source or from the live command tree — never typed out. A manifest
// binds every site to a [Disposition]. [Check] fails when a site has none.
//
// The property that matters is not that every site is proven. It is that no
// site can be *silent*. A site is proven, or knowingly exempt with a reason, or
// recorded as debt in a file a reviewer reads — and a site nobody has
// considered fails the build the day it appears. Debt is legal, invisible debt
// is not.
//
// Three failure modes are therefore all hard errors:
//
//   - unclassified — the site exists and the manifest does not mention it. This
//     is the one that makes a new command red by default.
//   - stale — the manifest mentions a site that no longer exists, so the ledger
//     stops describing the code.
//   - phantom — a disposition names a proof that no test in the repository
//     defines. This is what rots an "Enforced by:" list into decoration.
//
// [Debt] entries are reported, not failed, but a surface may not carry more of
// them than its recorded ceiling. Raising a ceiling is a one-line, greppable,
// reviewable act; forgetting a site is not an act at all. That asymmetry is the
// whole design.
package surfaceaudit

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Site is one place a rule has to reach.
type Site struct {
	// Key identifies the site stably across refactors of everything except
	// the thing it names. It is what a manifest line is keyed on, so it must
	// read as prose to whoever has to disposition it: "auto apply", not
	// "internal/cmd/auto.go:922".
	Key string
	// File and Line locate it for the report. They deliberately do not take
	// part in the key: a site that moves down a file is the same site.
	File string
	Line int
	// Note is extractor-supplied context printed beside the key, so the
	// report explains itself without the reader opening the file.
	Note string
}

// Surface is the complete set of sites a rule must reach, plus the name the
// manifest and the report use for it.
type Surface struct {
	// Name is the manifest's basename and the report's heading.
	Name string
	// Rule is the one-sentence property every site must satisfy. It is
	// printed at the top of every failure, because a gate that says only
	// "unclassified site" teaches nothing.
	Rule string
	// Sites is the derived set. An extractor that returns an empty surface is
	// itself an error — see [Check] — unless AllowEmpty says otherwise.
	Sites []Site
	// AllowEmpty marks a surface whose sites are *violations* rather than a
	// census, so zero of them is the goal rather than a broken extractor.
	//
	// A census surface (clock, confirm, target) lists every place the rule
	// reaches and can never legitimately be empty; emptiness there means the
	// extractor stopped matching and the gate has been passing while proving
	// nothing. A violation surface reaches zero when the rule holds
	// everywhere, and its extractor is instead guarded by a fixture test that
	// feeds it a known-bad function and requires it to be flagged.
	AllowEmpty bool
}

// DispositionKind is what a manifest claims about one site.
type DispositionKind int

const (
	// Proven means a named test asserts the rule for this site. The name must
	// resolve to a test function that exists, or the disposition is phantom.
	Proven DispositionKind = iota
	// Exempt means the rule does not apply here, for a stated reason.
	Exempt
	// Debt means the rule applies, nothing proves it, and that is recorded on
	// purpose. Legal, counted, printed, and capped.
	Debt
)

func (k DispositionKind) String() string {
	switch k {
	case Proven:
		return "proven"
	case Exempt:
		return "exempt"
	case Debt:
		return "debt"
	}
	return "unknown"
}

// Disposition is one manifest line's claim.
type Disposition struct {
	Kind   DispositionKind
	Detail string // test name for Proven; prose for Exempt and Debt
	Line   int    // line in the manifest, for the report
}

// MinReason is the shortest Exempt or Debt reason the gate accepts. It exists
// for the same purpose as testaudit's: to stop `exempt: n/a` from becoming the
// idiom. A reason nobody can be bothered to write is a reason nobody had.
const MinReason = 25

// Manifest is a surface's ledger, parsed from dev/surfaces/<name>.manifest.
type Manifest struct {
	Path    string
	Ceiling int // maximum Debt entries this surface may carry
	Entries map[string]Disposition
}

// ManifestDir is where the ledgers live. They are deliberately not under a
// testdata/ directory: they are not fixtures, they are the project's running
// account of where each rule does and does not hold, and they are meant to be
// read by a person deciding what to work on next.
const ManifestDir = "dev/surfaces"

// ceilingPrefix introduces the debt ceiling line.
const ceilingPrefix = "#ceiling "

// LoadManifest parses a surface's ledger.
//
// The format is deliberately line-oriented and dumb — one site per line, keyed
// by prose — so that a diff of this file reads as a list of decisions rather
// than a reformatting.
//
//	#ceiling 3
//	auto apply = debt: no test asserts the preview refuses an unresolvable id
//	auto delete = proven: TestE2EDryRunRejectsFabricatedTargetCLI
//	svc call = exempt: a service call has no target to resolve; HA validates it
func LoadManifest(root, name string) (*Manifest, error) {
	path := filepath.Join(root, ManifestDir, name+".manifest")
	f, err := os.Open(path) //nolint:gosec // path is composed from a constant dir and a caller-fixed surface name
	if err != nil {
		return nil, fmt.Errorf("opening manifest: %w", err)
	}
	defer func() { _ = f.Close() }()

	m := &Manifest{Path: filepath.Join(ManifestDir, name+".manifest"), Ceiling: -1, Entries: map[string]Disposition{}}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if after, ok := strings.CutPrefix(line, ceilingPrefix); ok {
			if _, scanErr := fmt.Sscanf(strings.TrimSpace(after), "%d", &m.Ceiling); scanErr != nil {
				return nil, fmt.Errorf("%s:%d: unreadable ceiling %q", m.Path, n, after)
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rest, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: no '=' in %q", m.Path, n, line)
		}
		key = strings.TrimSpace(key)
		kindStr, detail, ok := strings.Cut(strings.TrimSpace(rest), ":")
		if !ok {
			return nil, fmt.Errorf("%s:%d: no ':' after the disposition in %q", m.Path, n, line)
		}
		var kind DispositionKind
		switch strings.TrimSpace(kindStr) {
		case "proven":
			kind = Proven
		case "exempt":
			kind = Exempt
		case "debt":
			kind = Debt
		default:
			return nil, fmt.Errorf("%s:%d: unknown disposition %q (want proven, exempt or debt)", m.Path, n, kindStr)
		}
		if _, dup := m.Entries[key]; dup {
			return nil, fmt.Errorf("%s:%d: %q is dispositioned twice", m.Path, n, key)
		}
		m.Entries[key] = Disposition{Kind: kind, Detail: strings.TrimSpace(detail), Line: n}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	if m.Ceiling < 0 {
		return nil, fmt.Errorf("%s: no '%s<n>' line — a surface must state how much debt it tolerates", m.Path, ceilingPrefix)
	}
	return m, nil
}

// Result is what Check concluded, in the shape a gate reports.
type Result struct {
	Surface string
	Rule    string

	// Unclassified sites exist and the manifest is silent about them. Always
	// a failure: this is the closure property itself.
	Unclassified []Site
	// Stale keys are dispositioned but no longer name a site.
	Stale []string
	// Phantom dispositions claim a proof that does not exist.
	Phantom []string
	// ThinReason dispositions have a reason too short to be one.
	ThinReason []string
	// Debt sites are knowingly unproven. Reported, not failed — unless the
	// surface carries more than its ceiling.
	Debt []string
	// Ceiling is what the manifest allows.
	Ceiling int

	// Proven and Exempt are counted for the summary line, which is the part
	// a reader actually looks at when the gate is green.
	Proven, Exempt int
}

// Failed reports whether the gate must go red.
func (r Result) Failed() bool {
	return len(r.Unclassified) > 0 || len(r.Stale) > 0 || len(r.Phantom) > 0 ||
		len(r.ThinReason) > 0 || len(r.Debt) > r.Ceiling
}

// Check binds a surface to its manifest.
//
// proofExists reports whether a named test function is defined somewhere in the
// repository. It is a parameter rather than a package-level lookup so the gate
// decides what counts as a proof: the confirm surface resolves names against
// every tier including the Docker-gated ones, which is the only scope in which
// "TestE2E…" is a real name.
func Check(s Surface, m *Manifest, proofExists func(string) bool) Result {
	res := Result{Surface: s.Name, Rule: s.Rule, Ceiling: m.Ceiling}

	seen := map[string]bool{}
	for _, site := range s.Sites {
		seen[site.Key] = true
		d, ok := m.Entries[site.Key]
		if !ok {
			res.Unclassified = append(res.Unclassified, site)
			continue
		}
		switch d.Kind {
		case Proven:
			if !proofExists(d.Detail) {
				res.Phantom = append(res.Phantom, fmt.Sprintf(
					"%s:%d: %s = proven: %s — no test by that name exists in any tier",
					m.Path, d.Line, site.Key, d.Detail))
				continue
			}
			res.Proven++
		case Exempt:
			if len(d.Detail) < MinReason {
				res.ThinReason = append(res.ThinReason, fmt.Sprintf(
					"%s:%d: %s = exempt: %q — a reason must be at least %d characters",
					m.Path, d.Line, site.Key, d.Detail, MinReason))
				continue
			}
			res.Exempt++
		case Debt:
			if len(d.Detail) < MinReason {
				res.ThinReason = append(res.ThinReason, fmt.Sprintf(
					"%s:%d: %s = debt: %q — a reason must be at least %d characters",
					m.Path, d.Line, site.Key, d.Detail, MinReason))
				continue
			}
			res.Debt = append(res.Debt, fmt.Sprintf("%s — %s  [%s:%d]", site.Key, d.Detail, site.File, site.Line))
		}
	}

	for key, d := range m.Entries {
		if !seen[key] {
			res.Stale = append(res.Stale, fmt.Sprintf(
				"%s:%d: %q is dispositioned but is no longer a site on this surface — delete the line",
				m.Path, d.Line, key))
		}
	}

	sort.Strings(res.Stale)
	sort.Strings(res.Phantom)
	sort.Strings(res.ThinReason)
	sort.Strings(res.Debt)
	return res
}

// Report renders a Result for a failing gate. It always ends with the manifest
// lines the author would have to add, because the cost of doing the right thing
// is the only lever a gate really has over whether it gets obeyed.
func (r Result) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nsurface %q — %s\n", r.Surface, r.Rule)
	fmt.Fprintf(&b, "  proven %d   exempt %d   debt %d/%d\n", r.Proven, r.Exempt, len(r.Debt), r.Ceiling)

	if len(r.Unclassified) > 0 {
		fmt.Fprintf(&b, "\n%d site(s) nobody has dispositioned. A site this rule reaches may be\n", len(r.Unclassified))
		b.WriteString("proven, exempt or recorded as debt — it may not be silent:\n")
		for _, s := range r.Unclassified {
			fmt.Fprintf(&b, "  %s  (%s)  [%s:%d]\n", s.Key, s.Note, s.File, s.Line)
		}
		b.WriteString("\nadd to " + ManifestDir + "/" + r.Surface + ".manifest:\n")
		for _, s := range r.Unclassified {
			fmt.Fprintf(&b, "  %s = debt: <why nothing proves this yet, %d+ chars>\n", s.Key, MinReason)
		}
	}
	for _, group := range []struct {
		head  string
		items []string
	}{
		{"stale entries — the ledger no longer describes the code", r.Stale},
		{"phantom proofs — a disposition names a test that does not exist", r.Phantom},
		{"reasons too thin to be reasons", r.ThinReason},
	} {
		if len(group.items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s:\n", group.head)
		for _, it := range group.items {
			fmt.Fprintf(&b, "  %s\n", it)
		}
	}
	if len(r.Debt) > r.Ceiling {
		fmt.Fprintf(&b, "\ndebt %d exceeds the ceiling of %d. Prove a site, or raise the\n", len(r.Debt), r.Ceiling)
		fmt.Fprintf(&b, "'%s<n>' line in %s on purpose:\n", ceilingPrefix, r.Surface+".manifest")
	} else if len(r.Debt) > 0 {
		b.WriteString("\noutstanding debt on this surface (within ceiling):\n")
	}
	for _, d := range r.Debt {
		fmt.Fprintf(&b, "  %s\n", d)
	}
	return b.String()
}
