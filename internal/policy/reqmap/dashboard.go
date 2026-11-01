package reqmap

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/hemm-ems/hactl/internal/policy"
)

// DiffDashboard maps a dashboard save to the finest addresses it touches, by
// comparing the proposed config with the stored one the same invocation read
// (D-66 as refined, D-72):
//
//   - a view whose `path` is in both and whose content changed: edit
//     dashboard:<u>/views/<path>; only in the proposal: create; only in the
//     stored config: delete;
//   - a change outside `views`, a changed, added or removed view without a
//     `path`, a reordering of views, a `views` that is not a list, or two
//     views sharing a path: edit dashboard:<u> as a whole — which any view
//     deny for edit makes partial, hence denied (D-66). Path-less views are
//     never addressed by index (a view whose path is `0` would collide);
//   - stored == nil (none was read, or it could not be): edit
//     dashboard:<u>. A dashboard Home Assistant holds no config for is not
//     this case: its caller passes EmptyDashboard (D-126);
//   - a stored config with no key (EmptyDashboard, or an empty object Home
//     Assistant holds — the two cannot be told apart here) and a proposal
//     that otherwise needs no check (`{}`, an empty or null `views`): edit
//     dashboard:<u> — storing a config where none was is a change (D-126).
//
// urlPath is canonicalised first (`lovelace` is the default dashboard,
// `dashboard:`). A view path the policy grammar refuses is an error: the
// request is unmappable, never canonicalised. Otherwise identical configs need
// no check.
func DiffDashboard(urlPath string, stored, proposed json.RawMessage) ([]Check, error) {
	u := canonicalDashboard(urlPath)
	whole, err := check(policy.Edit, policy.KindDashboard, u, "")
	if err != nil {
		return nil, err
	}
	next, err := decodeObject(proposed)
	if err != nil {
		return nil, fmt.Errorf("proposed config: %w", err)
	}
	if len(stored) == 0 || string(stored) == "null" {
		return []Check{whole}, nil
	}
	prev, err := decodeObject(stored)
	if err != nil {
		return []Check{whole}, nil //nolint:nilerr // an unreadable stored config is no basis for a diff: the save is checked whole
	}

	set := newCheckSet()
	if !reflect.DeepEqual(withoutViews(prev), withoutViews(next)) {
		set.add(whole)
	}
	pv, okPrev := viewsOf(prev)
	nv, okNext := viewsOf(next)
	if !okPrev || !okNext {
		set.add(whole)
		return set.list(), nil
	}
	pIdx, pDup := keyed(pv)
	nIdx, nDup := keyed(nv)
	if pDup || nDup {
		set.add(whole)
		return set.list(), nil
	}
	if !reflect.DeepEqual(pathless(pv), pathless(nv)) || reordered(pv, nv, pIdx, nIdx) {
		set.add(whole)
	}
	for p, v := range nIdx {
		old, ok := pIdx[p]
		switch {
		case !ok:
			if err := addView(set, policy.Create, u, p); err != nil {
				return nil, err
			}
		case !reflect.DeepEqual(old, v):
			if err := addView(set, policy.Edit, u, p); err != nil {
				return nil, err
			}
		}
	}
	for p := range pIdx {
		if _, ok := nIdx[p]; !ok {
			if err := addView(set, policy.Delete, u, p); err != nil {
				return nil, err
			}
		}
	}
	if len(prev) == 0 && len(set.m) == 0 {
		// Nothing stored, and the proposal brings no view to create: the
		// save still turns "nothing stored" into a stored dashboard (on the
		// auto-generated default dashboard it replaces what users see), so
		// it is the whole dashboard, never zero checks (D-126).
		set.add(whole)
	}
	return set.list(), nil
}

// EmptyDashboard is the stored config of a dashboard Home Assistant answers
// `config_not_found` for — its authoritative "nothing is stored" (D-126): a
// save is diffed against it, so each view the first save brings is a create
// and any other top-level key is an edit of the dashboard. An unreadable
// config is never this: it is nil, and the save is checked whole.
var EmptyDashboard = json.RawMessage(`{}`)

func addView(set *checkSet, v policy.Verb, u, path string) error {
	ch, err := check(v, policy.KindDashboard, u, "views/"+path)
	if err != nil {
		return err
	}
	set.add(ch)
	return nil
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("a dashboard config is a JSON object, not %T", v)
	}
	return m, nil
}

func withoutViews(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != "views" {
			out[k] = v
		}
	}
	return out
}

// viewsOf returns the views list; an absent `views` is an empty list, any
// other non-list value is not a list of views.
func viewsOf(m map[string]any) ([]any, bool) {
	v, ok := m["views"]
	if !ok || v == nil {
		return nil, true
	}
	l, isList := v.([]any)
	return l, isList
}

// viewPath is a view's `path` when it is a non-empty string.
func viewPath(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	p, ok := m["path"].(string)
	return p, ok && p != ""
}

// keyed indexes the views that have a path; dup reports two views sharing one.
func keyed(views []any) (map[string]any, bool) {
	idx := map[string]any{}
	for _, v := range views {
		p, ok := viewPath(v)
		if !ok {
			continue
		}
		if _, seen := idx[p]; seen {
			return nil, true
		}
		idx[p] = v
	}
	return idx, false
}

func pathless(views []any) []any {
	var out []any
	for _, v := range views {
		if _, ok := viewPath(v); !ok {
			out = append(out, v)
		}
	}
	return out
}

// reordered reports whether the views both configs share changed their
// relative order (a path-less view counts as a placeholder in the sequence).
func reordered(pv, nv []any, pIdx, nIdx map[string]any) bool {
	seq := func(views []any, other map[string]any) []string {
		var out []string
		for _, v := range views {
			p, ok := viewPath(v)
			if !ok {
				out = append(out, "")
				continue
			}
			if _, both := other[p]; both {
				out = append(out, "/"+p)
			}
		}
		return out
	}
	return !slices.Equal(seq(pv, nIdx), seq(nv, pIdx))
}
