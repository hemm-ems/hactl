package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// The plans of the multi-target writes (manual "Enforcement (writes)"): `ref
// replace` and `ent rename` (E-7, D-77), and the dash family (D-66 as
// refined). Every builder here runs only while a profile is in effect: with
// none, the commands send, print and log exactly what they did before (D-58).

// addChecks declares checks the request layer's own mapping computed
// (reqmap.RefReplaceChecks, reqmap.DiffDashboard), skipping one the plan
// already holds, so `checked` counts each (resource, verb) once.
func (p *writePlan) addChecks(cs []reqmap.Check) *writePlan {
	for _, c := range cs {
		dup := false
		for _, have := range p.checks {
			if have.String() == c.String() && have.KindWide == c.KindWide {
				dup = true
				break
			}
		}
		if !dup {
			p.checks = append(p.checks, c)
		}
	}
	return p
}

// addFailClosedEdit declares an edit the command had to check but cannot
// decide (D-89): it refuses the plan with its rule, line 0.
func (p *writePlan) addFailClosedEdit(resource, rule string) *writePlan {
	p.failClosed = append(p.failClosed, enforce.Denial{Resource: resource, Verb: policy.Edit.String(), Rule: rule})
	return p
}

// policyDashboardID is a dashboard's policy id: the default dashboard is
// `dashboard:` however it is named — "" or `lovelace` (D-66 as refined).
func policyDashboardID(urlPath string) string {
	if urlPath == defaultDashboardURLPath {
		return ""
	}
	return urlPath
}

// addDashboardSave declares a dashboard save the way the request layer maps
// it (reqmap.DiffDashboard): per view against the stored config — edit, create
// or delete dashboard:<u>/views/<path> — and edit dashboard:<u> for the rest,
// or for the whole save when the stored config could not be read (stored
// nil); a dashboard Home Assistant holds no config for comes as
// reqmap.EmptyDashboard (D-126). A save hactl cannot address (a view path the grammar refuses,
// a config that is not an object) is refused, never canonicalised.
func (p *writePlan) addDashboardSave(urlPath string, stored, proposed json.RawMessage) *writePlan {
	checks, err := reqmap.DiffDashboard(urlPath, stored, proposed)
	if err != nil {
		res := string(policy.KindDashboard) + ":" + policyDashboardID(urlPath)
		return p.addFailClosedEdit(res, "fail-closed: unmappable resource "+res+" ("+err.Error()+")")
	}
	return p.addChecks(checks)
}

// refCheckedPlan is a ref/replace rewrite whose plan was checked in this
// invocation: its changes, each placed in its typed owner (D-77, R-3), against
// which the confirmed write's answer is compared (A-7).
type refCheckedPlan struct {
	changes []reqmap.PlanChange
	// note is an allowed preview's line (checkRefReplacePlan).
	note *planPolicyNote
}

// addRefReplace declares a ref/replace rewrite (D-77): `edit file:<location>`
// for every file the companion's dry run would change; `edit
// <automation|script|template>:<id>/config` for every typed entry a change
// lies in (R-3), read from the file itself — a change that rewrites the
// entry's own id or unique_id is a rename, so also `create` on the new id
// (E-5); and every writable dashboard it rewrites, per view (D-66). A file
// whose entries cannot be placed (it cannot be read, or no longer holds the
// change's path) refuses the plan: `fail-closed: typed owner unknown`. The
// placed plan is recorded with the session, so the confirmed write of the same
// {old, new} is mapped through it at the request layer. With no profile in
// effect it reads nothing and returns nil.
func addRefReplace(ctx context.Context, p *writePlan, cc *companion.Client, oldVal, newVal string,
	changes []companion.RefChange, dashboards []dashReplacePlan) *refCheckedPlan {
	sess := policySession
	if sess == nil {
		return nil
	}
	placer := &refPlacer{cc: cc, files: map[string]refFile{}}
	var planned []reqmap.PlanChange
	for _, ch := range changes {
		bare := reqmap.PlanChange{Location: ch.Location, Path: ch.Path}
		owners, err := placer.owners(ctx, ch, oldVal, newVal)
		if err != nil {
			p.addFailClosedEdit(string(policy.KindFile)+":"+ch.Location,
				"fail-closed: typed owner unknown ("+err.Error()+")")
		}
		if len(owners) == 0 {
			planned = append(planned, bare)
		}
		for i := range owners {
			pc := bare
			pc.Owner = &owners[i]
			planned = append(planned, pc)
		}
	}
	for _, pc := range planned {
		checks, err := reqmap.RefReplaceChecks([]reqmap.PlanChange{pc})
		if err != nil {
			res := string(policy.KindFile) + ":" + pc.Location
			if pc.Owner != nil {
				res = string(pc.Owner.Kind) + ":" + pc.Owner.ID
			}
			p.addFailClosedEdit(res, "fail-closed: unmappable resource "+res+" ("+err.Error()+")")
			continue
		}
		p.addChecks(checks)
	}
	if sess.Policy().IndirectChecked() {
		p.addChecks(reqmap.RefReplaceIndirectChecks(planned, newVal))
	}
	sess.RecordRefReplacePlan(oldVal, newVal, planned)
	for _, d := range dashboards {
		if !d.writable {
			continue // never saved: an unwritable dashboard is refused or skipped, not written
		}
		stored, serr := json.Marshal(d.stored)
		proposed, perr := json.Marshal(d.result)
		if err := errors.Join(serr, perr); err != nil {
			res := string(policy.KindDashboard) + ":" + policyDashboardID(d.urlPath)
			p.addFailClosedEdit(res, "fail-closed: unmappable resource "+res+" ("+err.Error()+")")
			continue
		}
		p.addDashboardSave(d.urlPath, stored, proposed)
	}
	return &refCheckedPlan{changes: planned}
}

// refFile is one config file as the companion served it (raw), or why not.
type refFile struct {
	content []byte
	err     error
}

// refPlacer places ref/replace changes in their typed owners, reading each
// config file once per plan.
type refPlacer struct {
	cc    *companion.Client
	files map[string]refFile

	listed   bool
	every    map[string][]byte
	everyErr error
}

func (r *refPlacer) read(ctx context.Context, loc string) refFile {
	f, ok := r.files[loc]
	if !ok {
		resp, err := r.cc.ReadConfigFileRaw(ctx, loc)
		if err == nil {
			f = refFile{content: []byte(resp.Content)}
		} else {
			f = refFile{err: err}
		}
		r.files[loc] = f
	}
	return f
}

// allFiles is the instance's config files, read once, for the includes of a
// change's file (D-105): a fragment pulled into an automation, script or
// template entry by an `!include` lies in that entry. A listed file the
// companion refuses to read (4xx) is left out: the companion's walk of the
// include graph from configuration.yaml — which reached the change — never
// read it either, so it includes nothing the rewrite touches.
func (r *refPlacer) allFiles(ctx context.Context) (map[string][]byte, error) {
	if r.listed {
		return r.every, r.everyErr
	}
	r.listed = true
	list, err := r.cc.ListConfigFiles(ctx)
	if err != nil {
		r.everyErr = fmt.Errorf("listing the config files: %w", err)
		return nil, r.everyErr
	}
	r.every = map[string][]byte{}
	for _, name := range list.Files {
		f := r.read(ctx, name)
		if f.err != nil {
			if st, ok := haapi.HTTPStatus(f.err); ok && st >= 400 && st < 500 {
				continue
			}
			r.everyErr = fmt.Errorf("reading %s: %w", name, f.err)
			r.every = nil
			return nil, r.everyErr
		}
		r.every[name] = f.content
	}
	return r.every, nil
}

// owners are the typed entries a change lies in: by its file's shape and
// shared nodes (reqmap.RefChangeOwners, D-104) and through every file that
// includes it (reqmap.RefChangeIncluderOwners, D-105). An error means the
// owners cannot be known.
func (r *refPlacer) owners(ctx context.Context, ch companion.RefChange, oldVal, newVal string) ([]reqmap.TypedOwner, error) {
	f := r.read(ctx, ch.Location)
	if f.err != nil {
		return nil, f.err
	}
	owners, err := reqmap.RefChangeOwners(ch.Location, f.content, ch.Path, oldVal, newVal)
	if err != nil {
		return nil, err
	}
	all, err := r.allFiles(ctx)
	if err != nil {
		return owners, err
	}
	included, err := reqmap.RefChangeIncluderOwners(ch.Location, f.content, ch.Path, oldVal, newVal, all)
	for _, o := range included {
		if !slices.Contains(owners, o) {
			owners = append(owners, o)
		}
	}
	return owners, err
}

// checkRefReplacePlan declares a ref/replace rewrite (addRefReplace) as the
// whole plan of `ref replace` and checks it — in the preview (dryRun) and
// before a confirmed run's write. An allowed preview's note rides on the
// answer. With no profile it checks nothing and returns nil.
func checkRefReplacePlan(ctx context.Context, w io.Writer, cc *companion.Client, oldVal, newVal string,
	changes []companion.RefChange, dashboards []dashReplacePlan, dryRun bool) (*refCheckedPlan, error) {
	if policySession == nil {
		return nil, nil //nolint:nilnil // no profile in effect: nothing is checked (D-58)
	}
	plan := newWritePlan(refReplaceAction(oldVal, newVal))
	checked := addRefReplace(ctx, plan, cc, oldVal, newVal, changes, dashboards)
	note, err := checkWritePlan(w, dryRun, plan)
	if err != nil {
		return nil, err
	}
	checked.note = note
	return checked, nil
}

// writtenOutsidePlan compares a confirmed ref/replace answer with the plan
// checked before it (D-77, A-7): a file the write changed that the plan did
// not list fell into the documented window between the checked dry run and
// the write (R-4). It is the refusal reporting it after the fact — exit 1,
// policy_denied, each such file denied with `fail-closed: written outside the
// checked plan` and listed in `written` — or nil when everything written was
// checked (and always with no profile, or no checked plan).
func writtenOutsidePlan(action string, checked *refCheckedPlan, written []companion.RefChange) *enforce.DeniedError {
	sess := policySession
	if sess == nil || checked == nil {
		return nil
	}
	got := make([]reqmap.PlanChange, 0, len(written))
	for _, c := range written {
		got = append(got, reqmap.PlanChange{Location: c.Location, Path: c.Path})
	}
	return sess.WrittenOutsidePlan(action, reqmap.LocationsOutsidePlan(checked.changes, got))
}

// renderWrittenOutsidePlan renders that refusal (under --json, as the run's
// one document) and returns it.
func renderWrittenOutsidePlan(w io.Writer, ref *enforce.DeniedError) error {
	if err := renderPolicyRefusal(w, ref); err != nil {
		return err
	}
	return ref
}

// refReplaceAction is ref replace's plan action, as its preview names it.
func refReplaceAction(oldVal, newVal string) string {
	return fmt.Sprintf("rename %q to %q", oldVal, newVal)
}

// writePolicyNoteLine writes an allowed plan's line into a text report that is
// not a dryRunPlan (ref replace's table): nothing with no profile (D-58).
func writePolicyNoteLine(w io.Writer, note *planPolicyNote) {
	if note == nil || flagJSON {
		return
	}
	_, _ = fmt.Fprintf(w, "  policy: %s\n", note)
}
