package surfaceaudit

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// InvariantsFile is the law book this audit reads.
const InvariantsFile = "INVARIANTS.md"

// Invariant is one `## H-n — …` section.
type Invariant struct {
	ID    string // "H-17"
	Title string // the law, as stated
	Line  int
	// Cites are the test names the section names as its enforcement.
	Cites []string
}

var (
	invariantHeading = regexp.MustCompile(`^##\s+(H-\d+)\s+[—-]\s+(.*)$`)
	// A citation is a Go test identifier: `Test` followed by an upper-case
	// letter. The bound matters — without it the plural "Tests" in prose parses
	// as a citation and the gate reports the document's own English as a defect.
	testIdent  = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*`)
	enforcedBy = regexp.MustCompile(`^\s*[-*]\s*(Enforced|Quantified) by:`)
)

// ParseInvariants reads INVARIANTS.md.
//
// Citations are taken only from the "Enforced by:" bullet to the end of the
// section, never from the prose above it. The prose routinely names tests that
// were deliberately deleted or inverted — H-2's own history renamed
// TestDashDeleteDryRun out of existence and said so — and a gate that treated
// those as live citations would report the file's honesty as a defect.
func ParseInvariants(root string) ([]Invariant, error) {
	path := filepath.Join(root, InvariantsFile)
	f, err := os.Open(path) //nolint:gosec // fixed repository-relative path
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", InvariantsFile, err)
	}
	defer func() { _ = f.Close() }()

	var out []Invariant
	var cur *Invariant
	inCitations := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if m := invariantHeading.FindStringSubmatch(line); m != nil {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Invariant{ID: m[1], Title: strings.TrimSpace(m[2]), Line: n}
			inCitations = false
			continue
		}
		if cur == nil {
			continue
		}
		if enforcedBy.MatchString(line) {
			inCitations = true
		}
		if inCitations {
			cur.Cites = append(cur.Cites, testIdent.FindAllString(line, -1)...)
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", InvariantsFile, err)
	}
	for i := range out {
		out[i].Cites = dedupe(out[i].Cites)
	}
	return out, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// PhantomCitations returns the citations that name no test in the repository.
//
// This is the check that keeps an "Enforced by:" list from decaying into
// decoration. A list is only evidence while every name on it resolves; the
// moment one does not, the list has stopped tracking the code and nobody found
// out, because reading a document is not a build step.
func PhantomCitations(invs []Invariant, proofExists func(string) bool) []string {
	var out []string
	for _, inv := range invs {
		for _, name := range inv.Cites {
			if !proofExists(name) {
				out = append(out, fmt.Sprintf(
					"%s:%d %s cites %s, which no test in any tier defines",
					InvariantsFile, inv.Line, inv.ID, name))
			}
		}
	}
	sort.Strings(out)
	return out
}

// InvariantSurface is every law in INVARIANTS.md.
//
// Rule: an invariant is enforced by a gate that quantifies over the set it
// speaks about, not by a list of the sites that were fixed when it was written.
//
// Every heading in the file states a universal — "an identifier hactl prints is
// an identifier hactl accepts", "a preview fails exactly where the confirmed run
// would", "every decoded field is documented". Each was then enforced by naming
// the handful of tests written alongside the fix. An enumeration cannot be
// incomplete, because the list *is* the scope; so the document reads as a proof
// and functions as a receipt. H-2 lists thirteen commands and thirty-one carry
// `--confirm`. H-17's own prose asserts that `auto diff`/`apply` resolve
// correctly, which was false when it was written.
//
// A site here is dispositioned `proven` only by a test that walks a surface. It
// is the one place in this package where "proven" means something stronger than
// "a test exists".
func InvariantSurface(root string) (Surface, error) {
	invs, err := ParseInvariants(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name: "invariant",
		Rule: "a universal law is enforced by a gate that quantifies over its set, not by a list of fixed sites",
	}
	for _, inv := range invs {
		note := fmt.Sprintf("%d cited test(s)", len(inv.Cites))
		if len(inv.Cites) == 0 {
			note = "cites no test at all"
		}
		s.Sites = append(s.Sites, Site{
			Key:  inv.ID,
			File: InvariantsFile,
			Line: inv.Line,
			Note: note + ": " + truncate(inv.Title, 70),
		})
	}
	return s, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
