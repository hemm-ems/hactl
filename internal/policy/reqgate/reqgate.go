// Package reqgate is the seam between the request layer's clients
// (internal/haapi, internal/companion) and the policy enforcement that decides
// their requests (internal/policy/enforce) — INVARIANTS.md H-29, D-74.
//
// The clients cannot import the enforcement: it asks Home Assistant questions
// of its own (the live entity set, a service call's reach, the registries)
// through those very clients. So the clients ask a [Gate] they find here, and
// the command layer installs one for the length of one command invocation,
// only while a profile is in effect. With no gate installed — no policy file,
// the implicit allow-all, or a process that is not running a command — every
// client sends exactly what it sent before the policy existed (D-58).
//
// One verdict per logical request (D-74): a client calls [Gate.Check] once,
// before its retry loop, and marks the request's context with [WithChecked],
// so the shared HTTP transport's hook (haapi.HTTPClient) neither checks a
// retry again nor checks a companion request a second time as HA REST
// (R-15). A redirect hop carries the marked context but another URL; the
// transport maps it again with [Gate.CheckHop].
package reqgate

import (
	"context"
	"sync"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// Gate decides the requests of one command invocation.
type Gate interface {
	// Check decides one logical request before it is sent. A non-nil error
	// refuses it: the request must not be sent, and the error is returned to
	// the caller as is.
	Check(ctx context.Context, req reqmap.Request) error
	// CheckHop decides a redirect hop of a request Check allowed: the hop is
	// mapped again, and refused when it maps differently (D-74). It records no
	// second verdict for a hop that maps the same.
	CheckHop(ctx context.Context, orig, hop reqmap.Request) error
	// Observe hands the gate the successful answer to a request it checked,
	// so a fact this invocation established (a verified automation id, an
	// options flow's handler, a checked ref/replace dry run, a dashboard's
	// stored config) can authorise a later request of the same invocation
	// under the same policy (D-72).
	Observe(req reqmap.Request, resp []byte)
	// Filter is what the caller may decode of a successful answer to a
	// request Check allowed (W-2, D-138): the answer with the rows the policy
	// hides dropped and the parts it masks reading `<denied>`, or an error
	// refusing it when the address only the answer carries is denied. The
	// client returns the filtered bytes, never the raw ones, so no command
	// holds a denied value (H-21). Observe still sees the raw answer.
	Filter(ctx context.Context, req reqmap.Request, resp []byte) ([]byte, error)
}

type ownKey struct{}

// Own marks ctx as carrying the policy session's own questions — the live
// set, the registries, a stored dashboard, a config file a hit lies in — that
// it asks to decide a request or to filter an answer (W-2). Their reads are
// neither refused nor filtered: a write check that saw only what the profile
// may read would check less than the write touches. Only the session and the
// policy's own fact fetchers mark a context.
func Own(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownKey{}, true)
}

// IsOwn reports whether ctx carries the session's own questions.
func IsOwn(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ownKey{}).(bool)
	return v
}

var (
	mu      sync.Mutex
	current Gate
)

// Install makes g the gate every client asks, replacing any other, and
// returns the function that removes it again (a no-op once another gate was
// installed after it). Installing nil removes the gate.
func Install(g Gate) (uninstall func()) {
	mu.Lock()
	defer mu.Unlock()
	current = g
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if current == g {
			current = nil
		}
	}
}

// Current returns the installed gate, or nil when none is: the request is
// sent unchecked, exactly as without a policy (D-58).
func Current() Gate {
	mu.Lock()
	defer mu.Unlock()
	return current
}

// Checked records which logical request a context belongs to after its
// client decided it: the transport, the method and the wire path (escaped
// path plus raw query) it was decided for, and — for a companion request — the
// prefix the client hangs its routes under, so a redirect hop can be mapped
// back to a logical route.
type Checked struct {
	Request reqmap.Request
	// WirePath is the escaped path and query the decided request was built
	// with; a request on the same context with the same method and wire path
	// is a retry of it.
	WirePath string
	// Prefix is the transport prefix of a companion request (the Ingress
	// path, or the base URL's own path), "" for HA requests.
	Prefix string
}

type checkedKey struct{}

// WithChecked marks ctx as carrying a request its client already decided.
func WithChecked(ctx context.Context, c Checked) context.Context {
	return context.WithValue(ctx, checkedKey{}, c)
}

// CheckedFrom returns the mark WithChecked put on ctx.
func CheckedFrom(ctx context.Context) (Checked, bool) {
	c, ok := ctx.Value(checkedKey{}).(Checked)
	return c, ok
}
