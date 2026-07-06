// Package manual sections the embedded LLM manual (docs/manual.md) and owns
// the progressive-delivery taxonomy: which sections form the always-needed
// core and which travel with each command family. It is the Go counterpart of
// the sectioning logic in integrations/llm/tools.py and must keep the same
// heading semantics (docs/llm-tuning.md Rule 11).
package manual

import (
	"regexp"
	"strings"
	"sync"

	"github.com/hemm-ems/hactl/docs"
)

// PreambleKey indexes the text before the first heading in Sections().
const PreambleKey = "(preamble)"

var (
	headingRe    = regexp.MustCompile(`(?m)^#{2,3} .+$`)
	sectionsOnce sync.Once
	sections     map[string]string
)

// Sections returns the manual split at ##/### headings, keyed by the verbatim
// heading line. Each value starts with its heading line; the text before the
// first heading is stored under PreambleKey.
func Sections() map[string]string {
	sectionsOnce.Do(func() {
		locs := headingRe.FindAllStringIndex(docs.Manual, -1)
		sections = make(map[string]string, len(locs)+1)
		if len(locs) == 0 {
			sections[PreambleKey] = strings.TrimSpace(docs.Manual)
			return
		}
		sections[PreambleKey] = strings.TrimSpace(docs.Manual[:locs[0][0]])
		for i, loc := range locs {
			end := len(docs.Manual)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			heading := strings.TrimSpace(docs.Manual[loc[0]:loc[1]])
			sections[heading] = strings.TrimRight(docs.Manual[loc[0]:end], " \t\n")
		}
	})
	return sections
}

// CoreText returns the cold-start core block: preamble plus CoreHeadings,
// blank-line joined (~1.4k tokens).
func CoreText() string {
	s := Sections()
	parts := []string{s[PreambleKey]}
	for _, h := range CoreHeadings {
		if text, ok := s[h]; ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// FamilyText returns the blank-line-joined how-to sections of a family that
// are not in skip, plus the headings it included. Order follows
// FamilySections (workflows before reference).
func FamilyText(family string, skip map[string]bool) (string, []string) {
	s := Sections()
	var parts []string
	var headings []string
	for _, h := range FamilySections[family] {
		if skip[h] {
			continue
		}
		if text, ok := s[h]; ok {
			parts = append(parts, text)
			headings = append(headings, h)
		}
	}
	return strings.Join(parts, "\n\n"), headings
}
