package reqmap

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// failClosedError is a mapping failure that names its own fail-closed rule. Any
// other mapping error is reported under the row's generic rule.
type failClosedError struct {
	rule   string
	detail string
}

func (e *failClosedError) Error() string { return e.rule + ": " + e.detail }

func refuse(rule, format string, args ...any) error {
	return &failClosedError{rule: "fail-closed: " + rule, detail: fmt.Sprintf(format, args...)}
}

// call is one request matched to one row, with what the mapper may read.
type call struct {
	req   Request
	facts *Facts
	vars  map[string]string // decoded placeholder values
	query url.Values
}

// param reads a websocket parameter that must be a string when present.
func (c *call) param(key string) (string, bool, error) {
	v, ok := c.req.Params[key]
	if !ok || v == nil {
		return "", false, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return "", true, fmt.Errorf("parameter %q is a %T, not a string", key, v)
	}
	return s, true, nil
}

// requiredParam is param for a parameter the row cannot map without.
func (c *call) requiredParam(key string) (string, error) {
	s, ok, err := c.param(key)
	if err != nil {
		return "", err
	}
	if !ok || s == "" {
		return "", fmt.Errorf("parameter %q is missing", key)
	}
	return s, nil
}

// requiredQuery reads a query parameter the row cannot map without.
func (c *call) requiredQuery(key string) (string, error) {
	vs, ok := c.query[key]
	if !ok || len(vs) == 0 || vs[0] == "" {
		return "", fmt.Errorf("query parameter %q is missing", key)
	}
	if len(vs) > 1 {
		return "", fmt.Errorf("query parameter %q is given %d times", key, len(vs))
	}
	return vs[0], nil
}

// facts never returns nil, so a mapper can read every field.
func (c *call) fact() *Facts {
	if c.facts == nil {
		return &Facts{}
	}
	return c.facts
}

// mapping is what a row's mapper answers.
type mapping struct {
	checks       []Check
	fromResponse bool
}

type mapper func(c *call) (mapping, error)

// Classify maps one outgoing request to its disposition and checks. It is
// pure: it sends nothing and reads nothing beyond req and facts. facts may be
// nil.
func Classify(req Request, facts *Facts) Result {
	switch req.Transport {
	case WS:
		return classifyWS(req, facts)
	case REST, Companion:
		return classifyHTTP(req, facts)
	}
	return Result{
		Disposition: Unmapped,
		Mutating:    true,
		Rule:        "fail-closed: unmapped request",
		Detail:      fmt.Sprintf("unknown transport %q", req.Transport),
	}
}

func classifyWS(req Request, facts *Facts) Result {
	if req.Type == supervisorType {
		return classifySupervisor(req, facts)
	}
	r, ok := wsRows[req.Type]
	if !ok {
		what := "ws " + req.Type
		return Result{
			Disposition: Unmapped,
			Mutating:    true,
			Rule:        "fail-closed: unmapped request " + what,
			Detail:      "no row of dev/surfaces/policy.manifest names this websocket type; every websocket type counts as mutating (E-9)",
		}
	}
	return r.apply(&call{req: req, facts: facts})
}

// classifySupervisor keys the generic supervisor/api proxy on (method,
// endpoint), never on the type alone: any other pair is unmapped, hence
// refused.
func classifySupervisor(req Request, facts *Facts) Result {
	c := &call{req: req, facts: facts}
	endpoint, err := c.requiredParam("endpoint")
	if err != nil {
		return unmappable(supervisorType, true, "fail-closed: unmappable request ws "+supervisorType, err.Error())
	}
	method, _, err := c.param("method")
	if err != nil {
		return unmappable(supervisorType, true, "fail-closed: unmappable request ws "+supervisorType, err.Error())
	}
	if method == "" {
		method = "get" // SupervisorAPI's own default
	}
	what := fmt.Sprintf("ws %s %s %s", supervisorType, upper(method), endpoint)
	return matchPath(supervisorRows, upper(method), endpoint, c, what, true)
}

func classifyHTTP(req Request, facts *Facts) Result {
	rawPath, rawQuery, _ := strings.Cut(req.Path, "?")
	query, err := url.ParseQuery(rawQuery)
	method := upper(req.Method)
	mutating := method != "GET"
	what := fmt.Sprintf("%s %s %s", req.Transport, method, rawPath)
	if err != nil {
		return unmappable("", mutating, "fail-closed: unmappable request "+what, "query: "+err.Error())
	}
	for k, vs := range req.Query {
		query[k] = append(query[k], vs...)
	}
	rows := restRows
	if req.Transport == Companion {
		rows = companionRows
	}
	return matchPath(rows, method, rawPath, &call{req: req, facts: facts, query: query}, what, mutating)
}

// matchPath applies the whole-path rule, finds the row and maps the request.
// defaultMutating is the method's own answer, used when no row names it.
func matchPath(rows []*row, method, rawPath string, c *call, what string, defaultMutating bool) Result {
	segs, pathErr := checkWholePath(rawPath)
	raw := splitPath(rawPath)

	var strict, loose *row
	var vars map[string]string
	for _, r := range rows {
		if r.method != method {
			continue
		}
		if pathErr == nil {
			if v, ok := r.matchStrict(segs); ok {
				strict, vars = r, v
				break
			}
		}
		if loose == nil && r.matchLoose(raw) {
			loose = r
		}
	}
	switch {
	case strict != nil:
		c.vars = vars
		return strict.apply(c)
	case loose != nil:
		why := "a placeholder would have to span more or fewer than one path segment"
		if pathErr != nil {
			why = pathErr.Error()
		}
		// A request the row cannot vouch for keeps the transport's own
		// answer too: an unmappable websocket message is mutating even when
		// the row it resembles is exempt.
		return unmappable(loose.key, loose.mutating() || defaultMutating, "fail-closed: unmappable request "+what,
			"the whole-path rule refuses it (R-8): "+why)
	case pathErr != nil:
		return unmappable("", defaultMutating, "fail-closed: unmappable request "+what,
			"the whole-path rule refuses it (R-8): "+pathErr.Error())
	}
	detail := "no row of dev/surfaces/policy.manifest names this request"
	if defaultMutating {
		detail += "; every method other than GET counts as mutating (E-9)"
	} else {
		detail += "; an unmapped GET is allowed until reads are enforced"
	}
	return Result{
		Disposition: Unmapped,
		Mutating:    defaultMutating,
		Rule:        "fail-closed: unmapped request " + what,
		Detail:      detail,
	}
}

func unmappable(key string, mutating bool, rule, detail string) Result {
	return Result{Disposition: Unmappable, Row: key, Mutating: mutating, Rule: rule, Detail: detail}
}

// apply runs a matched row's mapper and shapes the result.
func (r *row) apply(c *call) Result {
	res := Result{Disposition: r.disp, Row: r.key, Mutating: r.mutating()}
	if r.disp == Exempt {
		res.Detail = r.why
		return res
	}
	if r.mapFn == nil {
		return res
	}
	m, err := r.mapFn(c)
	if err != nil {
		res.Disposition = Unmappable
		var fc *failClosedError
		if errors.As(err, &fc) {
			res.Rule, res.Detail = fc.rule, fc.detail
		} else {
			res.Rule, res.Detail = "fail-closed: unmappable request "+r.key, err.Error()
		}
		return res
	}
	set := newCheckSet()
	set.addAll(m.checks)
	res.Checks = set.list()
	res.PlanFromResponse = m.fromResponse
	return res
}

// checkWholePath applies R-8 before any row is matched: no `.` or `..`
// segment, no %2F or %5C (in any case) and no backslash anywhere, and every
// segment must unescape. It returns the decoded segments.
func checkWholePath(raw string) ([]string, error) {
	if !strings.HasPrefix(raw, "/") {
		return nil, fmt.Errorf("path %q does not start with /", raw)
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(raw, `\`) {
		return nil, fmt.Errorf("path %q carries an encoded slash or a backslash", raw)
	}
	parts := splitPath(raw)
	out := make([]string, len(parts))
	for i, p := range parts {
		dec, err := url.PathUnescape(p)
		if err != nil {
			return nil, fmt.Errorf("segment %q does not unescape: %w", p, err)
		}
		if dec == "." || dec == ".." || p == "." || p == ".." {
			return nil, fmt.Errorf("path %q has a dot segment", raw)
		}
		out[i] = dec
	}
	return out, nil
}

func splitPath(p string) []string {
	return strings.Split(strings.TrimPrefix(p, "/"), "/")
}

func isPlaceholder(seg string) bool {
	return len(seg) > 2 && strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

// matchStrict matches decoded segments: same count, literals equal, each
// placeholder exactly one non-empty segment.
func (r *row) matchStrict(segs []string) (map[string]string, bool) {
	if len(segs) != len(r.tmpl) {
		return nil, false
	}
	vars := map[string]string{}
	for i, t := range r.tmpl {
		if isPlaceholder(t) {
			if segs[i] == "" {
				return nil, false
			}
			vars[t[1:len(t)-1]] = segs[i]
			continue
		}
		if t != segs[i] {
			return nil, false
		}
	}
	return vars, true
}

// matchLoose reports whether the raw segments would match if a placeholder
// could take any number of segments (zero included). Such a request names the
// row but breaks the rule that a placeholder is exactly one segment, so it is
// unmappable — and mutating if the row is a write.
func (r *row) matchLoose(raw []string) bool {
	var rec func(ti, si int) bool
	rec = func(ti, si int) bool {
		if ti == len(r.tmpl) {
			return si == len(raw)
		}
		t := r.tmpl[ti]
		if isPlaceholder(t) {
			for k := si; k <= len(raw); k++ {
				if rec(ti+1, k) {
					return true
				}
			}
			return false
		}
		if si < len(raw) && raw[si] == t {
			return rec(ti+1, si+1)
		}
		return false
	}
	return rec(0, 0)
}
