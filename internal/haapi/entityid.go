package haapi

import "strings"

// ---------------------------------------------------------------------------
// What Home Assistant considers a valid entity_id.
//
// This is HA's own rule, not hactl's reading of it. `homeassistant/core.py`
// declares
//
//	VALID_ENTITY_ID = re.compile(r"^(?!.+__)(?!_)[\da-z_]+(?<!_)\.(?!_)[\da-z_]+(?<!_)$")
//
// and `valid_entity_id()` is the only thing that stands between a caller and
// HA's "Invalid entity ID" refusal. Go's RE2 has no lookaround, so the pattern
// cannot be transliterated; it is spelled out below clause by clause, and
// TestOracleEntityIDRule (internal/integration) runs a corpus through the
// running container's own regex and fails when the two disagree. A mirrored
// rule with no oracle is folklore one HA release later.
//
// It exists because two previews accepted identifiers the confirmed run could
// not use: `ent rename input_boolean.x 'input_boolean.pg w5 bad'` printed
// "would rename" at exit 0 while `config/entity_registry/update` answers
// "Invalid entity ID", and `svc call` echoed a malformed entity_id back inside
// --data while HA answers 400. H-2 makes those the same defect.
// ---------------------------------------------------------------------------

// ValidEntityID reports whether id is one Home Assistant will accept:
// <domain>.<object_id>, both slugs of [0-9a-z_], neither starting nor ending
// with an underscore, and no double underscore anywhere in the id.
func ValidEntityID(id string) bool {
	domain, object, found := strings.Cut(id, ".")
	if !found {
		return false
	}
	// `(?!.+__)` is anchored at the whole string, so a double underscore is
	// refused wherever it sits — including one straddling nothing but the
	// object id, which a per-segment check would let through.
	if strings.Contains(id, "__") {
		return false
	}
	return validSlug(domain) && validSlug(object)
}

// validSlug is one dot-separated half of the rule: non-empty, lowercase
// alphanumerics and underscores only, and no leading or trailing underscore.
// The scan is over bytes on purpose — a multi-byte character (an umlaut, an
// emoji) is invalid by the same clause that rejects an uppercase letter, and a
// rune-wise scan would have to say so twice.
func validSlug(s string) bool {
	if s == "" || s[0] == '_' || s[len(s)-1] == '_' {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// EntityIDDomain returns the domain half of an entity_id, or "" when id
// carries no dot at all. It answers the question HA's registry asks on a
// rename ("New entity ID should be same domain"), which is a different
// question from "is this id well-formed" — a caller that only compares
// domains would accept `Input_Boolean.X`, and one that only checks the shape
// would accept a rename across domains.
func EntityIDDomain(id string) string {
	domain, _, found := strings.Cut(id, ".")
	if !found {
		return ""
	}
	return domain
}
