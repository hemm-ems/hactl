package policy_test

// Spec tests for grammar edges of the manual's "Resources" table and D-68 that
// the earlier vectors leave open: a trailing empty sub-resource segment, the
// single segment below system:reload, a system id glob that matches reload,
// and the helper charset without '.'.

import (
	"errors"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// assertBrokenPattern loads a file whose one deny rule (line 7) names pattern
// and wants a broken file with a problem on that line.
func assertBrokenPattern(t *testing.T, pattern, why string) {
	t.Helper()
	body := "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      \"" + pattern + "\": [read]\n"
	_, err := policy.Load(writePolicy(t, body))
	var le *policy.LoadError
	if !errors.As(err, &le) {
		t.Errorf("pattern %q loaded (err %v), want a broken file: %s", pattern, err, why)
		return
	}
	for _, p := range le.Problems {
		if p.Line == 7 {
			return
		}
	}
	t.Errorf("pattern %q: problems %+v, want one on line 7 (%s)", pattern, le.Problems, why)
}

func assertValidPattern(t *testing.T, pattern string) {
	t.Helper()
	body := "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      \"" + pattern + "\": [read]\n"
	if _, err := policy.Load(writePolicy(t, body)); err != nil {
		t.Errorf("pattern %q refused (%v), want it valid", pattern, err)
	}
}

// TestATrailingEmptySubSegmentIsRefused — an empty segment is refused
// anywhere, the last one included: `attr/` is not the attribute named "".
func TestATrailingEmptySubSegmentIsRefused(t *testing.T) {
	for _, in := range []string{
		"entity:light.x/attr/",
		"dashboard:main/views/",
		"file:a.yaml/block/",
	} {
		if r, err := policy.ParseResource(in); err == nil {
			t.Errorf("ParseResource(%q) = %+v, want a refusal (empty segment)", in, r)
		}
		assertBrokenPattern(t, in, "empty segment")
	}
}

// TestSystemReloadTakesExactlyOneSegment — `<domain>` is below `reload` only,
// as a single segment.
func TestSystemReloadTakesExactlyOneSegment(t *testing.T) {
	if _, err := policy.ParseResource("system:reload/automation"); err != nil {
		t.Errorf("ParseResource(system:reload/automation) = %v, want it accepted", err)
	}
	for _, in := range []string{"system:reload/automation/x", "system:reload/a/b/c"} {
		if r, err := policy.ParseResource(in); err == nil {
			t.Errorf("ParseResource(%q) = %+v, want a refusal (one segment below reload)", in, r)
		}
		assertBrokenPattern(t, in, "one segment below reload")
	}
}

// TestASystemIDGlobMatchingReloadMayCarryASub — a system id that is a glob
// stands for the set it matches; when that set includes reload it may carry
// reload's domain segment. A glob that cannot match reload has no sub.
func TestASystemIDGlobMatchingReloadMayCarryASub(t *testing.T) {
	for _, ok := range []string{"system:re*/automation", "system:*/automation", "system:r?load/script"} {
		assertValidPattern(t, ok)
	}
	for _, bad := range []string{"system:log*/x", "system:logs/x", "system:check_*/x"} {
		assertBrokenPattern(t, bad, "no sub-resource below a system id other than reload")
	}
}

// TestAHelperIDRefusesDotAndDash — D-68: a helper id is `<domain>/<id>`, each
// segment ASCII letters, digits and _; '.' is allowed only where the kind has
// a domain written with a dot (entity, service), never in a helper id.
func TestAHelperIDRefusesDotAndDash(t *testing.T) {
	for _, in := range []string{
		"helper:input_boolean/guest.mode",
		"helper:input.boolean/guest_mode",
		"helper:input_boolean/guest-mode",
		"helper:input-boolean/guest_mode",
	} {
		if r, err := policy.ParseResource(in); err == nil {
			t.Errorf("ParseResource(%q) = %+v, want a refusal (D-68 helper charset)", in, r)
		}
		assertBrokenPattern(t, in, "D-68 helper charset")
	}
	assertBrokenPattern(t, "helper:input_*/guest.*", "D-68 helper charset, wildcards aside")
}
