package cmd

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// The plans of the typed write commands — automation, script, template and
// helper apply, create, delete and rollback (manual, Enforcement: "What each
// write command checks"; D-78). Each plan is built from what the command itself
// read — its candidate file, its resolved target, the backup it restores, the
// registry entries its cleanup removes — never from the request it is about to
// send, so the R-12 coverage test compares two independent statements of the
// same write. Every builder is pure: what it cannot address it carries as a
// fail-closed denial, never as an error, so with no profile in effect (where
// checkWritePlan checks nothing) building a plan changes nothing (D-58).

// Rules a typed plan refuses with when the write has no address (D-78). The
// first two are the request layer's own rules for the same requests
// (internal/policy/reqmap), so the plan and the request refuse alike.
const (
	ruleAutomationWithoutID       = "fail-closed: automation without a config id"
	ruleTemplateWithoutUniqueID   = "fail-closed: template entity without unique_id"
	ruleAutomationNoConfigID      = "fail-closed: automation has no config id"
	ruleCandidateUnreadable       = "fail-closed: candidate file unreadable"
	typedNoID                     = "(no id)"
	typedNoUniqueID               = "(no unique_id)"
	typedDeleteAutomationAction   = "delete automation"
	typedRollBackAutomationAction = "roll back automation"
)

// policyInEffect reports whether this invocation enforces a policy: a profile
// is in effect and its session runs. Work a plan needs that the command would
// not otherwise do — a registry read in a preview, a backup lookup before the
// companion is reached — happens only then (D-58).
func policyInEffect() bool { return policySession != nil }

// failClosedOn adds a pair the plan must check but cannot address: denied with
// rule, line 0 (D-89).
func (p *writePlan) failClosedOn(v policy.Verb, resource, rule string) *writePlan {
	p.failClosed = append(p.failClosed, enforce.Denial{Resource: resource, Verb: v.String(), Rule: rule})
	return p
}

// yamlTop decodes a YAML document that must be a mapping; nil otherwise.
func yamlTop(data []byte) map[string]any {
	var v any
	if yaml.Unmarshal(data, &v) != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// automationCandidateID is the `id:` of an automation body: a non-empty string,
// or not usable (missing, non-string, empty, or no mapping at all) — the same
// reading the request layer gives the body it is sent (R-6).
func automationCandidateID(data []byte) (string, bool) {
	id, ok := yamlTop(data)["id"].(string)
	return id, ok && id != ""
}

// addRenameOf declares the E-5 half of writing body over automation target:
// create on the body's id when it differs, or a fail-closed create when the
// body has no usable id (D-78, R-6).
func (p *writePlan) addRenameOf(target string, body []byte) {
	id, ok := automationCandidateID(body)
	switch {
	case !ok:
		p.failClosedOn(policy.Create, string(policy.KindAutomation)+":"+typedNoID, ruleAutomationWithoutID)
	case id != target:
		p.add(policy.Create, policy.KindAutomation, id, "")
	}
}

// readCandidate reads a candidate file for a plan; an unreadable one is
// carried as a fail-closed denial of the plan's main pair.
func readCandidate(p *writePlan, v policy.Verb, resource, path string) ([]byte, bool) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		p.failClosedOn(v, resource, ruleCandidateUnreadable)
		return nil, false
	}
	return data, true
}

// autoApplyPlan: edit automation:<config id>/config, + create
// automation:<file id> when the file's id differs (E-5, D-78), or refused when
// the file has no usable id (R-6).
func autoApplyPlan(configID, file string) *writePlan {
	p := newWritePlan("apply automation").add(policy.Edit, policy.KindAutomation, configID, "config")
	if data, ok := readCandidate(p, policy.Create, string(policy.KindAutomation)+":"+typedNoID, file); ok {
		p.addRenameOf(configID, data)
	}
	return p
}

// autoCreatePlan: create automation:<the file's id>; refused without one.
func autoCreatePlan(data []byte) *writePlan {
	p := newWritePlan("create automation")
	if id, ok := automationCandidateID(data); ok {
		return p.add(policy.Create, policy.KindAutomation, id, "")
	}
	return p.failClosedOn(policy.Create, string(policy.KindAutomation)+":"+typedNoID, ruleAutomationWithoutID)
}

// autoRollbackPlan: edit automation:<target>/config with the backup's own
// bytes, + create on the backup's id when it differs; refused when the backup
// has no usable id (D-78).
func autoRollbackPlan(target, backupPath string) *writePlan {
	p := newWritePlan(typedRollBackAutomationAction).add(policy.Edit, policy.KindAutomation, target, "config")
	if data, ok := readCandidate(p, policy.Create, string(policy.KindAutomation)+":"+typedNoID, backupPath); ok {
		p.addRenameOf(target, data)
	}
	return p
}

// autoDeletePlan: delete automation:<config id> + delete entity:<live entity>
// (the cleanup after the delete). configID is "" when the companion's GET did
// not answer with the very id hactl will send — an automation without a config
// id, found by alias or entity id — which is refused (D-78): the companion
// would fall back to an alias or an entity id no automation:<id> rule names.
func autoDeletePlan(configID, liveEntityID string) *writePlan {
	p := newWritePlan(typedDeleteAutomationAction)
	if configID == "" {
		p.failClosedOn(policy.Delete, string(policy.KindAutomation)+":"+typedNoID, ruleAutomationNoConfigID)
	} else {
		p.add(policy.Delete, policy.KindAutomation, configID, "")
	}
	if liveEntityID != "" {
		p.add(policy.Delete, policy.KindEntity, liveEntityID, "")
	}
	return p
}

// scriptApplyPlan: edit script:<id>/config + the reload hactl sends itself
// after the write (`control service:script.reload`, E-2, D-73).
func scriptApplyPlan(id string) *writePlan {
	return newWritePlan("apply script").add(policy.Edit, policy.KindScript, id, "config")
}

// scriptCreatePlan: create script:<the file's single top-level key>, as
// written — the companion keys the new script by it verbatim.
func scriptCreatePlan(content string) *writePlan {
	p := newWritePlan("create script")
	top := yamlTop([]byte(content))
	if len(top) == 1 {
		for key := range top {
			return p.add(policy.Create, policy.KindScript, key, "")
		}
	}
	return p.failClosedOn(policy.Create, string(policy.KindScript)+":"+typedNoID, "fail-closed: script file without a single top-level id")
}

// deletePlan: delete <kind>:<id> + delete entity:<e> for each registry entry
// the command removes after the delete (orphan cleanup, D-78).
func deletePlan(action string, kind policy.Kind, id string, orphans ...string) *writePlan {
	p := newWritePlan(action).add(policy.Delete, kind, id, "")
	for _, e := range orphans {
		if e != "" {
			p.add(policy.Delete, policy.KindEntity, e, "")
		}
	}
	return p
}

// tplCreatePlan: create template:<uid> for every unique_id the file declares —
// a bare entity item, or each item of each entity-domain list of a full block.
// An entity without a string unique_id has no address: the create is refused
// (D-78).
func tplCreatePlan(content string) *writePlan {
	p := newWritePlan("create template")
	top := yamlTop([]byte(content))
	items := []any{top}
	block := false
	for _, d := range tplEntityDomains {
		v, ok := top[d]
		if !ok {
			continue
		}
		if !block {
			items, block = nil, true
		}
		list, isList := v.([]any)
		if !isList {
			list = []any{nil}
		}
		items = append(items, list...)
	}
	refused := false
	for _, it := range items {
		entity, _ := it.(map[string]any)
		uid, ok := entity["unique_id"].(string)
		if !ok || uid == "" {
			refused = true
			continue
		}
		p.add(policy.Create, policy.KindTemplate, uid, "")
	}
	if refused {
		p.failClosedOn(policy.Create, string(policy.KindTemplate)+":"+typedNoUniqueID, ruleTemplateWithoutUniqueID)
	}
	return p
}

// helperCreatePlan: create helper:<domain>/<id>.
func helperCreatePlan(domain, id string) *writePlan {
	return newWritePlan("create helper").add(policy.Create, policy.KindHelper, domain+"/"+id, "")
}
