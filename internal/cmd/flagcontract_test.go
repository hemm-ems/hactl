package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestEveryNumericGlobalFlagStatesItsDomain derives the set that needs a domain
// from the live flag set rather than from globalFlagDomains itself, so a new
// counting or measuring global flag is red until somebody says what it accepts.
//
// Both directions are checked. A row for a flag that no longer exists is the
// same kind of rot a stale manifest entry is: the ledger has stopped describing
// the code.
func TestEveryNumericGlobalFlagStatesItsDomain(t *testing.T) {
	// The pflag types whose values can be out of range at all. A bool's domain
	// is the two values pflag parses and a string's is decided by whatever
	// consumes it (--dir by config.Load), so neither is a site.
	measured := map[string]bool{
		"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
		"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
		"float32": true, "float64": true, "duration": true,
	}

	stated := map[string]bool{}
	for _, d := range globalFlagDomains {
		if stated[d.Name] {
			t.Errorf("globalFlagDomains states --%s twice", d.Name)
		}
		stated[d.Name] = true
		if rootCmd.PersistentFlags().Lookup(d.Name) == nil {
			t.Errorf("globalFlagDomains states a domain for --%s, which the root no longer declares", d.Name)
		}
	}

	var need int
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if !measured[f.Value.Type()] {
			return
		}
		need++
		if !stated[f.Name] {
			t.Errorf("--%s is a %s and states no domain — add a row to globalFlagDomains saying which values it accepts "+
				"(H-25: a flag accepts the values it says it accepts, and never reinterprets one it cannot honour)",
				f.Name, f.Value.Type())
		}
	})
	if need == 0 {
		t.Fatal("no global flag has a measurable type — the walk has stopped matching")
	}
}

// TestGlobalFlagDomainsRefuseWhatTheyCannotHonour drives the real entry point,
// because what the rule is about is the value that reached the flag and only
// cobra's parse decides that.
//
// The legal values are asserted beside the illegal ones: without them, "refuse
// a bad bound" is satisfied by refusing every bound. They pass through the
// contract and fail on the unconfigured instance one layer down, which is what
// makes each row an assertion about ORDER rather than about erroring at all.
func TestGlobalFlagDomainsRefuseWhatTheyCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		refuse bool
		names  string // a phrase the refusal must contain
	}{
		{args: []string{"ent", "ls", "--top", "-1"}, refuse: true, names: "--top counts the rows"},
		{args: []string{"ent", "ls", "--top", "-999"}, refuse: true, names: "--top counts the rows"},
		{args: []string{"ent", "ls", "--top", "0"}, refuse: false},
		{args: []string{"ent", "ls", "--top", "10"}, refuse: false},
		{args: []string{"ent", "ls", "--tokensmax", "-5"}, refuse: true, names: "--tokensmax counts the tokens"},
		{args: []string{"ent", "ls", "--tokensmax", "0"}, refuse: false},
		{args: []string{"health", "--timeout", "0s"}, refuse: true, names: "bounds every connection"},
		{args: []string{"health", "--timeout", "-1s"}, refuse: true, names: "bounds every connection"},
		{args: []string{"health", "--timeout", "1ns"}, refuse: false},
		{args: []string{"health", "--timeout", "5s"}, refuse: false},
	} {
		out, err := runCLI(t, tc.args...)
		got := errors.Is(err, errFlagContract)
		if got != tc.refuse {
			t.Errorf("hactl %s: refused=%v, want %v (err %v)", strings.Join(tc.args, " "), got, tc.refuse, err)
			continue
		}
		if !tc.refuse {
			continue
		}
		if !strings.Contains(err.Error(), tc.names) {
			t.Errorf("hactl %s: refusal %q does not say why", strings.Join(tc.args, " "), err)
		}
		if out != "" {
			t.Errorf("hactl %s wrote %.60q to stdout while refusing", strings.Join(tc.args, " "), out)
		}
	}
}

// TestATimeoutThatCannotBoundNeverReachesATransport is the sharp half of the
// domain rule, and the reason documenting `0 = unlimited` was not enough.
//
// `--timeout -1s` used to be installed as haapi.DefaultTimeout and arrive at
// net.Dialer as a deadline already in the past, so hactl reported `dial tcp:
// lookup <host>: i/o timeout` — a network failure invented out of a flag value,
// against a host that was up (#56). The check therefore runs in
// PersistentPreRunE, before the value is installed; this asserts the ordering
// by reading the package variable the transports read.
func TestATimeoutThatCannotBoundNeverReachesATransport(t *testing.T) {
	setFlagForTest(t, &flagTimeout, defaultTimeout)
	before := haapiDefaultTimeout()

	if _, err := runCLI(t, "health", "--timeout", "-1s"); !errors.Is(err, errFlagContract) {
		t.Fatalf("hactl health --timeout -1s: %v, want the flag contract to refuse it", err)
	}
	if after := haapiDefaultTimeout(); after != before {
		t.Errorf("a refused --timeout still reached the transports: DefaultTimeout is %s, was %s", after, before)
	}
	if after := haapiDefaultTimeout(); after <= 0 {
		t.Errorf("DefaultTimeout is %s — a non-positive bound reached the transports", after)
	}
}

// TestSinceIsDeclaredOnlyOnTheCommandsThatReadIt closes the structural half:
// the tree's `--since` declarations and sinceCommands are one set.
//
// It was 112 commands and nine readers. The gate is written over the live tree
// rather than over the list, so a command that grows the flag by hand — or an
// accidental return of the flag to the root's persistent set — fails here.
func TestSinceIsDeclaredOnlyOnTheCommandsThatReadIt(t *testing.T) {
	want := map[string]bool{}
	for _, c := range sinceCommands() {
		want[c.CommandPath()] = true
	}
	if len(want) != len(sinceCommands()) {
		t.Fatal("sinceCommands lists a command twice")
	}

	var declared []string
	walkCommandTree(rootCmd, func(c *cobra.Command) {
		if declaresFlag(c, "since", "") {
			declared = append(declared, c.CommandPath())
		}
	})
	sort.Strings(declared)
	if len(declared) == 0 {
		t.Fatal("no command declares --since — the walk has stopped matching")
	}
	for _, path := range declared {
		if !want[path] {
			t.Errorf("%s declares --since but is not in sinceCommands, so nothing proves it reads the window", path)
		}
		delete(want, path)
	}
	for path := range want {
		t.Errorf("sinceCommands names %s, which does not declare --since", path)
	}

	if rootCmd.PersistentFlags().Lookup("since") != nil {
		t.Error("--since is a root persistent flag again: it would be declared on every command in the tree, " +
			"which is the state #54 reported")
	}
}

// TestEveryCommandDeclaringSinceReadsIt is the behavioural half, and the reason
// sinceWindow exists at all: the declaration list is PROVEN to be the
// consumption list rather than asserted to be.
//
// Each of the nine is driven through the real entry point against the shared
// contract fixture with an explicit `--since`, and has to have consulted the
// window. `log` and `cc logs` read it only when the caller passes it — an unset
// --since deliberately leaves HA's whole buffer alone — which is why the flag is
// explicit here rather than defaulted.
func TestEveryCommandDeclaringSinceReadsIt(t *testing.T) {
	fixture := buildContractFixture(t)
	posArgs := contractPosArgs(fixture)

	for _, c := range sinceCommands() {
		path := strings.Join(cmdArgsOf(c), " ")
		extra, ok := posArgs[path]
		if !ok {
			t.Errorf("%s declares --since and has no fixture in contractPosArgs — nothing can drive it", path)
			continue
		}
		args := append([]string{"hactl", "--dir", fixture.dir}, cmdArgsOf(c)...)
		args = append(args, extra...)
		args = append(args, "--since", "1h")

		out, err := runArgs(t, args)
		if !sinceWasRead {
			t.Errorf("%s declares --since and never read the window (err %v, out %.80q) — "+
				"a flag a command does not act on is not a flag that command may declare", path, err, out)
		}
	}

	// The control. sinceWasRead is package state, so without a command that
	// leaves it false every row above would pass on a stale true — which is
	// the one way an instrumented gate can prove nothing while looking green.
	if _, err := runArgs(t, []string{"hactl", "--dir", fixture.dir, "area", "ls"}); err != nil {
		t.Fatalf("area ls against the fixture: %v", err)
	}
	if sinceWasRead {
		t.Error("area ls consulted the --since window — either the reset is not running, or a command that " +
			"cannot declare the flag is reading it anyway")
	}
}

// TestACommandThatCannotActOnSinceRefusesItAndSaysWhereItLives quantifies the
// other direction over the whole tree: for every command that is NOT one of the
// nine, `--since 1h` ends the command.
//
// The refusal has to come from the flag contract — no instance is configured
// here, so a command that got as far as talking to Home Assistant would fail
// with a different error and be caught. And it has to carry the flag's address:
// "unknown flag" alone would make a caller's next move a guess, which is
// precisely the asymmetry #48 reported one token to the left.
func TestACommandThatCannotActOnSinceRefusesItAndSaysWhereItLives(t *testing.T) {
	consumes := map[string]bool{}
	for _, c := range sinceCommands() {
		consumes[c.CommandPath()] = true
	}

	var checked int
	walkCommandTree(rootCmd, func(c *cobra.Command) {
		if consumes[c.CommandPath()] {
			return
		}
		checked++
		args := append(cmdArgsOf(c), "--since", "1h")
		out, err := runCLI(t, args...)
		if !errors.Is(err, errFlagContract) {
			t.Errorf("hactl %s: err = %v (out %.60q), want the flag contract to refuse --since",
				strings.Join(args, " "), err, out)
			return
		}
		if !strings.Contains(err.Error(), "it is declared by:") {
			t.Errorf("hactl %s: refusal %q does not say where --since lives", strings.Join(args, " "), err)
		}
		if out != "" {
			t.Errorf("hactl %s wrote %.60q to stdout while refusing", strings.Join(args, " "), out)
		}
	})
	if checked < 90 {
		t.Fatalf("only %d commands were checked — the walk has stopped matching", checked)
	}
}

// TestUnknownFlagOffersTheNearestFlagTheCommandTakes is #48: a mistyped flag
// gets the help a mistyped command has always got.
//
// The contrast row is the whole point. `hactl ento ls` has answered with cobra's
// "Did you mean this?" block for as long as the tree has existed, and
// `hactl ent ls --tpo` answered with four words — so a caller correcting itself
// got a strictly weaker signal for the half of the command line it is more
// likely to get wrong.
func TestUnknownFlagOffersTheNearestFlagTheCommandTakes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"ent", "ls", "--tpo", "5"}, "did you mean --top?"},
		{[]string{"ent", "ls", "--jso"}, "did you mean --json?"},
		{[]string{"ent", "ls", "--patern", "x"}, "did you mean --pattern?"},
		{[]string{"ent", "ls", "--dmain", "light"}, "did you mean --domain?"},
		{[]string{"health", "--timout", "5s"}, "did you mean --timeout?"},
	} {
		out, err := runCLI(t, tc.args...)
		if err == nil {
			t.Errorf("hactl %s was accepted", strings.Join(tc.args, " "))
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("hactl %s: %q does not offer %q", strings.Join(tc.args, " "), err, tc.want)
		}
		// pflag's own words stay the first line: they are what a caller
		// matching on hactl's output already sees, and the help is an addition.
		if !strings.HasPrefix(err.Error(), "unknown flag: --") {
			t.Errorf("hactl %s: %q no longer begins with pflag's own message", strings.Join(tc.args, " "), err)
		}
		if out != "" {
			t.Errorf("hactl %s wrote %.60q to stdout while refusing", strings.Join(tc.args, " "), out)
		}
	}

	// The control: a flag nothing in the tree resembles gets no invented
	// suggestion. Without it, "offer the nearest" is satisfied by offering
	// something for everything.
	_, err := runCLI(t, "ent", "ls", "--zzzzqqqq")
	if err == nil {
		t.Fatal("hactl ent ls --zzzzqqqq was accepted")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("a flag resembling nothing was answered with a suggestion: %q", err)
	}
}

// TestUnknownFlagNameReadsPflagsOwnErrors guards the one place this file
// depends on somebody else's wording.
//
// pflag reports an unknown flag as a plain error, so the message is the only
// carrier. Both forms are produced BY pflag here rather than written out as
// literals, so a wording change upstream fails this test instead of silently
// turning every suggestion off — the shape a closure gate is most exposed to.
func TestUnknownFlagNameReadsPflagsOwnErrors(t *testing.T) {
	set := pflag.NewFlagSet("probe", pflag.ContinueOnError)
	set.SetOutput(&strings.Builder{})
	set.String("known", "", "")

	long := set.Parse([]string{"--nosuchflag"})
	if long == nil {
		t.Fatal("pflag accepted --nosuchflag")
	}
	if name, short := unknownFlagName(long); name != "nosuchflag" || short != "" {
		t.Errorf("unknownFlagName(%q) = (%q, %q), want (%q, \"\")", long, name, short, "nosuchflag")
	}

	short := set.Parse([]string{"-q"})
	if short == nil {
		t.Fatal("pflag accepted -q")
	}
	if name, sh := unknownFlagName(short); name != "" || sh != "q" {
		t.Errorf("unknownFlagName(%q) = (%q, %q), want (\"\", %q)", short, name, sh, "q")
	}

	if name, sh := unknownFlagName(errors.New("some other failure")); name != "" || sh != "" {
		t.Errorf("unknownFlagName read a flag out of an unrelated error: (%q, %q)", name, sh)
	}
}

// TestEditDistance pins the measure the suggestions are built on, including the
// two shapes the reported typos take: a transposition (`tpo`) and a truncation
// (`jso`).
func TestEditDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"top", "top", 0},
		{"tpo", "top", 2},
		{"jso", "json", 1},
		{"", "json", 4},
		{"json", "", 4},
		{"pattern", "patern", 1},
		{"füll", "full", 1}, // runes, not bytes
	} {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestVersionFlagAndVersionCommandAgreeInBothModes is #13: two spellings of one
// question, one answer.
//
// `hactl --version --json` printed the plain banner in either flag order while
// `hactl version --json` printed JSON, because cobra answers its built-in
// version flag from a template string that cannot read a flag. The plain form
// is asserted too — the Homebrew formula's `test do` runs it (homebrew-tap/
// hactl.rb, mirrored from .goreleaser.yaml).
func TestVersionFlagAndVersionCommandAgreeInBothModes(t *testing.T) {
	for _, mode := range [][]string{nil, {"--json"}} {
		flagForm := runOut(t, append([]string{"hactl", "--version"}, mode...))
		// Order must not decide it either: the reported symptom was identical
		// for `--version --json` and `--json --version`.
		reversed := runOut(t, append(append([]string{"hactl"}, mode...), "--version"))
		cmdForm := runOut(t, append([]string{"hactl", "version"}, mode...))

		switch {
		case flagForm == "":
			t.Errorf("hactl --version %v printed nothing", mode)
		case flagForm != cmdForm:
			t.Errorf("hactl --version %v printed %.80q, hactl version %v printed %.80q — "+
				"two spellings of one question must answer the same", mode, flagForm, mode, cmdForm)
		case flagForm != reversed:
			t.Errorf("flag order changed the answer: %.80q vs %.80q", flagForm, reversed)
		}
	}

	if !strings.HasPrefix(runOut(t, []string{"hactl", "--version"}), "hactl ") {
		t.Error("hactl --version no longer starts with \"hactl \" — the Homebrew formula's test runs this form")
	}
	if !strings.HasPrefix(strings.TrimSpace(runOut(t, []string{"hactl", "--version", "--json"})), "{") {
		t.Error("hactl --version --json is not a JSON document")
	}
}

// runArgs drives the real entry point with a complete argv, including the
// binary name — runCLI's counterpart for the cases that need a global flag
// AHEAD of the subcommand (`hactl --json --version`), which the caller cannot
// express by appending.
func runArgs(t *testing.T, args []string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	err := RunWithOutput(args, buf)
	return buf.String(), err
}

// runOut runs args through the real entry point and returns stdout, failing on
// an error: every caller here asserts on an ANSWER.
func runOut(t *testing.T, args []string) string {
	t.Helper()
	out, err := runArgs(t, args)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out
}

// haapiDefaultTimeout is the bound the three transports read.
func haapiDefaultTimeout() time.Duration { return haapi.DefaultTimeout }

// jsonReachClaim is the sentence in docs/manual.md that enumerates the commands
// `--json` does not reach.
const jsonReachClaim = "**Commands `--json` does not reach:**"

// TestManualNamesTheCommandsJSONDoesNotReach holds the manual's enumeration to
// what the commands actually do — in both directions, which is the half that
// matters here.
//
// The list was written by hand, in two places, and was wrong both ways at once:
// it named `tpl eval`, which answers `--json` with a JSON envelope and has since
// H-10 forced it, and it omitted `rtfm`, which prints Markdown whatever the flag
// says (#12). A caller reading the manual as the contract — which is what the
// manual is for — was told the opposite of the truth about two commands.
//
// The derivation is the classification TestJSONContract already sweeps by
// (verbatimByDesign), plus `rtfm`: the one meta command whose output a caller
// would plausibly ask for as data. Every command named is then RUN with --json
// and has to answer with something that is not a JSON document, so a command
// that starts honouring the flag falls out of the sentence rather than sitting
// in it as folklore.
func TestManualNamesTheCommandsJSONDoesNotReach(t *testing.T) {
	named := commandsTheManualExcludesFromJSON(t)
	fixture := buildContractFixture(t)
	posArgs := contractPosArgs(fixture)

	want := map[string]bool{"rtfm": true}
	walkCommandTree(rootCmd, func(c *cobra.Command) {
		if !c.HasSubCommands() && verbatimByDesign[c.Name()] {
			want[strings.Join(cmdArgsOf(c), " ")] = true
		}
	})

	for path := range want {
		if !named[path] {
			t.Errorf("`%s` does not reach --json and the manual does not say so", path)
		}
	}
	for path := range named {
		if !want[path] {
			t.Errorf("the manual says --json does not reach `%s`, which is not how the command is classified", path)
		}
		if _, isEnforced := posArgs[path]; isEnforced {
			t.Errorf("the manual says --json does not reach `%s`, and TestJSONContract asserts that it does", path)
		}
	}

	// The measurement. Without it the sentence and the classification could
	// agree with each other and both be wrong about the command — which is
	// exactly the state `tpl eval` was in.
	for path := range named {
		assertAnswerIsNotJSON(t, fixture, path)
	}
}

// verbatimCommandArgs is what each command the manual names needs to answer at
// all against the shared contract fixture. `auto diff`/`script diff` compare a
// candidate against the stored entry, so they need one to compare.
func verbatimCommandArgs(t *testing.T) map[string][]string {
	t.Helper()
	candidate := filepath.Join(t.TempDir(), "candidate.yaml")
	if err := os.WriteFile(candidate, []byte("alias: A document\nsequence: []\n"), 0o600); err != nil {
		t.Fatalf("writing the diff candidate: %v", err)
	}
	return map[string][]string{
		"auto cat":     {"morning"},
		"auto diff":    {"morning", "-f", candidate},
		"script cat":   {"wakeup"},
		"script diff":  {"wakeup", "-f", candidate},
		"helper cat":   {"guest_mode"},
		"tpl cat":      {"tpl1"},
		"config file":  {"configuration.yaml"},
		"config block": {"template.yaml", "tpl1"},
		"rtfm":         nil,
	}
}

// assertAnswerIsNotJSON runs one command the manual names with --json and
// requires the answer to be something other than a JSON document.
func assertAnswerIsNotJSON(t *testing.T, fixture *contractFixture, path string) {
	t.Helper()
	extra, ok := verbatimCommandArgs(t)[path]
	if !ok {
		t.Errorf("`%s` is named in the manual's claim and this gate cannot drive it — add its arguments", path)
		return
	}
	args := append([]string{"hactl", "--dir", fixture.dir}, strings.Fields(path)...)
	args = append(args, extra...)
	args = append(args, "--json", "--tokensmax", "0")
	out, err := runArgs(t, args)
	switch {
	case err != nil:
		t.Errorf("`%s --json`: %v", path, err)
	case out == "":
		t.Errorf("`%s --json` answered nothing — this gate cannot tell a verbatim document from a broken fixture", path)
	default:
		var doc any
		if json.Unmarshal([]byte(out), &doc) == nil {
			t.Errorf("`%s --json` answers with a JSON document — it reaches --json after all, and the manual says it does not", path)
		}
	}
}

// commandsTheManualExcludesFromJSON reads the claim's backticked spans:
// "auto|script|helper|tpl cat" names four commands, "rtfm" names one.
func commandsTheManualExcludesFromJSON(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(surfaceRepoRoot(t), "docs", "manual.md"))
	if err != nil {
		t.Fatalf("reading the manual: %v", err)
	}
	idx := strings.Index(string(raw), jsonReachClaim)
	if idx < 0 {
		t.Fatalf("docs/manual.md no longer contains %q — the claim this gate enforces has moved or gone", jsonReachClaim)
	}
	claim := string(raw)[idx+len(jsonReachClaim):]
	if end := strings.Index(claim, "\u2014"); end > 0 {
		claim = claim[:end]
	}

	named := map[string]bool{}
	for span := range strings.SplitSeq(claim, "`") {
		fields := strings.Fields(strings.TrimSpace(strings.Trim(span, ",.: ")))
		switch len(fields) {
		case 1:
			for leaf := range strings.SplitSeq(fields[0], "|") {
				named[leaf] = true
			}
		case 2:
			for family := range strings.SplitSeq(fields[0], "|") {
				for leaf := range strings.SplitSeq(fields[1], "|") {
					named[family+" "+leaf] = true
				}
			}
		}
	}
	if len(named) == 0 {
		t.Fatal("no command was read out of the manual's claim — the parse has stopped matching")
	}
	return named
}

// TestSinceIsReadThroughOneAccessor holds sinceWindow's doc comment to the
// source.
//
// TestEveryCommandDeclaringSinceReadsIt means something only while sinceWindow
// really is the one read: a command that reached for `flagSince` directly would
// consume the window without recording it, and the proof that the declaration
// set is the consumption set would quietly become a proof about nothing. The
// two files allowed are the one that owns the accessor and the one that resets
// the value between in-process invocations.
func TestSinceIsReadThroughOneAccessor(t *testing.T) {
	dir := filepath.Join(surfaceRepoRoot(t), "internal", "cmd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading internal/cmd: %v", err)
	}
	owners := map[string]bool{"since.go": true, "root.go": true}

	var scanned int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || owners[name] {
			continue
		}
		scanned++
		src, readErr := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // dir is the repository's own internal/cmd and name comes from ReadDir over it
		if readErr != nil {
			t.Fatalf("reading %s: %v", name, readErr)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "flagSince") {
				t.Errorf("%s:%d reads flagSince directly: %s\n"+
					"every consumer goes through sinceWindow(), which is what makes "+
					"TestEveryCommandDeclaringSinceReadsIt an observation rather than an assumption",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if scanned < 30 {
		t.Fatalf("only %d files were scanned — the walk has stopped matching", scanned)
	}
}
