package realdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Storage-backed helper collections
// ---------------------------------------------------------------------------
//
// The sharpest single number in SPEC-realdata-fixture.md: the reference
// instance has 220 storage-backed helpers and the rig had zero, with no
// `.storage/<domain>` file at all. The whole `helper show` 404 family (#27 #28
// #29 #63 #64 #65 #86 #97) was therefore fixed and verified against a real
// house only, and #104 was found by a capture rather than by a test.
//
// What Home Assistant accepts here was probed rather than assumed
// (SPEC §6 Q1). The envelope below comes up as real entities on both the
// `stable` and the previous image. The probe's second answer is the one that
// shaped this file: HA ACCEPTING an item is not the same as the entity working.
// A `counter` item without `minimum`/`maximum` passed the storage schema, was
// registered, and then raised `KeyError: 'minimum'` on every state write — the
// entity existed and read `unavailable`. Both keys are absent-not-invalid, so
// nothing rejected them.
//
// Hence [requiredKeys]: every domain declares the full set of keys its item
// dict must carry, and [StorageCollections] refuses a capture that cannot fill
// them rather than emitting a helper that boots broken. That is S4's "refuses
// rather than silently degrading", and it is the same rule as H-14 one layer
// out — a missing field is not a zero, it is a missing field.

// HelperItem is one captured storage-backed helper, already projected out of
// whatever read produced it.
//
// Config carries the domain-specific keys as Home Assistant reports them in the
// entity's attributes (min/max/step for an input_number, options for an
// input_select, the weekday blocks for a schedule). Nothing here interprets
// them; the generator's job is to carry the shape, not to understand it.
type HelperItem struct {
	Domain string
	ID     string // the storage collection's item id, i.e. the entity object_id
	Name   string
	Icon   string
	Config map[string]any
}

// requiredKeys is what each domain's item dict must contain for the entity to
// come up working, as established by the SPEC §6 Q1 probe.
//
// `counter` is listed although the reference instance uses none: the probe
// failed on exactly that domain, and dropping it from this table because the
// current capture does not exercise it is how the lesson would be lost the day
// somebody creates one.
var requiredKeys = map[string][]string{
	"input_boolean":  {},
	"input_button":   {},
	"input_number":   {"min", "max"},
	"input_text":     {"min", "max"},
	"input_select":   {"options"},
	"input_datetime": {"has_date", "has_time"},
	"timer":          {"duration"},
	// A schedule's weekday keys are what a schedule IS. They are absent from
	// the entity's state attributes — HA publishes `next_event` there, not the
	// blocks — so a capture that projected attributes alone wrote 13 schedules
	// carrying nothing, and the generator said yes. That is precisely the
	// silent degradation S4 forbids, so the keys are required here and the
	// capture has to go and get them. Empty lists are fine; absent is not.
	"schedule": {"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
	"counter":  {"minimum", "maximum", "step", "initial"},
}

// carriedAttributes names, per domain, the attribute keys copied from the wire
// into the stored item.
//
// It is a list per domain rather than "copy everything" because HA's state
// attributes also carry RUNTIME values — `editable`, `friendly_name`,
// `next_event` on a schedule — and a stored item containing those is not a
// document HA would ever have written. The fixture has to be a plausible
// `.storage` file, not a transcript of a state.
var carriedAttributes = map[string][]string{
	"input_number":   {"min", "max", "step", "mode", "unit_of_measurement", "initial"},
	"input_text":     {"min", "max", "mode", "pattern"},
	"input_select":   {"options"},
	"input_datetime": {"has_date", "has_time"},
	"timer":          {"duration", "restore"},
	"counter":        {"minimum", "maximum", "step", "initial", "restore"},
	"schedule": {"monday", "tuesday", "wednesday", "thursday", "friday",
		"saturday", "sunday"},
}

// CarriedAttributes returns the attribute keys a domain contributes to its
// stored item, for a capture to project with.
func CarriedAttributes(domain string) []string { return carriedAttributes[domain] }

// ErrNoHelpers is returned when the capture holds none.
//
// An empty capture must not silently produce an empty `.storage` — that is the
// green-by-construction failure this whole package exists to end, arriving
// through the generator instead of through the fixture.
var ErrNoHelpers = errors.New("the capture holds no storage-backed helpers, so the fixture would carry the same zero the rig already had")

// StorageCollections turns captured helpers into `.storage/<domain>` documents,
// sanitized and keyed by the file name each must be written to.
//
// It also returns the id mapping, because a helper's identifier travels: the
// entity registry references it, and so does every automation that mentions the
// entity. A caller that sanitized those separately would produce a fixture
// whose parts do not refer to each other.
func StorageCollections(items []HelperItem, s *Sanitizer) (files map[string][]byte, renamed map[string]string, err error) {
	if len(items) == 0 {
		return nil, nil, ErrNoHelpers
	}

	byDomain := map[string][]map[string]any{}
	renamed = map[string]string{}

	for _, item := range items {
		if _, known := requiredKeys[item.Domain]; !known {
			return nil, nil, fmt.Errorf("helper %s.%s: %q is not a helper domain this generator knows how to write",
				item.Domain, item.ID, item.Domain)
		}
		id := s.Identifier(item.ID)
		renamed[item.Domain+"."+item.ID] = item.Domain + "." + id

		stored := map[string]any{"id": id, "name": s.Name(item.Name)}
		if item.Icon != "" {
			stored["icon"] = s.Icon(item.Icon)
		}
		for _, key := range carriedAttributes[item.Domain] {
			if value, present := item.Config[key]; present {
				stored[key] = sanitizeConfigValue(key, value, s)
			}
		}
		for _, key := range requiredKeys[item.Domain] {
			if _, present := stored[key]; !present {
				// Refuse rather than default. The probe's own failure was a
				// missing key that HA accepted and then crashed on, so a
				// generator that filled one in would be reproducing the defect
				// it was written after.
				return nil, nil, fmt.Errorf(
					"helper %s.%s: the capture has no %q, which this domain's entity reads on every state write — "+
						"a helper written without it comes up `unavailable` (SPEC-realdata-fixture.md §6 Q1)",
					item.Domain, item.ID, key)
			}
		}
		byDomain[item.Domain] = append(byDomain[item.Domain], stored)
	}

	files = make(map[string][]byte, len(byDomain))
	for domain, stored := range byDomain {
		sort.Slice(stored, func(i, j int) bool {
			return fmt.Sprint(stored[i]["id"]) < fmt.Sprint(stored[j]["id"])
		})
		doc := map[string]any{
			"version":       1,
			"minor_version": 1,
			"key":           domain,
			"data":          map[string]any{"items": stored},
		}
		encoded, marshalErr := json.MarshalIndent(doc, "", "  ")
		if marshalErr != nil {
			return nil, nil, fmt.Errorf("encoding .storage/%s: %w", domain, marshalErr)
		}
		files[domain] = append(encoded, '\n')
	}
	return files, renamed, nil
}

// sanitizeConfigValue replaces the human-authored content inside a helper's
// own config. Numbers, booleans and times are structure and pass through.
func sanitizeConfigValue(key string, value any, s *Sanitizer) any {
	if key != "options" {
		return value
	}
	// input_select options are user-authored strings and the reference
	// instance's include people's names ("Sender für Lasse" has options that
	// name radio stations, but the same field elsewhere names family members).
	list, isList := value.([]any)
	if !isList {
		return value
	}
	out := make([]any, 0, len(list))
	for i, opt := range list {
		text, isText := opt.(string)
		if !isText {
			out = append(out, opt)
			continue
		}
		out = append(out, s.Name(fmt.Sprintf("option:%d:%s", i, text)))
	}
	return out
}

// HelperDomains lists the domains this generator can write, sorted, for a
// caller that needs to ask an instance for exactly those.
func HelperDomains() []string {
	out := make([]string, 0, len(requiredKeys))
	for domain := range requiredKeys {
		out = append(out, domain)
	}
	sort.Strings(out)
	return out
}

// IsHelperDomain reports whether a domain holds helpers.
func IsHelperDomain(domain string) bool {
	_, ok := requiredKeys[domain]
	return ok
}

// EntityDomain splits an entity_id and returns its domain.
func EntityDomain(entityID string) string {
	domain, _, _ := strings.Cut(entityID, ".")
	return domain
}
