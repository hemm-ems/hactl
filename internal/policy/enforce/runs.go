package enforce

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// RuleStoredConfigUnreadable refuses a call that starts a script or runs an
// automation whose stored config cannot be read (D-207).
const RuleStoredConfigUnreadable = "fail-closed: stored config unreadable"

// StoredConfigFetcher is a Fetcher that can read a script's or an
// automation's stored config, as `script cat` and `auto cat` read it (D-207):
// kind "script" by its config key, kind "automation" by its config id or
// entity id. The answer is the config's YAML text.
type StoredConfigFetcher interface {
	StoredConfig(ctx context.Context, kind, id string) ([]byte, error)
}

// runChainKey carries the runs being expanded, for the cycle guard.
type runChainKey struct{}

func runChain(ctx context.Context) []string {
	chain, _ := ctx.Value(runChainKey{}).([]string)
	return chain
}

func withRun(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, runChainKey{}, append(slices.Clone(runChain(ctx)), key))
}

// runChecks is what the scripts and automations a service call starts do
// (D-207): each one's stored config is checked as a candidate is (E-3, D-133,
// D-198, D-202, D-185), every check carrying its run path. A script the
// config starts is expanded the same way, through the service call it is; a
// run already on the chain is not expanded again. A config that cannot be
// read is a denial (RuleStoredConfigUnreadable). Under `indirect:
// unchecked`, or in a profile that denies nothing, nothing is added.
func (s *Session) runChecks(ctx context.Context, runs []reqmap.Run) ([]reqmap.Check, []Denial) {
	if len(runs) == 0 || !s.pol.IndirectChecked() || !DeniesAnything(s.pol) {
		return nil, nil
	}
	chain := runChain(ctx)
	var checks []reqmap.Check
	var denials []Denial
	for _, r := range runs {
		key := r.Kind + ":" + r.ID
		if r.ID == "" {
			key = r.Kind + ":" + r.EntityID
		}
		if slices.Contains(chain, key) {
			continue // already being expanded: a cycle
		}
		name := "run of " + r.Name()
		doc, err := s.storedConfig(ctx, r)
		if err != nil {
			resource := key
			if r.EntityID != "" {
				resource = "entity:" + r.EntityID
			}
			denials = append(denials, Denial{Resource: resource, Verb: policy.Control.String(),
				Rule: RuleStoredConfigUnreadable, Via: name + ": " + err.Error()})
			continue
		}
		cs, ds := s.candidateDoc(withRun(ctx, key), r.Kind, doc, func(path string) string {
			if path == "" {
				return name
			}
			return name + ": " + path
		})
		checks = append(checks, cs...)
		denials = append(denials, ds...)
	}
	return checks, denials
}

// storedConfig reads and parses a run's stored config, as the session's own
// read.
func (s *Session) storedConfig(ctx context.Context, r reqmap.Run) (any, error) {
	sf, ok := s.fetcher.(StoredConfigFetcher)
	if !ok || s.fetcher == nil {
		return nil, errors.New("stored configs cannot be read in this invocation")
	}
	if r.ID == "" {
		return nil, fmt.Errorf("the entity registry holds no config key for %s", r.EntityID)
	}
	body, err := sf.StoredConfig(own(ctx), r.Kind, r.ID)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := yaml.Unmarshal(body, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("%s %s: the stored config does not parse", r.Kind, r.ID)
	}
	return doc, nil
}
