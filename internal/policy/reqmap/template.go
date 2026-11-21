package reqmap

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// What a template in a candidate reads (D-188 as refined by D-198, decided by
// Jan 2026-11-20).
//
// A template made only of safe constructs — literals, operators, its own and
// the automation's variables, `this`, the time functions and the pure math,
// string and list filters and tests of tplnames.go — reads nothing Home
// Assistant holds. A construct that reads state is checked as the reads it
// makes where hactl can name them (tplreads.go): `states('sun.sun')` needs
// `read entity:sun.sun/state`. One whose reach hactl cannot name — a
// computed entity id, a lookup, an unknown name, filter, test or tag — is
// unknowable: it needs `read system:template`, which only an allow by that
// exact name grants while the profile denies anything (enforce.TemplateRead).
// A variable assigned from an unknowable template is unknowable wherever it
// is used; one assigned from a template whose reads hactl names is checked
// where it is defined.
//
// A template whose value hactl can compute from constants alone (a literal, a
// concatenation, a foldable variable) is checked as the value it renders to:
// `{{ 'input_boolean.' ~ 'guard' }}` in a service's data names
// input_boolean.guard exactly as the literal does (D-189).

// TemplateUnsafe is one unknowable construct a candidate's template uses.
type TemplateUnsafe struct {
	Path, Construct string
}

// tplEnv is what a candidate's templates can name besides the globals.
type tplEnv struct {
	// vars are the candidate's variables (variables, trigger_variables,
	// fields, response_variable) and the runtime names (this, context,
	// repeat, wait, trigger).
	vars map[string]*tplVar
	// live, li and reg are what the reads are resolved against: the live
	// entity set and the registries (nil when not read).
	live *LiveStates
	li   *liveIndex
	reg  *Registries
	// trig is what `trigger` hands the templates (D-201).
	trig trigFacts
}

type tplVar struct {
	// unsafe names the unknowable construct the variable's value uses, ""
	// when there is none.
	unsafe string
	// value is the folded value, valid when folded.
	value  any
	folded bool
}

// tplRuntime are the names Home Assistant hands a script or automation's
// templates at run time: never a state hactl does not otherwise check
// (`this` is the entity itself; `repeat`, `wait`, `context` carry the run).
var tplRuntime = []string{"this", "context", "repeat", "wait"}

// newTplEnv builds the environment of a candidate: every variable it
// defines, analysed to a fixpoint, the runtime names, and what `trigger`
// hands over (D-201, D-203). A definition of a runtime name is not one: the
// candidate is refused for it (D-204), and the analysis keeps the runtime
// meaning.
func newTplEnv(doc any, live *LiveStates, reg *Registries) *tplEnv {
	env := &tplEnv{vars: map[string]*tplVar{}, live: live, reg: reg}
	if live != nil {
		env.li = newLiveIndex(live.EntityIDs)
	}
	for _, n := range tplRuntime {
		env.vars[n] = &tplVar{}
	}
	env.vars["trigger"] = &tplVar{}
	env.trig = triggerFacts(doc, reg)
	defs := map[string][]any{}
	collectVarDefs(doc, defs, env)
	// Fixpoint: a variable is unsafe if any definition is; folded when every
	// definition folds to one value.
	for range 8 {
		changed := false
		for _, name := range slices.Sorted(maps.Keys(defs)) {
			vals := defs[name]
			v := env.vars[name]
			if v == nil {
				v = &tplVar{}
				env.vars[name] = v
			}
			unsafe := ""
			var folded any
			ok := true
			for i, val := range vals {
				u, fv, fok := env.analyseValue(val)
				if unsafe == "" && u != "" {
					unsafe = u
				}
				if !fok || (i > 0 && fmt.Sprint(fv) != fmt.Sprint(folded)) {
					ok = false
				}
				folded = fv
			}
			if unsafe != v.unsafe || ok != v.folded || fmt.Sprint(folded) != fmt.Sprint(v.value) {
				v.unsafe, v.folded, v.value = unsafe, ok, folded
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return env
}

// collectVarDefs gathers every variable definition at any depth: the keys of
// `variables:` and `trigger_variables:` mappings, `response_variable:` names
// and a script's `fields:` keys (caller input: safe, never folded).
func collectVarDefs(v any, defs map[string][]any, env *tplEnv) {
	switch t := v.(type) {
	case map[string]any:
		// In key order: a variable defined twice keeps its definitions in one
		// order, so the construct a refusal names never moves.
		for _, k := range slices.Sorted(maps.Keys(t)) {
			collectVarDef(k, t[k], defs, env)
			collectVarDefs(t[k], defs, env)
		}
	case []any:
		for _, e := range t {
			collectVarDefs(e, defs, env)
		}
	}
}

// collectVarDef records what the key k with value val of a mapping defines.
func collectVarDef(k string, val any, defs map[string][]any, env *tplEnv) {
	switch k {
	case "variables", "trigger_variables":
		if m, ok := val.(map[string]any); ok {
			for _, name := range slices.Sorted(maps.Keys(m)) {
				if !tplReserved[name] {
					defs[name] = append(defs[name], m[name])
				}
			}
		}
	case "response_variable":
		if s, ok := val.(string); ok && !isTemplate(s) && !tplReserved[s] {
			env.vars[s] = &tplVar{}
		}
	case "fields":
		if m, ok := val.(map[string]any); ok {
			for name := range m {
				if _, defined := env.vars[name]; !defined && !tplReserved[name] {
					env.vars[name] = &tplVar{}
				}
			}
		}
	}
}

func anyStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// analyseValue analyses a variable's value (any YAML value): the first
// unsafe construct its templates use, and its folded value.
func (env *tplEnv) analyseValue(v any) (unsafe string, folded any, ok bool) {
	switch t := v.(type) {
	case string:
		if !isTemplate(t) {
			return "", t, true
		}
		tpl, err := parseJinja(t)
		if err != nil {
			return "template that does not parse", nil, false
		}
		if u := env.unsafeIn(tpl); len(u) > 0 {
			return u[0], nil, false
		}
		val, fok := env.fold(tpl)
		return "", val, fok
	case map[string]any:
		out := map[string]any{}
		ok = true
		for _, k := range sortedMapKeys(t) {
			uk, fk, okk := env.analyseValue(k)
			u, f, okv := env.analyseValue(t[k])
			if unsafe == "" {
				unsafe = uk
			}
			if unsafe == "" {
				unsafe = u
			}
			ks, isStr := fk.(string)
			ok = ok && okk && okv && isStr
			if isStr {
				out[ks] = f
			}
		}
		return unsafe, out, ok
	case []any:
		out := make([]any, 0, len(t))
		ok = true
		for _, e := range t {
			u, f, eok := env.analyseValue(e)
			if unsafe == "" {
				unsafe = u
			}
			ok = ok && eok
			out = append(out, f)
		}
		return unsafe, out, ok
	}
	return "", v, true
}

// unsafeIn lists every unknowable construct a parsed template uses, in
// order, each once.
func (env *tplEnv) unsafeIn(t *jinjaTemplate) []string { return env.analyse(t).out }

// loopValues are the for variables that fold (D-210): the target is a single
// name no other target assigns, and the iterable folds to a list of strings
// — `{% for e in ['lock.front', 'lock.back'] %}` — each mapped to its
// values, lowercased like any entity id.
func loopValues(env *tplEnv, t *jinjaTemplate) map[string][]string {
	var out map[string][]string
	for _, l := range t.loops {
		if t.binds[l.name] != 1 || l.name == "loop" {
			continue
		}
		v, ok := env.foldExpr(l.iter)
		list, isList := v.([]any)
		if !ok || !isList {
			continue
		}
		vals := make([]string, 0, len(list))
		for _, e := range list {
			s, isStr := e.(string)
			if !isStr {
				vals = nil
				break
			}
			vals = append(vals, strings.ToLower(s))
		}
		if vals == nil && len(list) > 0 {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[l.name] = vals
	}
	return out
}

// analyse reads a parsed template: its unknowable constructs and the reads
// hactl can name.
func (env *tplEnv) analyse(t *jinjaTemplate) *tplAnalysis {
	a := &tplAnalysis{env: env, bound: t.bound, seen: map[string]bool{}, seenRead: map[string]int{}}
	a.loopVals = loopValues(env, t)
	for _, p := range t.parts {
		if p.expr != nil {
			a.node(p.expr)
		}
	}
	for _, tag := range t.tags {
		switch tag.name {
		case "if", "elif", "else", "endif", "for", "endfor", "set", "endset", "with", "endwith",
			"filter", "endfilter", "do", "break", "continue":
		default:
			a.add("tag " + tag.name)
		}
		for _, e := range tag.exprs {
			a.node(e)
		}
		for _, f := range tag.filters {
			a.filter(f)
		}
	}
	return a
}

type tplAnalysis struct {
	env   *tplEnv
	bound map[string]bool
	// loopVals are the for variables over a literal list and their values
	// (D-210).
	loopVals map[string][]string
	// out are the unknowable constructs; reads what the rest reads.
	out      []string
	seen     map[string]bool
	reads    []tplRead
	seenRead map[string]int
}

func (a *tplAnalysis) add(c string) {
	if !a.seen[c] {
		a.seen[c] = true
		a.out = append(a.out, c)
	}
}

func (a *tplAnalysis) nodes(ns []jNode) {
	for _, n := range ns {
		a.node(n)
	}
}

func (a *tplAnalysis) node(n jNode) {
	switch t := n.(type) {
	case nil, jConst:
	case jName:
		a.name(t.name)
	case jList:
		a.nodes(t.items)
	case jDict:
		a.nodes(t.keys)
		a.nodes(t.vals)
	case jGetattr, jGetitem, jCall:
		a.chain(n)
	case jSlice:
		a.node(t.a)
		a.node(t.b)
		a.node(t.c)
	case jFilter:
		a.filter(t)
	case jTest:
		if unsafeTemplateTests[t.name] {
			a.reading(t.name, append([]jNode{t.arg}, t.args.pos...), t.args, "test "+t.name)
			return
		}
		a.node(t.arg)
		a.nodes(t.args.all())
		if !safeTemplateTests[t.name] {
			a.add("test " + t.name)
		}
	case jBin:
		a.node(t.l)
		a.node(t.r)
	case jUnary:
		a.node(t.x)
	case jCond:
		a.node(t.test)
		a.node(t.a)
		a.node(t.b)
	default:
		a.add(fmt.Sprintf("construct %T", n))
	}
}

// name classifies a bare name: `states` used whole reads every live entity,
// `trigger` and `wait` what the triggers hand over; a reading global used
// other than by a call, or rebound by the template or a variable, is
// unknowable (a `set` in a branch not taken leaves Jinja resolving the
// global); a bound name or a safe variable is safe; a safe global is safe;
// anything else is unknowable.
func (a *tplAnalysis) name(n string) {
	switch {
	case n == "states" && a.global(n):
		a.wholeDomain("states", "")
	case (n == "trigger" || n == "wait") && a.runtime(n):
		a.triggerChain(nil, n)
	case unsafeTemplateGlobals[n] && a.global(n):
		a.add(n)
	case unsafeTemplateGlobals[n]:
		a.add(n + " (rebound)")
	case a.bound[n]:
	case a.env.vars[n] != nil:
		if u := a.env.vars[n].unsafe; u != "" {
			a.add("variable " + n + " (from " + u + ")")
		}
	case safeTemplateGlobals[n]:
	default:
		a.add("name " + n)
	}
}

// nameArgFilters take the name of a filter or test as a string argument:
// map('states') applies the states filter, select('is_state', …) the
// is_state test. The position of the name, and whether it is a filter.
var nameArgFilters = map[string]struct {
	pos      int
	isFilter bool
}{
	"map": {0, true}, "select": {0, false}, "reject": {0, false},
	"selectattr": {1, false}, "rejectattr": {1, false},
}

func (a *tplAnalysis) filter(f jFilter) {
	if a.projection(f) {
		return
	}
	if unsafeTemplateFilters[f.name] {
		a.reading(f.name, append([]jNode{f.arg}, f.args.pos...), f.args, "filter "+f.name)
		return
	}
	walk := func() {
		a.node(f.arg)
		a.nodes(f.args.all())
	}
	if !safeTemplateFilters[f.name] {
		walk()
		a.add("filter " + f.name)
		return
	}
	spec, ok := nameArgFilters[f.name]
	switch {
	case !ok, f.name == "map" && len(f.args.pos) == 0, // map(attribute=…)
		len(f.args.pos) <= spec.pos && len(f.args.dyn) == 0: // selectattr('a'): truthiness, no test
		walk()
		return
	case len(f.args.dyn) > 0:
		walk()
		a.add("filter " + f.name + " with computed arguments")
		return
	}
	c, isConst := f.args.pos[spec.pos].(jConst)
	s, isStr := c.v.(string)
	switch {
	case !isConst || !isStr:
		walk()
		a.add("filter " + f.name + " with a computed name")
	case spec.isFilter && unsafeTemplateFilters[s], !spec.isFilter && unsafeTemplateTests[s]:
		a.nameArgReads(f, s, spec.isFilter, spec.pos)
	case spec.isFilter && !safeTemplateFilters[s]:
		walk()
		a.add("filter " + s + " (through " + f.name + ")")
	case !spec.isFilter && !safeTemplateTests[s]:
		walk()
		a.add("test " + s + " (through " + f.name + ")")
	default:
		walk()
	}
}

// ---------------------------------------------------------------------------
// Folding: the value a template renders to, computed from constants alone.

type tplUnfoldable struct{}

// fold renders a template whose every part folds. A template with
// statements does not fold. One `{{ expr }}` alone folds to the expression's
// value (Home Assistant renders native types); text and parts fold to the
// string they concatenate to, read back as a literal when it is one, as Home
// Assistant does.
func (env *tplEnv) fold(t *jinjaTemplate) (any, bool) {
	if len(t.tags) > 0 {
		return nil, false
	}
	var exprs []jNode
	for _, p := range t.parts {
		if p.expr != nil {
			exprs = append(exprs, p.expr)
		}
	}
	if len(t.parts) == 1 && len(exprs) == 1 {
		return env.foldExpr(exprs[0])
	}
	var b strings.Builder
	for _, p := range t.parts {
		if p.expr == nil {
			b.WriteString(p.text)
			continue
		}
		v, ok := env.foldExpr(p.expr)
		if !ok {
			return nil, false
		}
		b.WriteString(pyStr(v))
	}
	return literalOf(b.String()), true
}

// literalOf reads a rendered string back as Home Assistant does: a string
// that is a Python literal of a list, dict, number or bool is that value.
func literalOf(s string) any {
	trim := strings.TrimSpace(s)
	if trim == "" || !strings.ContainsAny(trim[:1], "[{(0123456789-+.TF") {
		return s
	}
	tpl, err := parseJinja("{{ " + trim + " }}")
	if err != nil {
		return s
	}
	env := &tplEnv{vars: map[string]*tplVar{}}
	if v, ok := env.fold(tpl); ok {
		return v
	}
	return s
}

func (env *tplEnv) foldExpr(n jNode) (v any, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			v, ok = nil, false
		}
	}()
	return env.eval(n), true
}

func unfoldable() { panic(tplUnfoldable{}) }

func (env *tplEnv) eval(n jNode) any {
	switch t := n.(type) {
	case jConst:
		return t.v
	case jName:
		v := env.vars[t.name]
		if v == nil || !v.folded || v.unsafe != "" {
			unfoldable()
		}
		return v.value
	case jList:
		out := make([]any, 0, len(t.items))
		for _, e := range t.items {
			out = append(out, env.eval(e))
		}
		return out
	case jDict:
		out := map[string]any{}
		for i := range t.keys {
			k, ok := env.eval(t.keys[i]).(string)
			if !ok {
				unfoldable()
			}
			out[k] = env.eval(t.vals[i])
		}
		return out
	case jBin:
		return foldBin(t.op, env.eval(t.l), env.eval(t.r))
	case jFilter:
		return env.foldFilter(t)
	case jGetitem:
		return foldItem(env.eval(t.obj), env.eval(t.idx))
	}
	unfoldable()
	return nil
}

// foldItem is obj[idx] for a folded dict or list; anything else cannot be
// folded.
func foldItem(obj, idx any) any {
	switch o := obj.(type) {
	case map[string]any:
		if s, ok := idx.(string); ok {
			if v, ok := o[s]; ok {
				return v
			}
		}
	case []any:
		if i, ok := idx.(int64); ok {
			if i < 0 {
				i += int64(len(o))
			}
			if i >= 0 && i < int64(len(o)) {
				return o[i]
			}
		}
	}
	unfoldable()
	return nil
}

func foldBin(op string, l, r any) any {
	switch op {
	case "~":
		return pyStr(l) + pyStr(r)
	case "+":
		switch lv := l.(type) {
		case string:
			if rv, ok := r.(string); ok {
				return lv + rv
			}
		case []any:
			if rv, ok := r.([]any); ok {
				return append(append([]any{}, lv...), rv...)
			}
		case int64:
			if rv, ok := r.(int64); ok {
				return lv + rv
			}
		}
	}
	unfoldable()
	return nil
}

// foldFilter folds the string filters a concatenation is commonly built
// with; any other filter does not fold.
func (env *tplEnv) foldFilter(f jFilter) any {
	if len(f.args.kwName) > 0 || len(f.args.dyn) > 0 {
		unfoldable()
	}
	arg := env.eval(f.arg)
	args := make([]any, 0, len(f.args.pos))
	for _, a := range f.args.pos {
		args = append(args, env.eval(a))
	}
	str := func() string {
		s, ok := arg.(string)
		if !ok {
			unfoldable()
		}
		return s
	}
	switch {
	case f.name == "lower" && len(args) == 0:
		return strings.ToLower(str())
	case f.name == "upper" && len(args) == 0:
		return strings.ToUpper(str())
	case f.name == "trim" && len(args) == 0:
		return strings.TrimSpace(str())
	case f.name == "string" && len(args) == 0:
		return pyStr(arg)
	case f.name == "list" && len(args) == 0:
		if l, ok := arg.([]any); ok {
			return l
		}
	case f.name == "replace" && len(args) == 2:
		o, ok1 := args[0].(string)
		nw, ok2 := args[1].(string)
		if ok1 && ok2 {
			return strings.ReplaceAll(str(), o, nw)
		}
	case f.name == "join" && len(args) <= 1:
		l, ok := arg.([]any)
		sep := ""
		if len(args) == 1 {
			if sep, ok = args[0].(string); !ok {
				unfoldable()
			}
		}
		if ok {
			parts := make([]string, 0, len(l))
			for _, e := range l {
				parts = append(parts, pyStr(e))
			}
			return strings.Join(parts, sep)
		}
	}
	unfoldable()
	return nil
}

// pyStr is Python's str() of a folded value.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e16 {
			return strconv.FormatFloat(t, 'f', 1, 64)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, pyRepr(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(t))
		for _, k := range keys {
			parts = append(parts, pyRepr(k)+": "+pyRepr(t[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	unfoldable()
	return ""
}

func pyRepr(v any) string {
	if s, ok := v.(string); ok {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
	}
	return pyStr(v)
}
