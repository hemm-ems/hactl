package policy

import "strconv"

// suggestDistance is how far an unknown word may be from a valid one and still
// be suggested (D-117): two edits catch a transposition (`wirte`), a dropped
// or doubled letter (`entiy`, `allows`), and stay clear of words that only
// share a length.
const suggestDistance = 2

// didYouMean is the ` — did you mean "<word>"?` an unknown verb, key, kind,
// sub-resource word or registry field name ends with (D-117): the word of
// words nearest to unknown, when it is within suggestDistance edits; a tie
// goes to the first in words, which callers pass in the manual's order. ""
// when nothing is that close.
func didYouMean(unknown string, words []string) string {
	best, bestDist := "", suggestDistance+1
	for _, w := range words {
		if d := editDistance(unknown, w); d < bestDist {
			best, bestDist = w, d
		}
	}
	if best == "" {
		return ""
	}
	return " — did you mean " + strconv.Quote(best) + "?"
}

// editDistance is the Levenshtein distance between a and b, by rune: the
// fewest single-rune insertions, deletions and substitutions that turn one
// into the other.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
