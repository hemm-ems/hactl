//go:build livefire

package livefire

import (
	"encoding/json"
	"testing"
)

// Finding #30: `device ls`/`device show` ignore name_by_user.
//
// The shape: a device the user renamed carries TWO names — HA keeps the
// integration's `name` and stores the override in `name_by_user`, and the UI
// shows the override. 17 of 307 devices on the reference instance are in this
// state, e.g. `Wozi-Yeelight10` shown to its owner as "Wohnzimmer Tisch Licht
// Pendelleuchte".
//
// Reproducing on the real instance corrected the report, which said the
// name lookup missed RENAMED devices. It is the other way round, and the two
// halves are swapped:
//
//	--name Pendelleuchte  (only in name_by_user) -> 1 hit
//	--name Yeelight10     (only in the registry name) -> 0 hits
//
// So the listing renders `name` and ignores the rename, while `--name` matches
// only `name_by_user` and cannot find the device by the name the integration
// gave it. Each site picks exactly one of the two names, and a user who knows
// either one is failed by one of them.
func TestSweepRenamedDeviceIsFoundAndShownByBothNames(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		renamed, original := "Kitchen Pendant Light", "Acme Widget 4711"
		if tgt.Profile == Live {
			renamed, original = "Pendelleuchte", "Yeelight10"
		}

		// The listing shows the name its owner gave the device.
		out := tgt.MustRead(t, "device", "ls", "--name", renamed, "--json")
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("device ls --json: %v\n%s", err, truncate(out))
		}
		if len(rows) == 0 {
			t.Fatalf("--name %q found nothing; the rename is invisible to the lookup", renamed)
		}
		if name, _ := rows[0]["name"].(string); name == "" || !containsFold(name, renamed) {
			t.Errorf("listing shows %q for a device its owner renamed to %q — the stale registry name",
				name, renamed)
		}

		// And the device is still reachable by the name the integration gave it.
		byOriginal := tgt.MustRead(t, "device", "ls", "--name", original, "--json")
		var originalRows []map[string]any
		if err := json.Unmarshal([]byte(byOriginal), &originalRows); err != nil {
			t.Fatalf("device ls --json: %v\n%s", err, truncate(byOriginal))
		}
		if len(originalRows) == 0 {
			t.Errorf("--name %q found nothing; a renamed device is unreachable by the name it still carries",
				original)
		}
	})
}

func containsFold(haystack, needle string) bool {
	return len(needle) == 0 || indexFold(haystack, needle) >= 0
}

func indexFold(haystack, needle string) int {
	hl, nl := lower(haystack), lower(needle)
	for i := 0; i+len(nl) <= len(hl); i++ {
		if hl[i:i+len(nl)] == nl {
			return i
		}
	}
	return -1
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
