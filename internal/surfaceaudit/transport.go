package surfaceaudit

import (
	"go/ast"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Transport surface
// ---------------------------------------------------------------------------
//
// H-23: every connection hactl opens is bounded by the caller's `--timeout`.
//
// hactl opens three kinds of connection — REST to Home Assistant, a WebSocket to
// Home Assistant, HTTP to the companion — and they were built independently, in
// three files, by three changes. Two of them read the flag. The third, the
// WebSocket, was a 5-second constant dial attempted twice behind a 10-second
// constant handshake, so `companion status --timeout 1s` against a host that
// never answers returned after 10.02s and `--timeout 3s` and `--timeout 20s`
// landed on the same 10.02s, while `health --timeout 1s` and `ent ls --timeout
// 1s` against the identical host aborted at 1.01s (#73).
//
// Nothing about that was findable from the outside. The flag was documented, two
// of three transports honoured it, and the third was in a different package from
// either. So the surface is the set of TRANSPORT CONSTRUCTIONS in the typed
// source — every place a connection's bounds are decided — and a fourth transport
// cannot be added without saying what bounds it.

// transportTypes are the composite literals whose fields ARE the bounds of a
// connection. A site is one construction of one of them.
//
// http.Transport is on the list although it carries no timeout of its own in
// this tree: it is where DialContext is installed, which is where a dial bound
// either comes from the flag or does not.
var transportTypes = map[string]string{
	"http.Client":      "an HTTP client: Timeout bounds the whole request",
	"http.Transport":   "an HTTP transport: DialContext bounds connection establishment",
	"net.Dialer":       "a dialer: Timeout bounds connection establishment",
	"websocket.Dialer": "a websocket dialer: NetDialContext and HandshakeTimeout bound the upgrade",
}

// unboundedRequestFuncs are the package-level helpers that issue a request
// through http.DefaultClient, which has no timeout at all. They are transports
// with their bound already decided — as zero — and are on this surface for the
// same reason a construction is: the decision was made somewhere.
var unboundedRequestFuncs = map[string]bool{
	"Get": true, "Post": true, "PostForm": true, "Head": true,
}

// TransportSurface is every place in the product where a connection's bounds are
// set.
//
// Rule: the bound comes from the caller's --timeout (haapi.DefaultTimeout, which
// the root command writes from the flag), directly or through the shared
// haapi.HTTPClient. A constant is a bound the caller cannot ask to be smaller.
func TransportSurface(root string) (Surface, error) {
	files, err := scanSources(root)
	if err != nil {
		return Surface{}, err
	}
	s := Surface{
		Name: "transport",
		Rule: "every connection hactl opens takes its bound from the caller's --timeout, never from a constant alone",
	}
	for _, f := range files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			key, note, ok := transportEvidence(f, n)
			if !ok {
				return true
			}
			s.Sites = append(s.Sites, Site{
				Key:  key,
				File: f.rel,
				Line: f.fset.Position(n.Pos()).Line,
				Note: note,
			})
			return true
		})
	}
	sort.Slice(s.Sites, func(i, j int) bool { return s.Sites[i].Key < s.Sites[j].Key })
	return s, nil
}

// transportEvidence reports whether a node decides the bounds of a connection.
//
// The key carries the enclosing function AND the type, because one function
// legitimately builds several: haapi.HTTPClient constructs the client, the
// transport and the dialer, and each of those is a separate decision about a
// separate bound.
func transportEvidence(f srcFile, n ast.Node) (key, note string, ok bool) {
	switch node := n.(type) {
	case *ast.CompositeLit:
		name := typeName(node.Type)
		what, known := transportTypes[name]
		if !known {
			return "", "", false
		}
		fn := enclosingFunc(f.ast, node.Pos())
		if fn == "" {
			fn = "(package level)"
		}
		return f.rel + ":" + fn + ":" + name, what, true
	case *ast.CallExpr:
		sel, isSel := node.Fun.(*ast.SelectorExpr)
		if !isSel {
			return "", "", false
		}
		pkg, isID := sel.X.(*ast.Ident)
		if !isID || pkg.Name != "http" || !unboundedRequestFuncs[sel.Sel.Name] {
			return "", "", false
		}
		fn := enclosingFunc(f.ast, node.Pos())
		if fn == "" {
			fn = "(package level)"
		}
		return f.rel + ":" + fn + ":http." + sel.Sel.Name,
			"issues a request through http.DefaultClient, which has no timeout", true
	}
	return "", "", false
}

// TransportBoundedByFlag reports whether an expression reads the per-request
// timeout the --timeout flag writes.
//
// It is exported for the gate's own use: the manifest records what each site
// does, and this answers the one question a manifest entry cannot check for
// itself — whether the value in the literal traces back to the flag at all.
func TransportBoundedByFlag(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch id := n.(type) {
		case *ast.Ident:
			if id.Name == "DefaultTimeout" || strings.HasSuffix(id.Name, "Bound") {
				found = true
			}
		case *ast.SelectorExpr:
			if id.Sel.Name == "DefaultTimeout" {
				found = true
			}
		}
		return !found
	})
	return found
}
