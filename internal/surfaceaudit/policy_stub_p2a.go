//go:build p2a_stub

package surfaceaudit

// PolicySurface — CONTRACT STUB for stage 2a, built only with `-tags p2a_stub`
// so the `p2a_contract` tests compile and can be watched to fail. The
// integrator deletes this file when the real extractor lands.
//
// The API the tests assume (no contract document names one):
//
//	func PolicySurface(root string, commands []Site) (Surface, error)
//
// Name "policy" (the manifest's basename). The request half is derived from
// the non-test sources under root — every sendCommand type and every direct
// conn.WriteJSON of a literal "type" (key `ws <type>`), every SupervisorAPI
// call (`ws supervisor/api <METHOD> <endpoint>`, never the bare type), every
// haapi.Client request path (`rest <METHOD> <path>`, concatenation written as
// {name} placeholders, the query dropped), every companion operation in
// companion.Endpoints that a companion.Client method sends
// (`companion <METHOD> <path>`), plus a companion.Client method that builds a
// path outside Endpoints — surfaced as a site or as an error, never silently.
// internal/hatest and internal/companiontest_discovery are left out. The
// command half is handed in, as WriteBackSurface's census is: one Site per
// write command, keyed by its cobra command path ("hactl area create"), each
// becoming `cmd <path>`. An empty command census is an error.
func PolicySurface(_ string, _ []Site) (Surface, error) {
	return Surface{Name: "policy", Rule: "contract stub: no extractor is built yet"}, nil
}
