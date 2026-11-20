package reqmap

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// A Jinja reader for the templates a candidate carries (D-188): enough of
// Jinja's lexer and parser (jinja2/lexer.py, jinja2/parser.py, the version Home
// Assistant ships) to name every identifier, attribute, call, filter, test and
// tag a template uses — never a substring search. What it does not parse it
// refuses: a template hactl cannot read is a template whose reads it cannot
// state.

// jinjaTemplate is a parsed template: its text and `{{ }}` parts in order,
// and every statement (`{% %}`) it holds.
type jinjaTemplate struct {
	parts []jinjaPart
	tags  []jinjaTag
	// bound is every name the template itself assigns: set, for and with
	// targets, and `loop` inside a for.
	bound map[string]bool
}

// jinjaPart is literal text (expr nil) or one `{{ expr }}`.
type jinjaPart struct {
	text string
	expr jNode
}

// jinjaTag is one statement: its keyword and the expressions it evaluates.
type jinjaTag struct {
	name  string
	exprs []jNode
	// filters are the filter names a `{% filter %}` block or a filtered
	// block `set` applies.
	filters []jFilter
}

type jNode any

type (
	jConst   struct{ v any } // string, int64, float64, bool or nil
	jName    struct{ name string }
	jList    struct{ items []jNode }
	jDict    struct{ keys, vals []jNode }
	jGetattr struct {
		obj  jNode
		attr string
	}
	jGetitem struct{ obj, idx jNode }
	jSlice   struct{ a, b, c jNode }
	jCall    struct {
		fn   jNode
		args jArgs
	}
	jFilter struct {
		arg  jNode
		name string
		args jArgs
	}
	jTest struct {
		arg  jNode
		name string
		args jArgs
	}
	jBin struct {
		op   string
		l, r jNode
	}
	jUnary struct {
		op string
		x  jNode
	}
	jCond struct{ test, a, b jNode }
)

type jArgs struct {
	pos    []jNode
	kwName []string
	kwVal  []jNode
	dyn    []jNode // *args, **kwargs
}

func (a jArgs) all() []jNode {
	out := append(append([]jNode{}, a.pos...), a.kwVal...)
	return append(out, a.dyn...)
}

var errJinja = errors.New("template does not parse")

func jinjaErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errJinja, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------
// Lexer

type jTok struct {
	kind string // name, string, int, float, op, end ("}}" / "%}"), eof
	val  string
}

// lexExpr tokenizes src from i up to the closing delimiter (`}}` or `%}`, an
// optional `-`/`+` before it), returning the tokens and the index after it.
func lexExpr(src string, i int, closer string) ([]jTok, int, error) {
	var toks []jTok
	depth := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
			continue
		case depth == 0 && (strings.HasPrefix(src[i:], closer) ||
			((c == '-' || c == '+') && strings.HasPrefix(src[i+1:], closer))):
			if c != closer[0] {
				i++
			}
			return append(toks, jTok{kind: "end"}), i + len(closer), nil
		case c == '_' || isAlpha(c):
			j := lexName(src, i)
			toks = append(toks, jTok{kind: "name", val: src[i:j]})
			i = j
			continue
		case isDigit(c) || (c == '.' && i+1 < len(src) && isDigit(src[i+1]) && !lastIsValue(toks)):
			j, kind := lexNumber(src, i)
			toks = append(toks, jTok{kind: kind, val: strings.ReplaceAll(src[i:j], "_", "")})
			i = j
			continue
		case c == '\'' || c == '"':
			s, j, err := lexString(src, i)
			if err != nil {
				return nil, 0, err
			}
			toks = append(toks, jTok{kind: "string", val: s})
			i = j
			continue
		}
		op, err := lexOp(src, i, &depth)
		if err != nil {
			return nil, 0, err
		}
		toks = append(toks, jTok{kind: "op", val: op})
		i += len(op)
	}
	return nil, 0, jinjaErr("unterminated %s", closer)
}

// lexOp reads the operator at src[i], keeping the bracket depth.
func lexOp(src string, i int, depth *int) (string, error) {
	op := ""
	for _, o := range []string{"**", "//", "==", "!=", "<=", ">="} {
		if strings.HasPrefix(src[i:], o) {
			op = o
			break
		}
	}
	if op == "" {
		c := src[i]
		if !strings.ContainsRune("+-*/%~<>=.,:|()[]{}", rune(c)) {
			return "", jinjaErr("unexpected character %q", c)
		}
		op = string(c)
	}
	switch op {
	case "(", "[", "{":
		*depth++
	case ")", "]", "}":
		*depth--
		if *depth < 0 {
			return "", jinjaErr("unbalanced %q", op)
		}
	}
	return op, nil
}

// lexName is the index after the name starting at src[i].
func lexName(src string, i int) int {
	j := i + 1
	for j < len(src) && (src[j] == '_' || isAlpha(src[j]) || isDigit(src[j])) {
		j++
	}
	return j
}

func lastIsValue(toks []jTok) bool {
	if len(toks) == 0 {
		return false
	}
	t := toks[len(toks)-1]
	return t.kind == "name" || t.kind == "string" || t.kind == "int" || t.kind == "float" ||
		(t.kind == "op" && (t.val == ")" || t.val == "]" || t.val == "}"))
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func lexNumber(src string, i int) (int, string) {
	j := i
	kind := "int"
	digits := func() {
		for j < len(src) && (isDigit(src[j]) || src[j] == '_') {
			j++
		}
	}
	if strings.HasPrefix(src[j:], "0x") || strings.HasPrefix(src[j:], "0X") ||
		strings.HasPrefix(src[j:], "0o") || strings.HasPrefix(src[j:], "0b") {
		j += 2
		for j < len(src) && (isDigit(src[j]) || src[j] == '_' || strings.ContainsRune("abcdefABCDEF", rune(src[j]))) {
			j++
		}
		return j, "int"
	}
	digits()
	if j+1 < len(src) && src[j] == '.' && isDigit(src[j+1]) {
		kind = "float"
		j++
		digits()
	}
	if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
		k := j + 1
		if k < len(src) && (src[k] == '+' || src[k] == '-') {
			k++
		}
		if k < len(src) && isDigit(src[k]) {
			kind = "float"
			j = k
			digits()
		}
	}
	return j, kind
}

func lexString(src string, i int) (string, int, error) {
	q := src[i]
	var b strings.Builder
	j := i + 1
	for j < len(src) {
		c := src[j]
		switch {
		case c == '\\' && j+1 < len(src):
			n := src[j+1]
			switch n {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\', '\'', '"':
				b.WriteByte(n)
			default:
				b.WriteByte('\\')
				b.WriteByte(n)
			}
			j += 2
			continue
		case c == q:
			return b.String(), j + 1, nil
		}
		b.WriteByte(c)
		j++
	}
	return "", 0, jinjaErr("unterminated string")
}

// parseJinja parses a whole template source.
func parseJinja(src string) (*jinjaTemplate, error) {
	t := &jinjaTemplate{bound: map[string]bool{}}
	var stack []string // open block tags
	i := 0
	for i < len(src) {
		j := indexDelim(src, i)
		if j < 0 {
			t.parts = append(t.parts, jinjaPart{text: src[i:]})
			break
		}
		if j > i {
			t.parts = append(t.parts, jinjaPart{text: src[i:j]})
		}
		open := src[j : j+2]
		k := j + 2
		if k < len(src) && (src[k] == '-' || src[k] == '+') {
			k++
		}
		switch open {
		case "{#":
			end := strings.Index(src[k:], "#}")
			if end < 0 {
				return nil, jinjaErr("unterminated comment")
			}
			i = k + end + 2
		case "{{":
			next, err := t.output(src, k)
			if err != nil {
				return nil, err
			}
			i = next
		case "{%":
			next, err := t.block(src, k, &stack)
			if err != nil {
				return nil, err
			}
			i = next
		}
	}
	if len(stack) > 0 {
		return nil, jinjaErr("unclosed {%% %s %%}", stack[len(stack)-1])
	}
	return t, nil
}

// output parses the `{{ … }}` whose expression starts at src[k].
func (t *jinjaTemplate) output(src string, k int) (int, error) {
	toks, next, err := lexExpr(src, k, "}}")
	if err != nil {
		return 0, err
	}
	p := &jParser{toks: toks}
	e, err := p.expression(true)
	if err != nil {
		return 0, err
	}
	if err := p.expectEnd(); err != nil {
		return 0, err
	}
	t.parts = append(t.parts, jinjaPart{expr: e})
	return next, nil
}

// block parses the `{% … %}` whose tokens start at src[k]; a raw block is
// kept as text up to its endraw.
func (t *jinjaTemplate) block(src string, k int, stack *[]string) (int, error) {
	toks, next, err := lexExpr(src, k, "%}")
	if err != nil {
		return 0, err
	}
	if len(toks) > 1 && toks[0].kind == "name" && toks[0].val == "raw" {
		end := rawEnd(src, next)
		if end.start < 0 {
			return 0, jinjaErr("unterminated raw block")
		}
		t.parts = append(t.parts, jinjaPart{text: src[next:end.start]})
		return end.after, nil
	}
	return next, t.statement(toks, stack)
}

func indexDelim(src string, i int) int {
	best := -1
	for _, d := range []string{"{{", "{%", "{#"} {
		if k := strings.Index(src[i:], d); k >= 0 && (best < 0 || i+k < best) {
			best = i + k
		}
	}
	return best
}

type rawSpan struct{ start, after int }

// rawEnd finds `{% endraw %}` (any whitespace control) from i.
func rawEnd(src string, i int) rawSpan {
	for {
		k := strings.Index(src[i:], "{%")
		if k < 0 {
			return rawSpan{-1, -1}
		}
		start := i + k
		rest := strings.TrimLeft(src[start+2:], "-+ \t\r\n")
		if strings.HasPrefix(rest, "endraw") {
			after := strings.Index(src[start:], "%}")
			if after < 0 {
				return rawSpan{-1, -1}
			}
			return rawSpan{start, start + after + 2}
		}
		i = start + 2
	}
}

// jStmt is one `{% %}` being parsed: its tokens, its tag and the open blocks.
type jStmt struct {
	t     *jinjaTemplate
	p     *jParser
	stack *[]string
	tag   jinjaTag
}

func (st *jStmt) top() string {
	if len(*st.stack) == 0 {
		return ""
	}
	return (*st.stack)[len(*st.stack)-1]
}

func (st *jStmt) push(open string) { *st.stack = append(*st.stack, open) }

func (st *jStmt) pop(open string) error {
	if st.top() != open {
		return jinjaErr("unexpected {%% %s %%}", st.tag.name)
	}
	*st.stack = (*st.stack)[:len(*st.stack)-1]
	return nil
}

func (st *jStmt) expr(allowTuple bool) error {
	e, err := st.p.expression(allowTuple)
	if err != nil {
		return err
	}
	st.tag.exprs = append(st.tag.exprs, e)
	return nil
}

// filters reads `f | g | …` into the tag's filters; first says whether the
// first filter comes without a leading `|`.
func (st *jStmt) filters(first bool) error {
	for first || st.p.skipOp("|") {
		first = false
		f, err := st.p.filterTail(nil)
		if err != nil {
			return err
		}
		st.tag.filters = append(st.tag.filters, f)
	}
	return nil
}

// statement parses the tokens of one `{% %}`.
func (t *jinjaTemplate) statement(toks []jTok, stack *[]string) error {
	if len(toks) == 0 || toks[0].kind != "name" {
		return jinjaErr("empty statement")
	}
	st := &jStmt{t: t, p: &jParser{toks: toks, pos: 1}, stack: stack, tag: jinjaTag{name: toks[0].val}}
	parse, known := jStatements[st.tag.name]
	if !known {
		// macro, call, import, from, include, extends, block, autoescape,
		// trans, …: a tag whose reads hactl does not follow. Recorded, and
		// classified unsafe by the analysis; its body is not parsed further.
		t.tags = append(t.tags, st.tag)
		return nil
	}
	if err := parse(st); err != nil {
		return err
	}
	if err := st.p.expectEnd(); err != nil {
		return err
	}
	t.tags = append(t.tags, st.tag)
	return nil
}

// jStatements are the tags hactl parses, by keyword.
var jStatements map[string]func(*jStmt) error

func init() {
	end := func(st *jStmt) error { return st.pop(strings.TrimPrefix(st.tag.name, "end")) }
	jStatements = map[string]func(*jStmt) error{
		"if":        func(st *jStmt) error { st.push("if"); return st.expr(true) },
		"elif":      stmtElif,
		"else":      stmtElse,
		"endif":     end,
		"endfor":    end,
		"endwith":   end,
		"endfilter": end,
		"endset":    end,
		"for":       stmtFor,
		"set":       stmtSet,
		"with":      stmtWith,
		"filter":    func(st *jStmt) error { st.push("filter"); return st.filters(true) },
		"do":        func(st *jStmt) error { return st.expr(true) },
		"break":     stmtLoopControl,
		"continue":  stmtLoopControl,
	}
}

func stmtElif(st *jStmt) error {
	if st.top() != "if" {
		return jinjaErr("elif outside if")
	}
	return st.expr(true)
}

func stmtElse(st *jStmt) error {
	if st.top() != "if" && st.top() != "for" {
		return jinjaErr("else outside if or for")
	}
	return nil
}

func stmtLoopControl(st *jStmt) error {
	if !slices.Contains(*st.stack, "for") {
		return jinjaErr("%s outside for", st.tag.name)
	}
	return nil
}

func stmtFor(st *jStmt) error {
	target, err := st.p.assignTarget()
	if err != nil {
		return err
	}
	st.t.bindTarget(target)
	st.t.bound["loop"] = true
	if !st.p.skipName("in") {
		return jinjaErr("for without in")
	}
	if err := st.expr(false); err != nil {
		return err
	}
	if st.p.skipName("if") {
		if err := st.expr(true); err != nil {
			return err
		}
	}
	st.p.skipName("recursive")
	st.push("for")
	return nil
}

func stmtSet(st *jStmt) error {
	target, err := st.p.assignTarget()
	if err != nil {
		return err
	}
	st.t.bindTarget(target)
	if st.p.skipOp("=") {
		st.tag.exprs = append(st.tag.exprs, target)
		return st.expr(true)
	}
	// A block set: `{% set x %}…{% endset %}`, optionally filtered.
	if _, ok := target.(jName); !ok {
		return jinjaErr("block set of %T", target)
	}
	st.push("set")
	return st.filters(false)
}

func stmtWith(st *jStmt) error {
	for !st.p.atEnd() {
		target, err := st.p.assignTarget()
		if err != nil {
			return err
		}
		st.t.bindTarget(target)
		if !st.p.skipOp("=") {
			return jinjaErr("with without =")
		}
		if err := st.expr(true); err != nil {
			return err
		}
		if !st.p.skipOp(",") {
			break
		}
	}
	st.push("with")
	return nil
}

func (t *jinjaTemplate) bindTarget(n jNode) {
	switch v := n.(type) {
	case jName:
		t.bound[v.name] = true
	case jList:
		for _, e := range v.items {
			t.bindTarget(e)
		}
	}
}

// ---------------------------------------------------------------------------
// Parser (jinja2/parser.py's expression grammar)

type jParser struct {
	toks []jTok
	pos  int
}

func (p *jParser) peek() jTok {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return jTok{kind: "eof"}
}

func (p *jParser) next() jTok {
	t := p.peek()
	if p.pos < len(p.toks) {
		p.pos++
	}
	return t
}

func (p *jParser) atEnd() bool { k := p.peek().kind; return k == "end" || k == "eof" }

func (p *jParser) expectEnd() error {
	if p.peek().kind != "end" {
		return jinjaErr("unexpected %q", p.peek().val)
	}
	p.pos++
	return nil
}

func (p *jParser) isOp(v string) bool   { t := p.peek(); return t.kind == "op" && t.val == v }
func (p *jParser) isName(v string) bool { t := p.peek(); return t.kind == "name" && t.val == v }

func (p *jParser) skipOp(v string) bool {
	if p.isOp(v) {
		p.pos++
		return true
	}
	return false
}

func (p *jParser) skipName(v string) bool {
	if p.isName(v) {
		p.pos++
		return true
	}
	return false
}

func (p *jParser) expectOp(v string) error {
	if !p.skipOp(v) {
		return jinjaErr("expected %q, found %q", v, p.peek().val)
	}
	return nil
}

// assignTarget is a for/set/with target: a name, a.b (namespace), or a
// tuple of names.
func (p *jParser) assignTarget() (jNode, error) {
	first, err := p.assignOne()
	if err != nil {
		return nil, err
	}
	if !p.isOp(",") {
		return first, nil
	}
	items := []jNode{first}
	for p.skipOp(",") {
		if p.isName("in") || p.isOp("=") {
			break
		}
		n, err := p.assignOne()
		if err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	return jList{items: items}, nil
}

func (p *jParser) assignOne() (jNode, error) {
	if p.skipOp("(") {
		n, err := p.assignTarget()
		if err != nil {
			return nil, err
		}
		return n, p.expectOp(")")
	}
	t := p.next()
	if t.kind != "name" {
		return nil, jinjaErr("bad assignment target %q", t.val)
	}
	var n jNode = jName{name: t.val}
	if p.skipOp(".") {
		a := p.next()
		if a.kind != "name" {
			return nil, jinjaErr("bad assignment target")
		}
		n = jGetattr{obj: n, attr: a.val}
	}
	return n, nil
}

// expression is parse_expression: a conditional expression; tuples (a bare
// `a, b`) when allowTuple.
func (p *jParser) expression(allowTuple bool) (jNode, error) {
	e, err := p.condExpr()
	if err != nil || !allowTuple || !p.isOp(",") {
		return e, err
	}
	items := []jNode{e}
	for p.skipOp(",") {
		if p.atEnd() || p.isOp(")") {
			break
		}
		n, err := p.condExpr()
		if err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	return jList{items: items}, nil
}

func (p *jParser) condExpr() (jNode, error) {
	e, err := p.orExpr()
	if err != nil {
		return nil, err
	}
	for p.skipName("if") {
		test, err := p.orExpr()
		if err != nil {
			return nil, err
		}
		var alt jNode
		if p.skipName("else") {
			if alt, err = p.condExpr(); err != nil {
				return nil, err
			}
		}
		e = jCond{test: test, a: e, b: alt}
	}
	return e, nil
}

func (p *jParser) orExpr() (jNode, error) {
	l, err := p.andExpr()
	for err == nil && p.skipName("or") {
		var r jNode
		r, err = p.andExpr()
		l = jBin{op: "or", l: l, r: r}
	}
	return l, err
}

func (p *jParser) andExpr() (jNode, error) {
	l, err := p.notExpr()
	for err == nil && p.skipName("and") {
		var r jNode
		r, err = p.notExpr()
		l = jBin{op: "and", l: l, r: r}
	}
	return l, err
}

func (p *jParser) notExpr() (jNode, error) {
	if p.skipName("not") {
		x, err := p.notExpr()
		return jUnary{op: "not", x: x}, err
	}
	return p.compare()
}

func (p *jParser) compare() (jNode, error) {
	l, err := p.math1()
	for err == nil {
		var op string
		switch {
		case p.isOp("==") || p.isOp("!=") || p.isOp("<") || p.isOp(">") || p.isOp("<=") || p.isOp(">="):
			op = p.next().val
		case p.isName("in"):
			p.next()
			op = "in"
		case p.isName("not") && p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == "name" && p.toks[p.pos+1].val == "in":
			p.pos += 2
			op = "not in"
		default:
			return l, nil
		}
		var r jNode
		r, err = p.math1()
		l = jBin{op: op, l: l, r: r}
	}
	return l, err
}

func (p *jParser) math1() (jNode, error) {
	l, err := p.concat()
	for err == nil && (p.isOp("+") || p.isOp("-")) {
		op := p.next().val
		var r jNode
		r, err = p.concat()
		l = jBin{op: op, l: l, r: r}
	}
	return l, err
}

func (p *jParser) concat() (jNode, error) {
	l, err := p.math2()
	for err == nil && p.isOp("~") {
		p.next()
		var r jNode
		r, err = p.math2()
		l = jBin{op: "~", l: l, r: r}
	}
	return l, err
}

func (p *jParser) math2() (jNode, error) {
	l, err := p.pow()
	for err == nil && (p.isOp("*") || p.isOp("/") || p.isOp("//") || p.isOp("%")) {
		op := p.next().val
		var r jNode
		r, err = p.pow()
		l = jBin{op: op, l: l, r: r}
	}
	return l, err
}

func (p *jParser) pow() (jNode, error) {
	l, err := p.unary(true)
	for err == nil && p.isOp("**") {
		p.next()
		var r jNode
		r, err = p.unary(true)
		l = jBin{op: "**", l: l, r: r}
	}
	return l, err
}

func (p *jParser) unary(withFilter bool) (jNode, error) {
	var n jNode
	var err error
	switch {
	case p.isOp("-") || p.isOp("+"):
		op := p.next().val
		x, xerr := p.unary(false)
		if xerr != nil {
			return nil, xerr
		}
		n = jUnary{op: op, x: x}
	default:
		if n, err = p.primary(); err != nil {
			return nil, err
		}
	}
	if n, err = p.postfix(n); err != nil {
		return nil, err
	}
	if withFilter {
		return p.filterExpr(n)
	}
	return n, nil
}

func (p *jParser) primary() (jNode, error) {
	t := p.next()
	switch t.kind {
	case "name":
		switch t.val {
		case "true", "True":
			return jConst{v: true}, nil
		case "false", "False":
			return jConst{v: false}, nil
		case "none", "None":
			return jConst{v: nil}, nil
		}
		return jName{name: t.val}, nil
	case "string":
		var b strings.Builder
		b.WriteString(t.val)
		for p.peek().kind == "string" {
			b.WriteString(p.next().val)
		}
		return jConst{v: b.String()}, nil
	case "int":
		v, err := strconv.ParseInt(t.val, 0, 64)
		if err != nil {
			return nil, jinjaErr("bad integer %q", t.val)
		}
		return jConst{v: v}, nil
	case "float":
		v, err := strconv.ParseFloat(t.val, 64)
		if err != nil {
			return nil, jinjaErr("bad number %q", t.val)
		}
		return jConst{v: v}, nil
	case "op":
		switch t.val {
		case "(":
			if p.skipOp(")") {
				return jList{}, nil
			}
			e, err := p.expression(true)
			if err != nil {
				return nil, err
			}
			return e, p.expectOp(")")
		case "[":
			return p.listLiteral()
		case "{":
			return p.dictLiteral()
		}
	}
	return nil, jinjaErr("unexpected %q", t.val)
}

// listLiteral reads `[a, b, …]` after its `[`.
func (p *jParser) listLiteral() (jNode, error) {
	var items []jNode
	for !p.isOp("]") {
		e, err := p.expression(false)
		if err != nil {
			return nil, err
		}
		items = append(items, e)
		if !p.skipOp(",") {
			break
		}
	}
	return jList{items: items}, p.expectOp("]")
}

// dictLiteral reads `{k: v, …}` after its `{`.
func (p *jParser) dictLiteral() (jNode, error) {
	var d jDict
	for !p.isOp("}") {
		k, err := p.expression(false)
		if err != nil {
			return nil, err
		}
		if err = p.expectOp(":"); err != nil {
			return nil, err
		}
		v, err := p.expression(false)
		if err != nil {
			return nil, err
		}
		d.keys, d.vals = append(d.keys, k), append(d.vals, v)
		if !p.skipOp(",") {
			break
		}
	}
	return d, p.expectOp("}")
}

func (p *jParser) postfix(n jNode) (jNode, error) {
	for {
		switch {
		case p.skipOp("."):
			t := p.next()
			switch t.kind {
			case "name":
				n = jGetattr{obj: n, attr: t.val}
			case "int":
				v, _ := strconv.ParseInt(t.val, 10, 64)
				n = jGetitem{obj: n, idx: jConst{v: v}}
			default:
				return nil, jinjaErr("bad attribute %q", t.val)
			}
		case p.skipOp("["):
			idx, err := p.subscript()
			if err != nil {
				return nil, err
			}
			n = jGetitem{obj: n, idx: idx}
		case p.isOp("("):
			args, err := p.callArgs()
			if err != nil {
				return nil, err
			}
			n = jCall{fn: n, args: args}
		default:
			return n, nil
		}
	}
}

func (p *jParser) subscript() (jNode, error) {
	var parts [3]jNode
	slice := false
	for k := range 3 {
		if !p.isOp(":") && !p.isOp("]") {
			e, err := p.expression(false)
			if err != nil {
				return nil, err
			}
			parts[k] = e
		}
		if !p.skipOp(":") {
			break
		}
		slice = true
	}
	if err := p.expectOp("]"); err != nil {
		return nil, err
	}
	if slice {
		return jSlice{a: parts[0], b: parts[1], c: parts[2]}, nil
	}
	return parts[0], nil
}

func (p *jParser) callArgs() (jArgs, error) {
	var a jArgs
	if err := p.expectOp("("); err != nil {
		return a, err
	}
	for !p.isOp(")") {
		switch {
		case p.skipOp("**"), p.skipOp("*"):
			e, err := p.expression(false)
			if err != nil {
				return a, err
			}
			a.dyn = append(a.dyn, e)
		case p.peek().kind == "name" && p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == "op" && p.toks[p.pos+1].val == "=":
			name := p.next().val
			p.next()
			e, err := p.expression(false)
			if err != nil {
				return a, err
			}
			a.kwName, a.kwVal = append(a.kwName, name), append(a.kwVal, e)
		default:
			e, err := p.expression(false)
			if err != nil {
				return a, err
			}
			a.pos = append(a.pos, e)
		}
		if !p.skipOp(",") {
			break
		}
	}
	return a, p.expectOp(")")
}

// filterExpr is parse_filter_expr: `| filter` and `is [not] test`, in turn.
func (p *jParser) filterExpr(n jNode) (jNode, error) {
	for {
		switch {
		case p.skipOp("|"):
			f, err := p.filterTail(n)
			if err != nil {
				return nil, err
			}
			n = f
		case p.skipName("is"):
			neg := p.skipName("not")
			name, err := p.dottedName()
			if err != nil {
				return nil, err
			}
			var args jArgs
			switch {
			case p.isOp("("):
				if args, err = p.callArgs(); err != nil {
					return nil, err
				}
			case p.startsTestArg():
				arg, aerr := p.unary(false)
				if aerr == nil {
					arg, aerr = p.postfix(arg)
				}
				if aerr != nil {
					return nil, aerr
				}
				args.pos = []jNode{arg}
			}
			var x jNode = jTest{arg: n, name: name, args: args}
			if neg {
				x = jUnary{op: "not", x: x}
			}
			n = x
		default:
			return n, nil
		}
	}
}

// startsTestArg: a test's single argument without parentheses (`is
// divisibleby 3`) — any primary but the keywords that end the expression.
func (p *jParser) startsTestArg() bool {
	t := p.peek()
	switch t.kind {
	case "string", "int", "float":
		return true
	case "name":
		switch t.val {
		case "else", "or", "and", "is", "in", "if", "not":
			return false
		}
		return true
	case "op":
		return t.val == "[" || t.val == "{" || t.val == "-" || t.val == "+"
	}
	return false
}

func (p *jParser) dottedName() (string, error) {
	t := p.next()
	if t.kind != "name" {
		return "", jinjaErr("expected a name, found %q", t.val)
	}
	var name strings.Builder
	name.WriteString(t.val)
	for p.isOp(".") && p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == "name" {
		p.next()
		name.WriteString("." + p.next().val)
	}
	return name.String(), nil
}

func (p *jParser) filterTail(arg jNode) (jFilter, error) {
	name, err := p.dottedName()
	if err != nil {
		return jFilter{}, err
	}
	f := jFilter{arg: arg, name: name}
	if p.isOp("(") {
		if f.args, err = p.callArgs(); err != nil {
			return jFilter{}, err
		}
	}
	return f, nil
}
