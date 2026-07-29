package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// This file is where INVARIANT H-21 lives: a listing decodes only the entities
// it lists.
//
// `/api/states` is every entity in the instance. A command that renders one
// domain — `auto ls`, `script ls` — used to decode the whole payload into its
// own domain-typed attribute struct and filter to `automation.`/`script.`
// afterwards, so an entity it discards had already been forced through a schema
// it was never described by. A live instance (HA 2026.7.4) reported the
// consequence:
//
//	parsing states: json: cannot unmarshal number -1.7525 into Go struct field
//	automationAttributes.attributes.current of type int
//
// The ordering is inverted here: split the payload, read the identity, filter,
// then decode only the survivors' attributes. The typed attribute schema is
// kept — a command may impose one on the entities it actually renders; what it
// may not do is impose one on entities it discards.

// statesEnvelope is one `/api/states` record with its attributes left RAW.
//
// It is the shape every domain listing decodes the whole payload into, and it
// is deliberately the *only* shape that ever sees the whole payload: nothing
// here can collide with another integration's attribute types, because nothing
// here has an opinion about them.
type statesEnvelope struct {
	EntityID   string          `json:"entity_id"`
	State      string          `json:"state"`
	Attributes json.RawMessage `json:"attributes"`
}

// Identity reports the entity key and its state — the same pair entityState
// declares (H-14). It sits on the envelope rather than on the domain structs so
// the degeneracy check keeps quantifying over the WHOLE payload after the
// filter moved earlier: a payload that lost `entity_id` matches no domain
// prefix, so a filter-first listing that checked only its survivors would
// answer "no automations found" at exit 0 — an unavailable source rendered as a
// confident negative, which is the H-7 failure this check exists to stop.
func (s *statesEnvelope) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entity_id", Value: &s.EntityID},
		{Name: "state", Value: &s.State},
	}
}

// fetchDomainStates reads `/api/states`, checks the whole payload for
// degeneracy, and returns only the records whose entity_id carries prefix —
// attributes still raw, so no domain schema has been applied to anything yet.
func fetchDomainStates(ctx context.Context, client *haapi.Client, prefix string) ([]statesEnvelope, error) {
	data, err := client.GetStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching states: %w", err)
	}

	var all []statesEnvelope
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("parsing states: %w", err)
	}
	// H-14, over every record the instance sent — not over the filtered subset.
	if err := degeneracy.Check("/api/states", &all); err != nil {
		return nil, err
	}

	out := make([]statesEnvelope, 0, len(all))
	for _, s := range all {
		if strings.HasPrefix(s.EntityID, prefix) {
			out = append(out, s)
		}
	}
	return out, nil
}

// decodeStateAttributes decodes one record's attributes into the command's own
// attribute schema, naming the record and the key when they do not fit.
//
// The naming is the point (SPEC §9, layer 0). `encoding/json` cannot name a
// slice element: decoding the whole payload at once produced "cannot unmarshal
// number -1.7525 into Go struct field automationAttributes.attributes.current
// of type int", which names hactl's Go type and not the entity — so diagnosing
// the report above meant reading this repository's source, against an instance
// nobody here can reach. Decoding per entity means the loop knows the id, and
// the next report of this class arrives with its own diagnosis inside it.
func decodeStateAttributes(e statesEnvelope, attrs any) error {
	if len(e.Attributes) == 0 {
		return nil
	}
	if err := json.Unmarshal(e.Attributes, attrs); err != nil {
		return fmt.Errorf("entity %s: %s", e.EntityID, attributeDecodeDetail(err))
	}
	return nil
}

// attributeDecodeDetail renders a failed attribute decode as "attributes.<key>:
// <what went wrong>", replacing encoding/json's Go-type-centric phrasing with
// the wire key the reader can look up in their own instance.
func attributeDecodeDetail(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return fmt.Sprintf("attributes.%s: cannot unmarshal %s into Go value of type %s",
			typeErr.Field, typeErr.Value, typeErr.Type)
	}
	return fmt.Sprintf("attributes: %v", err)
}
