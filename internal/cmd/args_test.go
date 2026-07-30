package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// runCLI drives the real command tree the way a caller does and returns the
// error and the captured output. No instance is configured, so a command that
// gets past its argument contract fails with "no hactl instance configured" —
// which is exactly what makes these assertions about *ordering* rather than
// about erroring at all.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	err := RunWithOutput(append([]string{"hactl"}, args...), buf)
	return buf.String(), err
}

// TestBlankIdentifierIsRefusedBeforeTheCommandRuns is the P1 from the
// 2026-07-30 live-fire run, at the layer that decides it: `auto show ”` and
// `auto delete ”` resolved to a real restored automation because the resolver
// compares the caller's reference with `==` against fields a ghost carries
// empty. Refusing the blank string at the boundary ends the whole class,
// including the two sites nobody has reported yet.
func TestBlankIdentifierIsRefusedBeforeTheCommandRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"auto show", []string{"auto", "show", ""}},
		{"auto delete plans a deletion", []string{"auto", "delete", ""}},
		{"auto cat", []string{"auto", "cat", ""}},
		{"auto apply", []string{"auto", "apply", ""}},
		{"device show", []string{"device", "show", ""}},
		{"area create writes an empty-id area", []string{"area", "create", ""}},
		{"whitespace only", []string{"auto", "show", "   "}},
		{"tab only", []string{"ent", "hist", "\t"}},
		{"second argument", []string{"device", "set-area", "kitchen-hub", ""}},
		{"optional argument given blank", []string{"dash", "show", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCLI(t, tc.args...)
			if !errors.Is(err, errPositional) {
				t.Fatalf("hactl %s: err = %v (out %q), want the positional contract to refuse it",
					strings.Join(tc.args, " "), err, out)
			}
			if !strings.Contains(err.Error(), "is blank") {
				t.Errorf("refusal does not say what is wrong: %v", err)
			}
			if out != "" {
				t.Errorf("a refused command still wrote to stdout: %q", out)
			}
		})
	}
}

// TestBlankRefusalNamesThePlaceholder — the message points at the argument the
// caller has to fix, read from the command's own Use line rather than from a
// second list that would drift from it.
func TestBlankRefusalNamesThePlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"first of two", "(<device>)", []string{"device", "set-area", "", "kitchen"}},
		{"second of two", "(<area>)", []string{"device", "set-area", "hub", ""}},
		{"single", "(<entity_id>)", []string{"ent", "show", ""}},
		{"optional", "[url_path]", []string{"dash", "show", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCLI(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("hactl %s: err = %v, want it to name %s", strings.Join(tc.args, " "), err, tc.want)
			}
		})
	}
}

// TestUnexpectedPositionalIsRefusedWithAFlagHint — `ent ls sensor` returned the
// same unfiltered listing as `ent ls`, with no signal that "sensor" had been
// dropped. It is the most plausible mistake an LLM caller makes with this
// command, so the refusal carries the flag that does what the caller meant.
func TestUnexpectedPositionalIsRefusedWithAFlagHint(t *testing.T) {
	out, err := runCLI(t, "ent", "ls", "sensor")
	if !errors.Is(err, errPositional) {
		t.Fatalf("ent ls sensor: err = %v (out %q), want a refusal", err, out)
	}
	if !strings.Contains(err.Error(), "--domain sensor") {
		t.Errorf("refusal offers no usable alternative: %v", err)
	}
	if out != "" {
		t.Errorf("a refused listing still produced output: %q", out)
	}
}

// TestEveryListingRefusesAPositional covers the sibling listings the same
// finding names — `auto ls`, `device ls` — plus a listing with no filter flags
// at all, where the hint is legitimately absent but the refusal is not.
func TestEveryListingRefusesAPositional(t *testing.T) {
	for _, args := range [][]string{
		{"auto", "ls", "kitchen"},
		{"device", "ls", "kitchen"},
		{"script", "ls", "kitchen"},
		{"helper", "ls", "kitchen"},
		{"area", "ls", "kitchen"},
		{"label", "ls", "kitchen"},
		{"floor", "ls", "kitchen"},
		{"dash", "ls", "kitchen"},
		{"cc", "ls", "kitchen"},
	} {
		if _, err := runCLI(t, args...); !errors.Is(err, errPositional) {
			t.Errorf("hactl %s: err = %v, want a refusal", strings.Join(args, " "), err)
		}
	}
}

// TestUnknownSubcommandUnderAFamilyIsAnError — the root command has always
// refused `hactl frobnicate`; every family group answered the same mistake with
// its own help text and exit 0, so a caller checking $? was told it worked.
func TestUnknownSubcommandUnderAFamilyIsAnError(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		expect string
	}{
		{[]string{"helper", "set"}, `unknown command "set" for "hactl helper"`},
		{[]string{"dash", "frobnicate"}, `unknown command "frobnicate" for "hactl dash"`},
		{[]string{"area", "rename"}, `unknown command "rename" for "hactl area"`},
		{[]string{"log", "bogus"}, `unknown command "bogus" for "hactl log"`},
		{[]string{"frobnicate"}, `unknown command "frobnicate" for "hactl"`},
	} {
		out, err := runCLI(t, tc.args...)
		if !errors.Is(err, errPositional) {
			t.Errorf("hactl %s: err = %v (out %q), want a refusal", strings.Join(tc.args, " "), err, out)
			continue
		}
		if !strings.Contains(err.Error(), tc.expect) {
			t.Errorf("hactl %s: err = %q, want it to contain %q", strings.Join(tc.args, " "), err, tc.expect)
		}
		if out != "" {
			t.Errorf("hactl %s printed help to stdout while failing: %q", strings.Join(tc.args, " "), out)
		}
	}
}

// TestMistypedSubcommandKeepsItsSuggestion — cobra's did-you-mean is the part of
// the root's behaviour worth keeping, so the group refusal carries it too.
func TestMistypedSubcommandKeepsItsSuggestion(t *testing.T) {
	_, err := runCLI(t, "auto", "shwo", "x")
	if err == nil || !strings.Contains(err.Error(), "show") {
		t.Errorf("auto shwo: err = %v, want a suggestion naming `show`", err)
	}
}

// TestValidUsageIsUnchanged is the other half of a contract fix: everything
// that worked has to keep working, including the bare family invocation whose
// exit-0 help output is the reason the group defect was invisible.
func TestValidUsageIsUnchanged(t *testing.T) {
	t.Run("a bare family still prints help and succeeds", func(t *testing.T) {
		out, err := runCLI(t, "auto")
		if err != nil {
			t.Fatalf("hactl auto: %v", err)
		}
		if !strings.Contains(out, "Available Commands:") || !strings.Contains(out, "rollback") {
			t.Errorf("hactl auto did not print the family help:\n%s", out)
		}
	})
	t.Run("the root still prints help and succeeds", func(t *testing.T) {
		out, err := runCLI(t)
		if err != nil {
			t.Fatalf("hactl: %v", err)
		}
		if !strings.Contains(out, "Available Commands:") {
			t.Errorf("bare hactl did not print help:\n%s", out)
		}
	})
	for _, args := range [][]string{
		{"auto", "show", "climate_schedule"},      // a real identifier
		{"ent", "ls", "--domain", "sensor"},       // the flag form of the swallowed positional
		{"dash", "show"},                          // optional argument omitted
		{"auto", "rollback"},                      // optional argument omitted: defaults to the latest backup
		{"tpl", "eval"},                           // optional argument, supplied by -f instead
		{"ent", "set-label", "light.x", "a"},      // exactly the minimum
		{"ent", "set-label", "light.x", "a", "b"}, // more than the minimum
	} {
		if _, err := runCLI(t, args...); errors.Is(err, errPositional) {
			t.Errorf("hactl %s was refused by the positional contract: %v", strings.Join(args, " "), err)
		}
	}
}

// TestResolveAutomationRefusesABlankReference pins the resolver half of the P1
// at the site where the wrong match was made: a restored ghost carries an empty
// config id and an empty friendly_name, so `"" == ""` made it the answer.
func TestResolveAutomationRefusesABlankReference(t *testing.T) {
	// A nil client is deliberate: if the guard is removed this panics on the
	// states fetch instead of quietly resolving, so the test cannot pass for
	// the wrong reason.
	for _, ref := range []string{"", "  ", "\t"} {
		got, ok, err := resolveAutomation(context.Background(), nil, ref)
		if err != nil {
			t.Fatalf("resolveAutomation(%q): unexpected error %v", ref, err)
		}
		if ok {
			t.Errorf("resolveAutomation(%q) resolved to %s, want no match", ref, got.EntityID)
		}
	}
}

// TestResolveDeviceRefusesABlankReference — the device registry legitimately
// carries an empty `name` (a renamed device keeps its override in
// name_by_user), and the exact-match pass compared it with the caller's
// reference, so `device show ”` answered with a real, arbitrary device.
func TestResolveDeviceRefusesABlankReference(t *testing.T) {
	devices := []haapi.DeviceRegistryEntry{
		{ID: "abc123", Name: ""}, // the shape that matched the empty string
		{ID: "def456", Name: "Kitchen Hub"},
	}
	for _, ref := range []string{"", " "} {
		got, err := resolveDevice(devices, ref)
		if err == nil {
			t.Errorf("resolveDevice(%q) returned %s, want a refusal", ref, got.ID)
		}
	}
	// The control: a real reference still resolves, so the guard did not just
	// break lookup.
	if got, err := resolveDevice(devices, "kitchen hub"); err != nil || got.ID != "def456" {
		t.Errorf("resolveDevice(kitchen hub) = %v, %v; want def456", got.ID, err)
	}
}
