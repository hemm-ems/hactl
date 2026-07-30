package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The registry write gates: area / floor / label create.
//
// `hactl area create "" --confirm` created a real area on a production
// instance whose `area_id` was the empty string, and every `hactl area`
// command — ls, create and delete alike — failed from that moment on, because
// H-14 fails a whole listing when one record arrives without its identity and
// `delete` has to list first. Recovery needed a raw WebSocket call; hactl
// itself could not undo what hactl had just done.
//
// The oracle (internal/integration/registry_blank_name_oracle_test.go) settles
// what HA does: it accepts an empty name and mints an empty id, it accepts a
// whitespace-only name and files it under an id it chose ("unknown"), and it
// considers the two the same name. So the refusal cannot live on the server and
// cannot live after the call — by then the damage is persisted. It lives here,
// before the wire, and it lives in the preview too: H-2 requires a preview to
// fail exactly where the confirmed run would.
// ---------------------------------------------------------------------------

// registryCreateProbe is one create command and the WS command it would send.
type registryCreateProbe struct {
	family   string
	wsCreate string
}

var registryCreateProbes = []registryCreateProbe{
	{"area", "config/area_registry/create"},
	{"floor", "config/floor_registry/create"},
	{"label", "config/label_registry/create"},
}

// blankNames are the names HA accepts and hactl must not send. "\t \n" is in
// the set because trimming only spaces would leave the same defect one
// keystroke away.
var blankNames = []string{"", "   ", "\t", "\t \n"}

// TestRegistryCreateRefusesABlankName — no blank name reaches the wire, in
// either mode, for any of the three registries.
func TestRegistryCreateRefusesABlankName(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/create":  map[string]any{"area_id": "a", "name": "a"},
		"config/floor_registry/create": map[string]any{"floor_id": "f", "name": "f"},
		"config/label_registry/create": map[string]any{"label_id": "l", "name": "l"},
	}, nil)

	for _, probe := range registryCreateProbes {
		for _, name := range blankNames {
			for _, mode := range []string{"dry-run", "confirm"} {
				t.Run(probe.family+"/"+mode+"/"+readableName(name), func(t *testing.T) {
					assertBlankNameRefused(t, ts, probe, name, mode)
				})
			}
		}
	}
}

// assertBlankNameRefused runs one create and requires it to end before the
// wire, with a message the caller can act on.
func assertBlankNameRefused(t *testing.T, ts *cmdTestServer, probe registryCreateProbe, name, mode string) {
	t.Helper()
	before := ts.commandCount(probe.wsCreate)

	args := []string{probe.family, "create", name, "--dir", ts.dir}
	if mode == "confirm" {
		args = append(args, "--confirm")
	}
	out, err := runRootCapture(t, args...)

	if err == nil {
		t.Fatalf("%s create %q (%s) was accepted; output:\n%s", probe.family, name, mode, out)
	}
	if got := ts.commandCount(probe.wsCreate); got != before {
		t.Errorf("%s create %q (%s) reached the wire (%s issued %d time(s)) — the refusal must "+
			"happen before the request, because HA persists it",
			probe.family, name, mode, probe.wsCreate, got-before)
	}
	// The message has to say what is wrong with the input and which family it
	// is about: a caller told only "invalid" retries with the same empty string.
	msg := err.Error()
	if !strings.Contains(msg, probe.family) {
		t.Errorf("refusal does not name the %s family: %q", probe.family, msg)
	}
	if !strings.Contains(msg, "name") {
		t.Errorf("refusal does not say the name is the problem: %q", msg)
	}
	if strings.Contains(out, "use --confirm") {
		t.Errorf("preview offered --confirm for a name that cannot be created:\n%s", out)
	}
}

// readableName gives a subtest a name for an unprintable one.
func readableName(name string) string {
	return strings.NewReplacer(" ", "_", "\t", "tab", "\n", "nl").Replace(name)
}

// TestRegistryCreateAcceptsANameWithSurroundingSpace is the no-false-positive
// half: the gate rejects names that are *only* blank, never a real name that
// happens to carry whitespace. Trimming the caller's name would be a different
// change with its own oracle (HA keeps the spaces), and this pins that hactl
// does not quietly make it.
func TestRegistryCreateAcceptsANameWithSurroundingSpace(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/create":  map[string]any{"area_id": "kitchen", "name": " Kitchen "},
		"config/floor_registry/create": map[string]any{"floor_id": "ground", "name": " Ground "},
		"config/label_registry/create": map[string]any{"label_id": "energy", "name": " Energy "},
	}, nil)

	for _, probe := range registryCreateProbes {
		t.Run(probe.family, func(t *testing.T) {
			before := ts.commandCount(probe.wsCreate)
			if _, err := runRootCapture(t, probe.family, "create", " Kitchen ", "--dir", ts.dir, "--confirm"); err != nil {
				t.Fatalf("%s create of a padded but real name was refused: %v", probe.family, err)
			}
			if got := ts.commandCount(probe.wsCreate); got != before+1 {
				t.Fatalf("%s create did not reach the wire", probe.family)
			}
			if sent, _ := ts.lastParams(probe.wsCreate)["name"].(string); sent != " Kitchen " {
				t.Errorf("hactl sent name %q, want the caller's %q verbatim — trimming is a "+
					"different decision and is not this gate's to make", sent, " Kitchen ")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// floor create --level 0
// ---------------------------------------------------------------------------

// TestFloorCreateSendsAnExplicitLevelZero — the level the caller gave reaches
// HA, including 0, which is the manual's own canonical example (a ground
// floor) and the single most common real value. `if level != 0` elided it, so
// the request omitted the field and HA stored null; the preview omitted the
// line too, so the dry run could not have warned anyone.
//
// The oracle (TestOracleFloorLevelZeroIsStored) pins that HA distinguishes
// absent, 0 and -1, which is what makes the distinction worth sending.
func TestFloorCreateSendsAnExplicitLevelZero(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/floor_registry/create": map[string]any{"floor_id": "ground", "name": "Ground"},
	}, nil)

	cases := []struct {
		wantLevel any
		name      string
		flags     []string
	}{
		{float64(0), "explicit zero", []string{"--level", "0"}},
		{float64(-1), "negative level", []string{"--level", "-1"}},
		{float64(2), "positive level", []string{"--level", "2"}},
		{nil, "no level flag", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"floor", "create", "Ground", "--dir", ts.dir, "--confirm"}, tc.flags...)
			if _, err := runRootCapture(t, args...); err != nil {
				t.Fatalf("floor create %v: %v", tc.flags, err)
			}
			got, present := ts.lastParams("config/floor_registry/create")["level"]
			if tc.wantLevel == nil {
				if present {
					t.Errorf("floor create with no --level sent level=%v — an absent flag must stay "+
						"absent on the wire, or every floor silently becomes level 0", got)
				}
				return
			}
			if !present {
				t.Fatalf("floor create %v sent no level at all — the flag was dropped before the request", tc.flags)
			}
			if got != tc.wantLevel {
				t.Errorf("floor create %v sent level=%#v, want %#v", tc.flags, got, tc.wantLevel)
			}
		})
	}
}

// TestFloorCreatePreviewShowsAnExplicitLevelZero — the preview states the same
// level the confirmed run would send, in text and under --json. A preview that
// omits the field an agent asked for reports a plan the caller did not make.
func TestFloorCreatePreviewShowsAnExplicitLevelZero(t *testing.T) {
	ts := startCmdServer(t, nil, nil)

	out, err := runRootCapture(t, "floor", "create", "Ground", "--level", "0", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("floor create dry-run: %v", err)
	}
	if !strings.Contains(out, "level") || !strings.Contains(out, "0") {
		t.Errorf("dry-run preview dropped `level: 0`:\n%s", out)
	}

	jsonOut, err := runRootCapture(t, "floor", "create", "Ground", "--level", "0", "--json", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("floor create dry-run --json: %v", err)
	}
	var plan struct {
		Details map[string]any `json:"details"`
	}
	if uErr := json.Unmarshal([]byte(jsonOut), &plan); uErr != nil {
		t.Fatalf("dry-run --json is not JSON: %v\n%s", uErr, jsonOut)
	}
	if got, ok := plan.Details["level"]; !ok || got != float64(0) {
		t.Errorf("dry-run --json details.level = %#v (present=%v), want 0 — H-10 makes --json the "+
			"machine contract, so an elided field is a lie told to a parser", got, ok)
	}
}

// runRootCapture drives the real cobra tree and returns its combined output.
// It resets flag state first: these tests run several invocations in one
// process, and a --confirm left set by a previous one would make the next
// assertion mean nothing.
func runRootCapture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetSubcommandFlags()
	flagDir = ""
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		resetSubcommandFlags()
	})
	err := rootCmd.Execute()
	return buf.String(), err
}
