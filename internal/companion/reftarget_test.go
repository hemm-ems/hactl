package companion

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRefRoutesRefuseATargetThatIsNotAWholeToken pins the rule the companion's
// own matcher states as its premise. `_target_pattern` builds
// `\b` + re.escape(target) + `\b` and its docstring says why that is the right
// notion of "whole token": "every entity_id-shaped target starts and ends on a
// word character". For a target that does not, the two `\b` bind to the
// NEIGHBOURING characters instead, so the pattern means the opposite of what the
// caller typed — `\b\.\b` matches a dot with word characters on both sides,
// which is every `light.turn_on` and every entity_id in the tree.
//
// Measured on the reference instance 2026-07-31, against the WP6 binary:
// `ref scan .` returned 2747 config hits (capped to a screenful with a hint
// about filters the command does not have), `ref scan -` returned 136, and
// `ref replace . X` planned 2747 rewrites of real config files — a dry run one
// --confirm away from rewriting every id in the instance.
//
// The check belongs at the client because that is the one place every caller of
// these routes passes through: `ref scan`, `ref replace`, `ent rename`'s
// reference count, and whatever calls them next. A CLI-side copy would be a
// second rule to keep in step with this one.
func TestRefRoutesRefuseATargetThatIsNotAWholeToken(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"target":"x","hits":[],"status":"dry_run","changes":[]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	refused := []string{".", "-", "..", "[", "{{", "'sensor.x'", "sensor.x.", ".turn_on", " sensor.x"}
	for _, target := range refused {
		t.Run("scan_"+target, func(t *testing.T) {
			_, err := c.RefScan(context.Background(), target)
			var bad *InvalidRefTargetError
			if !errors.As(err, &bad) {
				t.Fatalf("target %q is not a whole token and must be refused, got %v", target, err)
			}
			// The refusal has to teach: it names the boundary rule and where to
			// go instead, because "invalid target" would send the caller
			// looking for a typo in an id they typed correctly.
			for _, want := range []string{"whole token", "ent ls --pattern"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal for %q missing %q: %v", target, want, err)
				}
			}
		})
		t.Run("replace_"+target, func(t *testing.T) {
			_, err := c.RefReplace(context.Background(), target, "sensor.new", true)
			if !errors.As(err, new(*InvalidRefTargetError)) {
				t.Fatalf("replacing %q must be refused before the wire, got %v", target, err)
			}
			// The replacement side takes the same rule: `ref replace sensor.x .`
			// would write a bare dot into every position the id held.
			_, err = c.RefReplace(context.Background(), "sensor.old", target, true)
			if !errors.As(err, new(*InvalidRefTargetError)) {
				t.Fatalf("renaming TO %q must be refused before the wire, got %v", target, err)
			}
		})
	}

	accepted := []string{"sensor.x", "a", "input_boolean.pg_core_flag_a", "Wozi TV", "3f8a_id", "sensor.a-b"}
	for _, target := range accepted {
		t.Run("accepted_"+target, func(t *testing.T) {
			if _, err := c.RefScan(context.Background(), target); err != nil {
				t.Fatalf("target %q starts and ends on a word character and must be accepted: %v", target, err)
			}
		})
	}
	if requests != len(accepted) {
		t.Errorf("only the accepted targets may reach the wire: %d requests for %d accepted targets",
			requests, len(accepted))
	}
}

// TestValidRefTargetMatchesTheCompanionsPremise is the rule stated as data, so
// the boundary case that decides it is written down rather than implied: it is
// the FIRST and LAST character that must be a word character, not every
// character — a display name with spaces ("Wozi TV") is a legitimate scan target
// and a hyphenated id is a legitimate replacement, while a quoted or bracketed
// one is the caller pasting syntax around the thing they meant.
func TestValidRefTargetMatchesTheCompanionsPremise(t *testing.T) {
	for target, want := range map[string]bool{
		"sensor.x": true,
		"x":        true,
		"_":        true,
		"9":        true,
		"a b":      true,
		"a-b":      true,
		// A non-ASCII letter is a word character to Python's `\b` as well, so a
		// German display name is a real target and not an unfamiliar one to
		// refuse: `dash grep`/`ref scan` match whole dashboard values.
		"Küche":       true,
		".":           false,
		"-x":          false,
		"x-":          false,
		"\"sensor.x\"": false,
		"":            false,
	} {
		if got := ValidRefTarget(target); got != want {
			t.Errorf("ValidRefTarget(%q) = %v, want %v", target, got, want)
		}
	}
}
