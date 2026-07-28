package surfaceaudit

import (
	"errors"
	"sort"
)

// ---------------------------------------------------------------------------
// Write-back surface
// ---------------------------------------------------------------------------

// ErrNoWriteCommands is returned when the census handed to [WriteBackSurface]
// is empty.
//
// It is an error rather than an empty surface because an extractor that has
// stopped matching is the one failure a closure gate cannot survive: it passes,
// forever, proving nothing. The gate checks for emptiness too; this makes the
// derivation itself refuse to produce a vacuous answer, so a future caller
// cannot lose the property by forgetting the check.
var ErrNoWriteCommands = errors.New(
	"no write command reached the write-back surface — the --confirm census is empty, so the cobra walk has stopped matching")

// WriteBackSurface is every command that mutates Home Assistant, one site per
// command.
//
// Rule (INVARIANTS.md H-12): a write is proven by reading it back from Home
// Assistant. Read the state from HA directly, write through hactl with
// `--confirm`, read it back from HA directly, and compare the whole document —
// with at least one assertion on a field the command never prints, as an
// independent witness that the document landed whole and that nothing else
// moved. The dry run is asserted to change nothing, and the restore is asserted
// too. Reading back *through hactl* does not count: then hactl both writes and
// verifies, and a shared modelling mistake agrees with itself.
//
// # Why this surface exists
//
// H-12 is stated as a universal — "every write family" — and enforced by an
// "Enforced by:" list of the families that happened to get a round-trip test
// written for them. That is the defect class this package exists to close: the
// law quantifies over a set nobody computes, so a write family added tomorrow is
// covered by no test and leaves no trace anywhere. `dash save` sat in exactly
// that state until `docs/testing.md` swept it up by hand, and the six registry
// commands (`area`/`floor`/`label` create+delete) are still verified through
// `hactl … ls` — hactl proving itself — which the law names as insufficient in
// its second paragraph.
//
// # Where the set comes from, and why not from a second extractor
//
// The census is the confirm surface's: every command in the live cobra tree
// carrying a `--confirm` flag. H-2 makes `--confirm` the definition of a
// mutating command ("mutating commands are dry-run by default"), so the two laws
// quantify over one set, and it is derived once. Deriving it a second time —
// from the source, from the mutating client calls — would give the enumeration
// two chances to disagree, and a scope built beside the real one is precisely
// how `auto apply` came to be missing from the thirteen commands that learned to
// resolve their target.
//
// The granularity is one site per command, not per write family, because the
// command is the unit a read-back test drives and therefore the unit a
// disposition can honestly speak about: `script create`, `script apply` and
// `script delete` are one family but three writes, and a family-level ledger
// would have let `tpl delete`'s ghost-cleanup gap hide behind `tpl create`'s
// proof. Keys are the full command paths the cobra tree yields, unchanged from
// the confirm census, so one grep across dev/surfaces/ shows every ledger's
// verdict on the same command.
//
// # The blind spot this surface does not close
//
// A command that mutates Home Assistant without a `--confirm` gate is invisible
// here, because it is invisible to the tree walk. Such a command is an H-2
// defect before it is an H-12 one, and nothing in this repository derives "every
// mutating command carries `--confirm`" yet — the confirm and write-back
// censuses both start from the flag. Recorded here rather than left to be
// rediscovered: a known limitation is cheap, an assumed completeness is not.
func WriteBackSurface(confirm []Site) (Surface, error) {
	if len(confirm) == 0 {
		return Surface{}, ErrNoWriteCommands
	}
	s := Surface{
		Name: "writeback",
		Rule: "a write is proven by reading it back from Home Assistant directly — never through hactl, which would only prove hactl consistent with itself",
	}
	for _, site := range confirm {
		site.Note = "mutates HA when --confirm is passed"
		s.Sites = append(s.Sites, site)
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}
