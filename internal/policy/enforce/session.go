package enforce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// Recorder sees every check a session makes, for the coverage test that a
// confirmed run's request-layer verdicts are a subset of its plan (R-12,
// D-80). Production code installs none.
type Recorder interface {
	// PlanChecked is called with a plan's checks, allowed or not.
	PlanChecked(checks []reqmap.Check)
	// RequestChecked is called with the checks of each write request the
	// request layer decided, allowed or not.
	RequestChecked(req reqmap.Request, checks []reqmap.Check)
}

// Options configure a session.
type Options struct {
	// Policy is the loaded policy; a session is only made while a profile is
	// in effect (D-74), and New refuses a policy without one.
	Policy *policy.Policy
	// InstanceDir is the instance the policy belongs to; the audit log is
	// `<InstanceDir>/cache/policy-audit.log`.
	InstanceDir string
	// Command is the command path and its positional arguments, as the audit
	// log names it (D-71).
	Command string
	// DryRun is whether the invocation is a preview: the command has a
	// --confirm flag and it is not set.
	DryRun bool
	// Fetcher answers the questions mappings and selectors ask. Nil means
	// nothing can be asked: every such question fails closed.
	Fetcher Fetcher
	// Recorder, when set, sees every check (tests only).
	Recorder Recorder
	// Now is the clock of the audit log's `time` (time.Now when nil).
	Now func() time.Time
}

// Session enforces one policy for one command invocation (H-29): it decides
// every request the clients send (it is the reqgate.Gate) and every plan a
// write command declares, and writes the audit log. Nothing it verifies
// outlives the invocation or the policy's content hash (D-72, R-7).
type Session struct {
	pol      *policy.Policy
	command  string
	dryRun   bool
	audit    *Audit
	fetcher  Fetcher
	recorder Recorder
	now      func() time.Time

	mu      sync.Mutex
	cache   *factCache
	written []string
	refused *DeniedError
	planned bool
}

// New starts a session. It refuses a policy that puts no profile in effect:
// with no file or the implicit allow-all there is nothing to enforce and no
// session may exist (D-58).
func New(opts Options) (*Session, error) {
	if opts.Policy == nil || !opts.Policy.InEffect() {
		return nil, errors.New("enforce: no profile is in effect — there is nothing to enforce")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Session{
		pol:      opts.Policy,
		command:  opts.Command,
		dryRun:   opts.DryRun,
		audit:    NewAudit(opts.InstanceDir),
		fetcher:  opts.Fetcher,
		recorder: opts.Recorder,
		now:      now,
		cache:    newFactCache(opts.Policy.Hash()),
	}, nil
}

// Policy is the policy the session enforces.
func (s *Session) Policy() *policy.Policy { return s.pol }

// Close releases the fetcher.
func (s *Session) Close() {
	if s.fetcher != nil {
		s.fetcher.Close()
	}
}

// RefusedMidRun is the first request-layer refusal of the invocation, or nil.
// A command that swallowed it (a best-effort cleanup) still ends refused: a
// request refused under a policy is an error, never a log line (H-29).
func (s *Session) RefusedMidRun() *DeniedError {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

// --- the command layer ---------------------------------------------------------

// Plan is a write command's plan: every (resource, verb) its confirmed run
// touches, follow-up writes included, in the order the report lists them.
type Plan struct {
	// Action completes "would …" (`create label`, `delete automation`).
	Action string
	Checks []reqmap.Check
	// FailClosed are pairs the command had to check but could not decide —
	// an address the policy grammar refuses (`fail-closed: unmappable
	// resource <address>`, D-67..D-69, never canonicalised), a service call
	// whose reach Home Assistant did not answer (`fail-closed: reach
	// unknown`, D-73). Each is denied with its Rule, line 0.
	FailClosed []Denial
}

// PlanResult is an allowed plan's summary for the preview: `policy: <n>
// checked, allowed (profile <p>)`.
type PlanResult struct {
	Checked    int
	Profile    string
	SelectedBy policy.Selection
}

// CheckPlan decides a write command's plan before its first write — in the
// preview and again under --confirm (H-29's command layer, H-2's parity).
// Every check is decided and written to the audit log; any denial refuses the
// whole plan (E-7) with a *DeniedError listing every denied pair in plan order. A
// partial verdict denies (D-66). An audit log that cannot be written refuses
// the plan (D-71). previewPassed marks a refused --confirm whose preview had
// passed (D-80).
func (s *Session) CheckPlan(p Plan, dryRun, previewPassed bool) (PlanResult, error) {
	if s.recorder != nil {
		s.recorder.PlanChecked(append([]reqmap.Check(nil), p.Checks...))
	}
	ref := s.newDenial(LayerCommand, dryRun, p.Action)
	ref.Checked = len(p.Checks)
	ref.PreviewPassed = previewPassed && !dryRun
	lines := make([]AuditLine, 0, len(p.Checks))
	for _, c := range p.Checks {
		v := s.decide(c)
		lines = append(lines, s.verdictLine(LayerCommand, dryRun, c, v))
		if !v.Allowed {
			ref.Denied = append(ref.Denied, denial(c, v))
		}
	}
	for _, d := range p.FailClosed {
		ref.Checked++
		lines = append(lines, s.line(LayerCommand, dryRun, d.Resource, d.Verb, false, d.Rule, 0))
		d.Line = 0
		ref.Denied = append(ref.Denied, d)
	}
	if err := s.audit.Append(lines); err != nil {
		ref.Denied = ref.Denied[:0]
		for _, c := range p.Checks {
			ref.Denied = append(ref.Denied, Denial{Resource: c.Target(), Verb: c.Verb.String(), Rule: RuleAuditUnwritable, KindWide: c.KindWide})
		}
		for _, d := range p.FailClosed {
			ref.Denied = append(ref.Denied, Denial{Resource: d.Resource, Verb: d.Verb, Rule: RuleAuditUnwritable, KindWide: d.KindWide})
		}
		return PlanResult{}, ref
	}
	s.mu.Lock()
	s.planned = true
	s.mu.Unlock()
	if len(ref.Denied) > 0 {
		return PlanResult{}, ref
	}
	return PlanResult{Checked: len(p.Checks), Profile: s.pol.Active(), SelectedBy: s.pol.Selection()}, nil
}

// ServiceCallChecks is what a service call needs, computed exactly as the
// request layer maps the call when it is sent — `control service:<d>.<s>`
// always (E-2), `control` on every entity its target reaches (asked of Home
// Assistant, D-59 as refined, R-1) and every entity its data names (D-73), the
// per-domain services `homeassistant.turn_on|turn_off|toggle` fan out into and
// the `script:<id>/run` of every script it starts (R-2) — so the svc family's
// plan holds the request layer's verdicts (R-12). data is the service data as
// it will be sent (nil = `{}`). When the reach cannot be computed the call
// cannot be decided: the answer is the fail-closed denial instead of checks
// (`fail-closed: reach unknown`), for the plan to carry.
func (s *Session) ServiceCallChecks(ctx context.Context, domain, service string, data any) ([]reqmap.Check, *Denial) {
	if data == nil {
		data = map[string]any{}
	}
	req := reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/" + url.PathEscape(domain) + "/" + url.PathEscape(service)}
	body, err := json.Marshal(data)
	if err != nil {
		return nil, &Denial{Resource: describe(req), Verb: policy.Control.String(), Rule: "fail-closed: service data does not encode: " + err.Error()}
	}
	req.Body = body
	res := s.classify(ctx, req)
	if res.Disposition == reqmap.Write {
		return res.Checks, nil
	}
	rule := res.Rule
	if rule == "" {
		rule = "fail-closed: unmapped request " + describe(req)
	}
	return nil, &Denial{Resource: describe(req), Verb: policy.Control.String(), Rule: rule}
}

// RecordRefReplacePlan records a ref/replace dry run whose changes the command
// resolved to their typed owners (D-77, R-3), so the confirmed write of the
// same {old, new} in this invocation is mapped through it. It is tagged with
// the session's policy hash.
func (s *Session) RecordRefReplacePlan(oldVal, newVal string, changes []reqmap.PlanChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = s.cache.forHash(s.pol.Hash())
	s.cache.refPlans = append(s.cache.refPlans, reqmap.RefReplacePlan{
		Old: oldVal, New: newVal, PolicyHash: s.pol.Hash(), Changes: append([]reqmap.PlanChange(nil), changes...),
	})
}

// --- the request layer (reqgate.Gate) ------------------------------------------

// Check decides one logical request before it is sent (D-74): a write is
// allowed only if every check of its mapping is; an unmapped or unmappable
// request that can change anything is refused; a read, an exempt request and
// an unmapped GET pass. Every write decision is written to the audit log.
func (s *Session) Check(ctx context.Context, req reqmap.Request) error {
	res := s.classify(ctx, req)
	switch {
	case res.Refused():
		ref := s.newDenial(LayerRequest, s.dryRun, describe(req))
		ref.Denied = []Denial{{Resource: describe(req), Rule: res.Rule}}
		line := s.line(LayerRequest, s.dryRun, describe(req), "", false, res.Rule, 0)
		_ = s.audit.Append([]AuditLine{line}) // refused either way
		return s.refuse(ref)
	case res.Disposition != reqmap.Write:
		return nil
	}
	if s.recorder != nil {
		s.recorder.RequestChecked(req, append([]reqmap.Check(nil), res.Checks...))
	}
	ref := s.newDenial(LayerRequest, s.dryRun, describe(req))
	ref.Checked = len(res.Checks)
	lines := make([]AuditLine, 0, len(res.Checks))
	for _, c := range res.Checks {
		v := s.decide(c)
		lines = append(lines, s.verdictLine(LayerRequest, s.dryRun, c, v))
		if !v.Allowed {
			ref.Denied = append(ref.Denied, denial(c, v))
		}
	}
	if err := s.audit.Append(lines); err != nil {
		ref.Denied = ref.Denied[:0]
		for _, c := range res.Checks {
			ref.Denied = append(ref.Denied, Denial{Resource: c.Target(), Verb: c.Verb.String(), Rule: RuleAuditUnwritable, KindWide: c.KindWide})
		}
		if len(ref.Denied) == 0 {
			ref.Denied = []Denial{{Resource: describe(req), Rule: RuleAuditUnwritable}}
		}
		return s.refuse(ref)
	}
	if len(ref.Denied) > 0 {
		return s.refuse(ref)
	}
	s.mu.Lock()
	for _, c := range res.Checks {
		s.written = append(s.written, c.String())
	}
	s.cache.dropFetched()
	s.mu.Unlock()
	return nil
}

// CheckHop decides a redirect hop of a request Check allowed (D-74): the hop
// is mapped again, and refused when it maps to another row or other checks.
// A hop that maps the same records nothing new.
func (s *Session) CheckHop(ctx context.Context, orig, hop reqmap.Request) error {
	a := s.classify(ctx, orig)
	b := s.classify(ctx, hop)
	if a.Disposition == b.Disposition && a.Row == b.Row && strings.Join(a.Strings(), "\n") == strings.Join(b.Strings(), "\n") {
		return nil
	}
	ref := s.newDenial(LayerRequest, s.dryRun, describe(hop))
	ref.Denied = []Denial{{Resource: describe(hop), Rule: RuleHopDiffers}}
	_ = s.audit.Append([]AuditLine{s.line(LayerRequest, s.dryRun, describe(hop), "", false, RuleHopDiffers, 0)})
	return s.refuse(ref)
}

// Observe records the facts a successful answer establishes (D-72).
func (s *Session) Observe(req reqmap.Request, resp []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = s.cache.forHash(s.pol.Hash())
	s.cache.observe(req, resp)
}

// classify maps a request with what this invocation knows, asking Home
// Assistant first for what the request's row needs and the invocation has
// not read yet.
func (s *Session) classify(ctx context.Context, req reqmap.Request) reqmap.Result {
	res := s.classifyRow(ctx, req)
	if res.Disposition != reqmap.Write || !reqmap.CandidateRows[res.Row] {
		return res
	}
	// The body is a candidate: what it would do is part of the write (W-1).
	more, denials := s.CandidateChecks(ctx, req.Body)
	if len(denials) > 0 {
		return reqmap.Result{Disposition: reqmap.Unmappable, Row: res.Row, Mutating: true,
			Rule: denials[0].Rule, Detail: "the candidate's effect cannot be decided: " + denials[0].Resource}
	}
	res.Checks = reqmap.MergeChecks(res.Checks, more)
	return res
}

// factsFor renders the cache with what the policy itself contributes. The
// caller holds s.mu.
func (s *Session) factsFor(reach *reqmap.Reach, cascade bool) *reqmap.Facts {
	f := s.cache.facts(reach, cascade)
	f.IndirectUnchecked = !s.pol.IndirectChecked()
	return f
}

// classifyRow maps a request by its row alone.
func (s *Session) classifyRow(ctx context.Context, req reqmap.Request) reqmap.Result {
	s.mu.Lock()
	s.cache = s.cache.forHash(s.pol.Hash())
	pre := reqmap.Classify(req, s.factsFor(nil, false))
	s.mu.Unlock()
	need := needsOf(pre.Row, req)
	if need == (needs{}) {
		return pre
	}
	reach := s.gather(ctx, req, need)
	s.mu.Lock()
	defer s.mu.Unlock()
	return reqmap.Classify(req, s.factsFor(reach, need.cascade))
}

// needs are the facts a row's mapping reads.
type needs struct {
	live, reach, registries, cascade bool
	dashboard                        bool
	dashboardIDs                     bool
}

func needsOf(row string, req reqmap.Request) needs {
	switch row {
	case "rest POST /api/services/{domain}/{service}":
		// A call with no data names no target and no entity: its service
		// check alone, nothing to ask (D-73).
		var data map[string]any
		if json.Unmarshal(req.Body, &data) == nil && len(data) == 0 {
			return needs{}
		}
		// The registries name the typed address of every entity the call
		// reaches — a script's config id (D-84), a helper's, a template
		// entity's (D-107).
		return needs{live: true, reach: true, registries: true}
	case "ws config/label_registry/delete", "ws config/area_registry/delete", "ws config/floor_registry/delete":
		return needs{registries: true}
	case "rest DELETE /api/config/config_entries/entry/{entry_id}":
		return needs{registries: true, cascade: true}
	case "ws lovelace/config/save":
		return needs{dashboard: true}
	case "ws lovelace/dashboards/delete":
		return needs{dashboardIDs: true}
	}
	return needs{}
}

// gather fills the cache with what need asks for and the invocation has not
// read, and returns the request's own reach answer. A question Home
// Assistant does not answer leaves the fact unread: the mapping then refuses
// or checks a superset (reqmap's pole), never less.
func (s *Session) gather(ctx context.Context, req reqmap.Request, need needs) *reqmap.Reach {
	f := s.fetcher
	s.mu.Lock()
	c := s.cache
	s.mu.Unlock()
	if need.live && !c.liveRead && f != nil {
		if st, err := f.LiveStates(ctx); err == nil {
			s.mu.Lock()
			c.live, c.liveRead = st, true
			s.mu.Unlock()
		}
	}
	if need.registries && !c.regRead && f != nil {
		if reg, complete, err := f.Registries(ctx); err == nil {
			s.mu.Lock()
			c.registries, c.regComplete, c.regRead = reg, complete, true
			s.mu.Unlock()
		}
	}
	if need.dashboard {
		u, _ := req.Params["url_path"].(string)
		u = canonicalDashboard(u)
		s.mu.Lock()
		_, have := c.dashboards[u]
		s.mu.Unlock()
		if !have && f != nil {
			if d, err := f.Dashboard(ctx, u); err == nil {
				s.mu.Lock()
				c.dashboards[u] = d
				s.mu.Unlock()
			}
		}
	}
	if need.dashboardIDs && c.dashboardIDs == nil && f != nil {
		if ids, err := f.DashboardIDs(ctx); err == nil {
			s.mu.Lock()
			c.dashboardIDs = ids
			s.mu.Unlock()
		}
	}
	if !need.reach {
		return nil
	}
	q, err := reqmap.ReachQuery(req)
	if err != nil {
		return &reqmap.Reach{Err: err}
	}
	if q == nil {
		return nil // no target, or only `all`: classified locally (D-59)
	}
	if f == nil {
		return &reqmap.Reach{Err: errors.New("the instance cannot be asked in this invocation (no .env)")}
	}
	target := map[string]any{}
	for k, v := range q {
		if k != "expand_group" {
			target[k] = v
		}
	}
	ids, err := f.Reach(ctx, target)
	if err != nil {
		return &reqmap.Reach{Err: err}
	}
	return &reqmap.Reach{EntityIDs: ids}
}

// decide answers one check: a concrete resource by Decide (where: selectors
// asked live), a kind-wide one by DecideKindWide (D-75), a deny-only one by
// its denies alone (D-107).
func (s *Session) decide(c reqmap.Check) policy.Verdict {
	switch {
	case c.KindWide && c.DenyOnly:
		return s.pol.DecideKindWideDenyOnly(c.Resource.Kind, c.Resource.Sub, c.Verb)
	case c.DenyOnly:
		return s.pol.DecideDenyOnly(c.Resource, c.Verb)
	case c.KindWide:
		return s.pol.DecideKindWide(c.Resource.Kind, c.Resource.Sub, c.Verb)
	}
	var live policy.Live
	if s.fetcher != nil {
		live = s.fetcher.Live()
	}
	return s.pol.Decide(c.Resource, c.Verb, live)
}

func denial(c reqmap.Check, v policy.Verdict) Denial {
	return Denial{
		Resource: c.Target(), Verb: c.Verb.String(), Rule: v.Rule, Line: v.Line,
		Partial: v.Partial, KindWide: c.KindWide, UnmetWhere: unmetWhere(v),
	}
}

// unmetWhere is a verdict's unmet selectors (D-112), nil when there are none
// so a denial and an audit line carry the key only when it says something.
func unmetWhere(v policy.Verdict) []policy.UnmetWhere {
	if len(v.UnmetWhere) == 0 {
		return nil
	}
	return v.UnmetWhere
}

func (s *Session) newDenial(layer string, dryRun bool, action string) *DeniedError {
	return &DeniedError{
		Layer: layer, DryRun: dryRun, Action: action,
		Profile: s.pol.Active(), SelectedBy: s.pol.Selection(), Written: []string{},
	}
}

// refuse records a request-layer refusal (the first one is the invocation's)
// and names what was already written.
func (s *Session) refuse(ref *DeniedError) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref.Written = append([]string{}, s.written...)
	ref.PlanAllowed = s.planned
	if s.refused == nil {
		s.refused = ref
	}
	return ref
}

func (s *Session) line(layer string, dryRun bool, resource, verb string, allowed bool, rule string, lineNo int) AuditLine {
	return AuditLine{
		Time: s.now().Format(time.RFC3339), Command: s.command, DryRun: dryRun, Layer: layer,
		Resource: resource, Verb: verb, Allowed: allowed, Rule: rule, Line: lineNo, Profile: s.pol.Active(),
	}
}

// verdictLine is the audit line of check c's verdict v.
func (s *Session) verdictLine(layer string, dryRun bool, c reqmap.Check, v policy.Verdict) AuditLine {
	l := s.line(layer, dryRun, c.Target(), c.Verb.String(), v.Allowed, v.Rule, v.Line)
	l.UnmetWhere = unmetWhere(v)
	return l
}

// describe names a request the way a refusal of it does: `ws <type>`,
// `rest <METHOD> <path>`, `companion <METHOD> <route>` — never its body or
// query, which may carry content.
func describe(req reqmap.Request) string {
	if req.Transport == reqmap.WS {
		return "ws " + req.Type
	}
	return fmt.Sprintf("%s %s %s", req.Transport, upperMethod(req.Method), routeOf(req.Path))
}

func upperMethod(m string) string { return strings.ToUpper(strings.TrimSpace(m)) }

// routeOf is a request path without its query.
func routeOf(p string) string {
	r, _, _ := strings.Cut(p, "?")
	return r
}

// queryValue reads one query parameter from the path's query and Query.
func queryValue(req reqmap.Request, key string) string {
	if _, raw, ok := strings.Cut(req.Path, "?"); ok {
		if q, err := url.ParseQuery(raw); err == nil && q.Get(key) != "" {
			return q.Get(key)
		}
	}
	return req.Query.Get(key)
}
