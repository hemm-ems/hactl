package surfaceaudit

import (
	"fmt"
	"go/types"
	"maps"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// PolicyCommand is one runnable command of the live cobra tree, located by the
// function its Run or RunE holds.
type PolicyCommand struct {
	// Path is the command path, `hactl svc call`.
	Path string
	// Confirm reports whether the command carries --confirm.
	Confirm bool
	// RunFile and RunLine locate the command's Run/RunE function (the line of
	// its `func` keyword), as runtime.FuncForPC reports it.
	RunFile string
	RunLine int
}

// declaredSinks are the writes a command can reach that are not a mapped
// request: the system:cache writes (D-79), the dispatcher that runs other
// commands (`mcp`) and the instance's bootstrap write (`setup`). They are named
// here as APIs — never as commands — so a command that starts reaching one is a
// site the day it does. Keys are `<file>|<function>`, as the source spells
// them; a sink that no longer exists is an error, so the list cannot rot.
var declaredSinks = map[string]string{
	"internal/cache/store.go|(*Store).Clear":         "a system:cache write (D-79)",
	"internal/cache/timeseries.go|(*TSStore).Clear":  "a system:cache write (D-79)",
	"internal/cache/store.go|(*Store).RefreshTraces": "a system:cache write (D-79)",
	"internal/cache/store.go|(*Store).RefreshLogs":   "a system:cache write (D-79)",
	"internal/cmd/setup.go|writeInstanceEnv":         "writes the instance's .env (bootstrap)",
	"internal/cmd/root.go|RunWithOutputContext":      "runs a command of the tree in-process (dispatch)",
}

// PolicyCommandSites is the command half of the policy surface: every
// `--confirm` command, plus every command whose Run reaches a write sink
// without one — derived, never hand-listed (D-80, R-17).
//
// A write sink is a function that sends a request whose classifier row is a
// write (or that no row names — an unmapped request is mutating until it is
// mapped), or one of declaredSinks. Reachability is over the product's SSA:
// static calls, every function a reached function takes the value of (a
// closure, a method value, a function passed as an argument), and every
// method an interface call could dispatch to among the module's own types. It
// over-approximates — a command that can reach a write is a site even if the
// path is conditional — which is the direction a closure gate must err in.
func PolicyCommandSites(root string, cmds []PolicyCommand) ([]Site, error) {
	files, err := productFiles(root)
	if err != nil {
		return nil, err
	}
	reqs, err := policyRequests(files)
	if err != nil {
		return nil, err
	}
	rows := map[string]reqmap.Disposition{}
	for _, r := range reqmap.Rows() {
		rows[r.Key] = r.Disposition
	}
	sinks := maps.Clone(declaredSinks)
	for _, r := range reqs {
		if d, ok := rows[r.key]; ok && d != reqmap.Write {
			continue
		}
		sinks[r.file+"|"+r.fn] = "sends " + r.key
	}

	g, err := loadCallGraph(root)
	if err != nil {
		return nil, err
	}
	var missing []string
	for k := range declaredSinks {
		if _, ok := g.byKey[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("declared write sinks no longer exist — update declaredSinks: %s", strings.Join(missing, ", "))
	}

	var out []Site
	for _, c := range cmds {
		if c.Confirm {
			out = append(out, Site{Key: "cmd " + c.Path, File: "cobra tree", Note: "carries --confirm"})
			continue
		}
		if c.RunFile == "" {
			continue
		}
		if rel := g.rel(c.RunFile); strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			// A Run outside the module is a library's own command — cobra's
			// `completion` family, added to the tree on first Execute. It calls
			// nothing of the product's, so it can reach no write sink.
			continue
		}
		fn := g.at(c.RunFile, c.RunLine)
		if fn == nil {
			return nil, fmt.Errorf("%s: no function at %s:%d — the command's Run is not where the runtime says", c.Path, c.RunFile, c.RunLine)
		}
		reached := g.sinksReached(fn, sinks)
		if len(reached) == 0 {
			continue
		}
		note := "reaches " + strings.Join(reached, "; ")
		out = append(out, Site{Key: "cmd " + c.Path, File: g.rel(c.RunFile), Line: c.RunLine, Note: note})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// callGraph is the product's SSA, indexed for the reachability question.
type callGraph struct {
	root   string
	prog   *ssa.Program
	byKey  map[string]*ssa.Function   // `<rel file>|<name>` of every named module function
	byLine map[string][]*ssa.Function // `<abs file>:<line>` of every module function
	keyOf  map[*ssa.Function]string
	types  []types.Type // every concrete module type and its pointer
	impls  map[string][]*ssa.Function
}

func loadCallGraph(root string) (*callGraph, error) {
	cfg := &packages.Config{Mode: packages.LoadSyntax, Dir: root}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("loading the module: %w", err)
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("package %s does not load: %w", p.PkgPath, p.Errors[0])
		}
	}
	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	g := &callGraph{
		root: root, prog: prog,
		byKey: map[string]*ssa.Function{}, byLine: map[string][]*ssa.Function{},
		keyOf: map[*ssa.Function]string{}, impls: map[string][]*ssa.Function{},
	}
	for _, p := range ssaPkgs {
		if p == nil {
			continue
		}
		p.Build()
		for _, m := range p.Members {
			if t, ok := m.(*ssa.Type); ok {
				if _, isIface := t.Type().Underlying().(*types.Interface); !isIface {
					g.types = append(g.types, t.Type(), types.NewPointer(t.Type()))
				}
			}
		}
	}
	for fn := range ssautil.AllFunctions(prog) {
		if fn.Pkg == nil || !fn.Pos().IsValid() {
			continue
		}
		pos := prog.Fset.Position(fn.Pos())
		lk := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
		g.byLine[lk] = append(g.byLine[lk], fn)
		if fn.Parent() != nil {
			continue
		}
		key := g.rel(pos.Filename) + "|" + ssaName(fn)
		g.byKey[key] = fn
		g.keyOf[fn] = key
	}
	return g, nil
}

func (g *callGraph) rel(abs string) string {
	r, err := filepath.Rel(g.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(r)
}

// ssaName spells a function the way enclosingFunc spells its declaration.
func ssaName(fn *ssa.Function) string {
	if recv := fn.Signature.Recv(); recv != nil {
		return "(" + types.TypeString(recv.Type(), func(*types.Package) string { return "" }) + ")." + fn.Name()
	}
	return fn.Name()
}

// at finds the function declared at a file:line — an anonymous Run closure is
// preferred to the named function around it.
func (g *callGraph) at(file string, line int) *ssa.Function {
	fns := g.byLine[fmt.Sprintf("%s:%d", file, line)]
	sort.Slice(fns, func(i, j int) bool { return fns[i].String() < fns[j].String() })
	for _, fn := range fns {
		if fn.Parent() != nil {
			return fn
		}
	}
	if len(fns) > 0 {
		return fns[0]
	}
	return nil
}

// sinksReached walks the graph from fn and returns the sinks it reaches,
// described and sorted.
func (g *callGraph) sinksReached(start *ssa.Function, sinks map[string]string) []string {
	seen := map[*ssa.Function]bool{start: true}
	queue := []*ssa.Function{start}
	found := map[string]bool{}
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		if k, ok := g.keyOf[fn]; ok {
			if why, isSink := sinks[k]; isSink {
				_, name, _ := strings.Cut(k, "|")
				found[name+" ("+why+")"] = true
				continue
			}
		}
		for _, next := range g.callees(fn) {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	out := make([]string, 0, len(found))
	for s := range found {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// callees is everything fn can transfer control to: static callees, every
// function whose value it takes, its closures, and the module's
// implementations of every interface method it invokes.
func (g *callGraph) callees(fn *ssa.Function) []*ssa.Function {
	var out []*ssa.Function
	out = append(out, fn.AnonFuncs...)
	var ops []*ssa.Value
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if call, ok := instr.(ssa.CallInstruction); ok {
				common := call.Common()
				if callee := common.StaticCallee(); callee != nil {
					out = append(out, callee)
				}
				if common.IsInvoke() {
					out = append(out, g.implementations(common.Value.Type(), common.Method)...)
				}
			}
			ops = instr.Operands(ops[:0])
			for _, op := range ops {
				if op == nil || *op == nil {
					continue
				}
				if f, ok := (*op).(*ssa.Function); ok {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

func (g *callGraph) implementations(t types.Type, m *types.Func) []*ssa.Function {
	iface, ok := t.Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	key := t.String() + "." + m.Id()
	if fns, done := g.impls[key]; done {
		return fns
	}
	var fns []*ssa.Function
	for _, ct := range g.types {
		if !types.Implements(ct, iface) {
			continue
		}
		sel := g.prog.MethodSets.MethodSet(ct).Lookup(m.Pkg(), m.Name())
		if sel == nil {
			continue
		}
		if f := g.prog.MethodValue(sel); f != nil {
			fns = append(fns, f)
		}
	}
	g.impls[key] = fns
	return fns
}
