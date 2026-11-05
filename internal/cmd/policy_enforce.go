package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/manual"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqgate"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// Enforcement of the permission policy (manual "Enforcement (writes)", H-29,
// D-58..D-80): one session per command invocation, started by the root's
// PersistentPreRunE and ended when the invocation ends.
//
//   - The policy is loaded once per invocation, in the pre-run — so under
//     `hactl mcp`, once per tool call (D-72): editing policy.yaml takes effect
//     on the next call, and nothing one call checked is trusted by the next.
//   - A file that cannot be read as a policy refuses every command but the
//     exempt ones (D-60), CLI and MCP alike: both run this pre-run.
//   - Only while a profile is in effect is a session started and the request
//     gate installed (D-74). With no file, or the implicit allow-all, nothing
//     is installed, nothing extra is sent and nothing is logged — the command
//     runs byte-identically to hactl without a policy (D-58).

var (
	// policySession is the running invocation's session, nil while no
	// profile is in effect.
	policySession *enforce.Session
	// policyUninstall removes the session's request gate.
	policyUninstall func()
	// policyInvocation is what the running invocation is, for the witness
	// lookup a refused --confirm makes (D-80).
	policyInvocation struct {
		cmdPath string
		args    []string
	}
	// policyRecorder, when a test sets it, sees every check a session makes
	// (R-12's coverage harness). Production code never sets it.
	policyRecorder enforce.Recorder
	// policyTrailer is the ended invocation's `hidden by policy` line (D-139),
	// "" when its reads hid and masked nothing or no profile was in effect.
	// The entry points print it once, after the command's output.
	policyTrailer string
)

// policyExemptPaths are the commands that still run on a broken policy.yaml
// (D-60 as refined): `policy show` and `policy lint` report the problems; the
// others read no instance and send nothing. One table for the CLI and for every
// MCP tool call, which run the same pre-run.
var policyExemptPaths = map[string]bool{
	"hactl policy show": true,
	"hactl policy lint": true,
	"hactl version":     true,
	"hactl help":        true,
	"hactl completion":  true,
	"hactl rtfm":        true,
}

// policyOwnerPaths are the owner's commands, which run without the instance's
// policy session (D-163): `policy activate` and `agent add` sit behind their
// own terminal gate (D-149), and a broken or missing policy.yaml — which
// refuses every other command — must still be replaceable. Neither is ever
// offered over MCP.
var policyOwnerPaths = map[string]bool{
	"hactl policy activate": true,
	"hactl agent add":       true,
}

// ownerRun reports whether this invocation is the owner's and runs without a
// policy session: `policy activate` and `agent add` always — their own
// terminal gate refuses anyone else (D-149) — and `policy review` when stdin
// and stdout are a terminal (D-172). The owner's review counts the real
// instance; anyone else's — an agent's shell, an MCP server, a script — runs
// under the session like every read, so its counts cover only what the
// profile lets it read and the trailer says how much was hidden.
func ownerRun(cmd *cobra.Command) bool {
	path := cmd.CommandPath()
	return policyOwnerPaths[path] || (path == "hactl policy review" && ownerTerminalCheck())
}

// lintUnderSession reports whether this invocation is `policy lint` run by
// anyone but the owner at a terminal (D-173): its inventory — the ids its
// findings name — then goes through the active profile's read filter, like
// every other read, so lint names nothing the profile hides.
func lintUnderSession(cmd *cobra.Command) bool {
	return cmd.CommandPath() == "hactl policy lint" && !ownerTerminalCheck()
}

// policyExempt reports whether cmd runs without the policy: a D-60 exempt
// command, a completion script or cobra's completion entry points, and the
// commands that only print help or the version — the root (`hactl`,
// `hactl --version`) and a bare group (`hactl ent`).
func policyExempt(cmd *cobra.Command) bool {
	path := cmd.CommandPath()
	switch {
	case policyExemptPaths[path]:
		return true
	case strings.HasPrefix(path, "hactl completion "), strings.HasPrefix(path, "hactl __complete"):
		return true
	case !cmd.HasParent(), isFamilyGroup(cmd):
		return true
	}
	return false
}

// startPolicySession loads the instance's policy for this invocation and, while
// a profile is in effect, installs the request gate. A broken file refuses the
// command (D-60). `hactl mcp` itself starts whatever the file says and checks
// nothing: each tool call it serves runs this pre-run again (D-60, D-72).
func startPolicySession(cmd *cobra.Command, args []string) error {
	endPolicySession()
	policyTrailer = ""
	// `policy lint` without a terminal reads its inventory through the
	// active profile's read filter (D-173): it stays exempt from refusal
	// (D-60), but while a profile is in effect it runs under the session.
	agentLint := lintUnderSession(cmd)
	if (policyExempt(cmd) && !agentLint) || cmd.CommandPath() == "hactl mcp" || ownerRun(cmd) {
		return nil
	}
	dir := config.BestEffortDir(flagDir)
	if dir == "" {
		return nil
	}
	pol, err := policy.Load(dir)
	if err != nil {
		var le *policy.LoadError
		if errors.As(err, &le) {
			if agentLint {
				return nil // lint reports the broken file itself (D-60)
			}
			return &enforce.BrokenError{Err: le}
		}
		return err
	}
	if !pol.InEffect() && agentLint {
		return nil // no profile to filter by; runPolicyLint decides (D-173)
	}
	if !pol.InEffect() {
		// D-136: an instance that requires a policy refuses without one in
		// effect; otherwise D-58 — nothing installed, sent or logged.
		if required, reqErr := config.PolicyRequired(dir); required || reqErr != nil {
			why := "no profile is in effect (no active:, no default profile)"
			switch {
			case reqErr != nil:
				why = reqErr.Error()
			case !pol.Exists():
				why = "the file does not exist"
			}
			return &enforce.MissingError{File: pol.Source(), Why: why}
		}
		return nil
	}
	cfg, cfgErr := config.Load(flagDir)
	var url, token string
	if cfg != nil {
		url, token = cfg.URL, cfg.Token
	}
	fetcher := enforce.NewHAFetcher(invocationContext(cmd), url, token, cfgErr)
	fetcher.SetConfigFileReader(policyConfigFileReader())
	sess, err := enforce.New(enforce.Options{
		Policy:      pol,
		InstanceDir: dir,
		Command:     strings.TrimSpace(cmd.CommandPath() + " " + strings.Join(args, " ")),
		DryRun:      isPreview(cmd),
		// A write command's own reads are exempt (D-82): its plan decides.
		WriteCommand: isWriteCommand(cmd),
		Fetcher:      fetcher,
		Recorder:     policyRecorder,
	})
	if err != nil {
		return err
	}
	policySession = sess
	policyUninstall = reqgate.Install(sess)
	policyInvocation.cmdPath, policyInvocation.args = cmd.CommandPath(), append([]string(nil), args...)
	return nil
}

// invocationContext is the command's context, or the background one when cobra
// set none (a command run outside Execute).
func invocationContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// endPolicySession removes the gate and releases the session.
func endPolicySession() {
	if policyUninstall != nil {
		policyUninstall()
	}
	if policySession != nil {
		policySession.Close()
	}
	policySession, policyUninstall = nil, nil
	policyInvocation.cmdPath, policyInvocation.args = "", nil
}

// finishPolicySession ends the session and returns the invocation's result: a
// request the request layer refused is an error even when the command carried
// on past it (a best-effort cleanup), never a log line (H-29). A refused read
// a command carried on past is not: the command showed less, and the trailer
// counts it as hidden (D-139). The trailer is left in policyTrailer.
func finishPolicySession(err error) error {
	sess := policySession
	endPolicySession()
	if sess == nil {
		return err
	}
	// A refusal is reported as itself, whatever context a command wrapped
	// it in (`fetching entity state: …`): its one line names the rule and
	// its line (D-70, manual "Enforcement (reads)").
	var denied *enforce.DeniedError
	if errors.As(err, &denied) {
		err = denied
	}
	if err == nil {
		if r := sess.RefusedMidRun(); r != nil {
			err = r
		}
	}
	hidden, masked, refused := sess.ReadCounts()
	if err == nil {
		hidden += refused
	}
	// A refusal is the invocation's whole answer: a write's report closes
	// with its hint (D-118), a refused read names what it refused. The
	// trailer describes an answer that was given (D-139).
	if policyErrorClass(err) == "" {
		policyTrailer = readTrailer(hidden, masked, sess.Policy().Active())
	}
	return err
}

// renderReadRefusal makes a refused read the invocation's whole answer, as a
// denied write's report is (D-155): the refusal report on stdout — under
// --json the refusal envelope — in place of anything the command printed
// before it, while the error's own line is the D-119 count on stderr. Any
// other error leaves out untouched.
func renderReadRefusal(out *bytes.Buffer, err error) {
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || !ref.Read {
		return
	}
	out.Reset()
	if rerr := renderPolicyRefusal(out, ref); rerr != nil {
		slog.Debug("rendering a read refusal", "error", rerr)
	}
}

// readTrailer is the `hidden by policy` line (D-139): `hidden by policy: N
// (profile p)`, with `, masked: M` after N when something was masked; "" when
// nothing was hidden or masked.
func readTrailer(hidden, masked int, profile string) string {
	if hidden == 0 && masked == 0 {
		return ""
	}
	s := fmt.Sprintf("hidden by policy: %d", hidden)
	if masked > 0 {
		s += fmt.Sprintf(", masked: %d", masked)
	}
	return s + " (profile " + profile + ")"
}

// writePolicyTrailer prints the ended invocation's trailer once (D-139): the
// last stdout line in prose; under --json — and for any document the command
// marked as one — on stderr, so the document keeps its shape.
func writePolicyTrailer(stdout, stderr io.Writer) {
	line := policyTrailer
	policyTrailer = ""
	if line == "" {
		return
	}
	if flagJSON || structuredOutput {
		_, _ = fmt.Fprintln(stderr, line)
		return
	}
	_, _ = fmt.Fprintln(stdout, line)
}

// policyConfigFileReader reads a config file's raw content through the
// companion for the read filter (D-142), connecting once, on the first file,
// as the session's own read.
func policyConfigFileReader() func(ctx context.Context, location string) ([]byte, error) {
	var (
		mu        sync.Mutex
		cc        *companion.Client
		connErr   error
		connected bool
	)
	return func(ctx context.Context, location string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if !connected {
			connected = true
			cc, connErr = connectCompanion(reqgate.Own(ctx))
		}
		if connErr != nil {
			return nil, connErr
		}
		r, err := cc.ReadConfigFileRaw(reqgate.Own(ctx), location)
		if err != nil {
			return nil, err
		}
		return []byte(r.Content), nil
	}
}

// rootPostRun ends the policy session of an invocation whose command
// succeeded; the entry points end it on every other path.
func rootPostRun(*cobra.Command, []string) error {
	return finishPolicySession(nil)
}

// writesWithoutConfirm are the write commands that have no --confirm (the
// policy write surface's cmd rows, dev/surfaces/policy.manifest): each writes
// as it runs. `config show` is not one of them here — it is a read command
// whose probe its plan checks — nor are `setup` and `mcp`, which no session
// covers. TestWriteCommandsAreThePolicyWriteSurface holds this list and the
// --confirm commands to the manifest.
var writesWithoutConfirm = map[string]bool{
	"hactl config flow-inspect": true,
	"hactl cache clear":         true,
	"hactl cache refresh":       true,
}

// isWriteCommand reports whether cmd is a write command, whose own reads are
// exempt from read enforcement (D-82): every --confirm command, previewed or
// confirmed, and the few that write without one. The owner's `agent add` and
// `policy activate` are not among them: they run without any session
// (ownerRun, D-163), so there is nothing whose reads to exempt (D-172).
func isWriteCommand(cmd *cobra.Command) bool {
	return cmd.Flags().Lookup("confirm") != nil || writesWithoutConfirm[cmd.CommandPath()]
}

// isPreview reports whether cmd runs as a preview: it has --confirm and it is
// not set.
func isPreview(cmd *cobra.Command) bool {
	if cmd.Flags().Lookup("confirm") == nil {
		return false
	}
	confirmed, err := cmd.Flags().GetBool("confirm")
	return err == nil && !confirmed
}

// policyErrorClass reports the policy error class of err (`policy_denied`,
// `policy_broken`), or "".
func policyErrorClass(err error) string {
	var pc interface{ PolicyClass() string }
	if errors.As(err, &pc) {
		return pc.PolicyClass()
	}
	return ""
}

// --- the command layer: a write command's plan ----------------------------------

// writePlan is a write command's plan (H-29's command layer): every (resource,
// verb) its confirmed run touches — the follow-up writes it makes on its own
// included — declared the same way in the preview and under --confirm, and
// checked before the first write.
type writePlan struct {
	action string
	checks []reqmap.Check
	// failClosed holds what the command had to check but could not decide:
	// an address the policy grammar refuses (never canonicalised, D-69), a
	// service call whose reach is unknown (D-73). Each refuses the plan.
	failClosed []enforce.Denial
}

// newWritePlan starts a plan. action completes "would …", as the preview's.
func newWritePlan(action string) *writePlan { return &writePlan{action: action} }

// add declares verb v on the resource `<kind>:<id>[/<sub>]`.
func (p *writePlan) add(v policy.Verb, kind policy.Kind, id, sub string) *writePlan {
	s := string(kind) + ":" + id
	if sub != "" {
		s += "/" + sub
	}
	r, err := policy.ParseResource(s)
	if err != nil {
		p.failClosed = append(p.failClosed, enforce.Denial{Resource: s, Verb: v.String(), Rule: "fail-closed: unmappable resource " + s})
		return p
	}
	p.checks = append(p.checks, reqmap.Check{Verb: v, Resource: r})
	return p
}

// addKindWide declares verb v on EVERY id of kind (D-75: Home Assistant
// mints the id), optionally on sub below each id; reported as `<kind>:*`.
func (p *writePlan) addKindWide(v policy.Verb, kind policy.Kind, sub string) *writePlan {
	p.checks = append(p.checks, reqmap.Check{Verb: v, Resource: policy.Resource{Kind: kind, Sub: sub}, KindWide: true})
	return p
}

// addServiceCall declares a service call: the request layer's own mapping of
// the call (enforce.Session.ServiceCallChecks — the service, its reach, the
// entities its data names, the fan-out and script runs), or, when its reach
// cannot be computed, a fail-closed denial. With no profile in effect it asks
// nothing and adds nothing (D-58).
func (p *writePlan) addServiceCall(ctx context.Context, domain, service string, data any) *writePlan {
	sess := policySession
	if sess == nil {
		return p
	}
	checks, denial := sess.ServiceCallChecks(ctx, domain, service, data)
	if denial != nil {
		p.failClosed = append(p.failClosed, *denial)
		return p
	}
	p.checks = append(p.checks, checks...)
	return p
}

// planPolicyNote is an allowed plan's line in the preview: `policy: <n>
// checked, allowed (profile <p>)`, and `"policy": {"checked", "profile"}` in
// its --json details.
type planPolicyNote struct {
	Checked int    `json:"checked"`
	Profile string `json:"profile"`
}

func (n planPolicyNote) String() string {
	return fmt.Sprintf("%d checked, allowed (profile %s)", n.Checked, n.Profile)
}

// withPolicy adds an allowed plan's note to a preview; nil (no profile in
// effect) adds nothing, so the preview stays byte-identical (D-58).
func (p *dryRunPlan) withPolicy(n *planPolicyNote) *dryRunPlan {
	if n == nil {
		return p
	}
	return p.with("policy", *n)
}

// checkWritePlan checks a write command's plan before its first write — in
// the preview (dryRun) and again under --confirm (H-2's parity). With no
// profile in effect it checks nothing and returns nil, nil. An allowed plan
// returns the preview's note. A refused plan is rendered to w — the refusal
// report, or under --json the preview envelope with ok false (D-70, R-13) —
// and returned as the *enforce.DeniedError whose text is the one stderr line;
// exit 1. A refused preview records no confirm witness, because it fails
// (R-11).
func checkWritePlan(w io.Writer, dryRun bool, p *writePlan) (*planPolicyNote, error) {
	sess := policySession
	if sess == nil {
		return nil, nil //nolint:nilnil // no profile in effect: nothing is checked and the preview gets no note (D-58)
	}
	previewPassed := !dryRun && policyInvocation.cmdPath != "" &&
		hasWitness(stateCacheDir(flagDir), manual.SessionKey(), policyInvocation.cmdPath, policyInvocation.args, time.Now())
	res, err := sess.CheckPlan(enforce.Plan{Action: p.action, Checks: p.checks, FailClosed: p.failClosed}, dryRun, previewPassed)
	var ref *enforce.DeniedError
	if err != nil && !errors.As(err, &ref) {
		return nil, err
	}
	if ref == nil {
		return &planPolicyNote{Checked: res.Checked, Profile: res.Profile}, nil
	}
	if rerr := renderPolicyRefusal(w, ref); rerr != nil {
		return nil, rerr
	}
	return nil, ref
}

// policyRefusalJSON is the refusal's --json form: the preview envelope with
// ok false and the error class (D-70, R-13).
type policyRefusalJSON struct {
	DryRun  bool   `json:"dry_run"`
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Action  string `json:"action"`
	Details struct {
		Policy policyRefusalDetails `json:"policy"`
	} `json:"details"`
	Hint string `json:"hint"`
}

type policyRefusalDetails struct {
	Denied     []enforce.Denial `json:"denied"`
	Checked    int              `json:"checked"`
	Profile    string           `json:"profile"`
	SelectedBy string           `json:"selected_by"`
	Level      string           `json:"level"`
	Written    []string         `json:"written"`
}

// renderPolicyRefusal writes the refusal report: under --json the envelope,
// otherwise
//
//	policy_denied: would <action> — refused by policy, nothing was written
//	  denied: <verb> <resource> — <rule> (line <n>)
//	    where not met: <rule> (line <n>) — <reason>
//	  checked: <n> resources, <m> denied
//	  profile: <p> (selected by <how>)
//	  hint: <what the causes leave the caller to do> (D-118)
func renderPolicyRefusal(w io.Writer, r *enforce.DeniedError) error {
	written := r.Written
	if written == nil {
		written = []string{}
	}
	if flagJSON {
		doc := policyRefusalJSON{DryRun: r.DryRun, OK: false, Error: enforce.ClassDenied, Action: r.Action, Hint: r.Hint()}
		doc.Details.Policy = policyRefusalDetails{
			Denied: r.Denied, Checked: r.Checked, Profile: r.Profile, SelectedBy: string(r.SelectedBy),
			Level: policy.Level, Written: written,
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	var b strings.Builder
	what := "nothing was written"
	if len(written) > 0 {
		what = "already written: " + strings.Join(written, ", ")
	}
	fmt.Fprintf(&b, "%s: would %s — refused by policy, %s\n", enforce.ClassDenied, r.Action, what)
	for _, d := range r.Denied {
		line := "  denied: " + d.Text()
		if d.Partial {
			line += " — a part of it is denied, so all of it is"
		}
		b.WriteString(line + "\n")
		for _, u := range d.UnmetWhere {
			b.WriteString("    " + unmetWhereText(u) + "\n")
		}
	}
	fmt.Fprintf(&b, "  checked: %d resources, %d denied\n", r.Checked, len(r.Denied))
	fmt.Fprintf(&b, "  profile: %s (selected by %s)\n", r.Profile, r.SelectedBy)
	fmt.Fprintf(&b, "  hint: %s\n", r.Hint())
	_, err := io.WriteString(w, b.String())
	return err
}

// unmetWhereText is the line a verdict's unmet selector renders as (D-112):
// `where not met: <rule> (line <n>) — <reason>`.
func unmetWhereText(u policy.UnmetWhere) string {
	return fmt.Sprintf("where not met: %s (line %d) — %s", u.Rule, u.Line, u.Reason)
}
