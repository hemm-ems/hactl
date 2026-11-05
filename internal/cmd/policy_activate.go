package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// `policy activate <draft>` (manual "`policy activate <draft>`", D-148,
// D-149, D-163): the owner's command, the only one that puts a policy in
// effect. An agent drafts; the owner, at a terminal of their own, reviews the
// draft, types its profile name, and only then is policy.yaml replaced.

var policyActivateCmd = &cobra.Command{
	Use:   "activate <draft>",
	Short: "Put a draft policy in effect — the owner's command; it needs a terminal and asks for the profile name",
	Long: "Lint the draft (a broken one is refused), print its review, ask the owner to type the profile name, " +
		"then write the draft's bytes to policy.yaml atomically, keeping the previous file as policy.yaml.prev. " +
		"Refused unless stdin and stdout are a terminal: an agent drafts, only the owner activates. A relative " +
		"draft path is resolved against the instance directory.",
	Args: takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Straight to the process's own stdout, like setup: the root buffers
		// cmd.OutOrStdout(), which would hide the prompt until the answer.
		return runPolicyActivate(cmd.Context(), os.Stdin, os.Stdout, args[0])
	},
}

func init() {
	policyCmd.AddCommand(policyActivateCmd)
}

// ownerTerminal reports whether stdin and stdout are both a terminal — what
// tells the owner at a keyboard from an agent's shell, Claude Code's `!`
// prefix and an MCP server, none of which has one (M-5, D-149). A guardrail
// against an agent activating its own draft by mistake, not a lock (D-51).
func ownerTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) //nolint:gosec // a file descriptor fits an int
}

// ownerTerminalCheck is ownerTerminal, a variable so an in-process test can
// reach the steps behind the gate. The gate itself is tested through the
// built binary, with pipes and with a pseudo-terminal.
var ownerTerminalCheck = ownerTerminal

// ownerOnlyError refuses an owner command without a terminal.
type ownerOnlyError struct{ what string }

func (e *ownerOnlyError) Error() string {
	return e.what + " needs a terminal: only the owner " + ownerVerb(e.what) +
		" — run it yourself in a terminal of your own (an agent's shell and Claude Code's ! prefix have none)\n" +
		"  hint: " + strings.TrimPrefix(enforce.OwnerTail, " — ")
}

func ownerVerb(what string) string {
	if what == "agent add" {
		return "adds an agent"
	}
	return "activates a policy"
}

// ExitCode is 1.
func (e *ownerOnlyError) ExitCode() int { return 1 }

// activateRefusalError is a draft activate will not write, with nothing written.
type activateRefusalError struct{ msg string }

func (e *activateRefusalError) Error() string { return "activate: " + e.msg }

// ExitCode is 1.
func (e *activateRefusalError) ExitCode() int { return 1 }

func runPolicyActivate(ctx context.Context, in io.Reader, out io.Writer, arg string) error {
	if !ownerTerminalCheck() {
		return &ownerOnlyError{what: "policy activate"}
	}
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	target := filepath.Join(cfg.Dir, policy.FileName)
	path := draftPath(cfg.Dir, arg)
	if samePath(path, target) {
		return &activateRefusalError{msg: "the draft is " + target + " itself — activate a draft (e.g. policy.draft.yaml), not the file in effect"}
	}
	if _, statErr := os.Lstat(filepath.Join(cfg.Dir, "policy.yml")); statErr == nil {
		return &activateRefusalError{msg: "a policy.yml sits in " + cfg.Dir + ": hactl reads only policy.yaml, and the two together break it — remove or merge policy.yml first; nothing was written"}
	}

	// The draft is read once: these bytes are what is reviewed and exactly
	// what is written (D-163).
	pol, data, err := policy.LoadDraft(path)
	var le *policy.LoadError
	switch {
	case errors.As(err, &le):
		return &enforce.BrokenError{Err: le}
	case err != nil:
		return err
	}
	if !pol.InEffect() {
		return &activateRefusalError{msg: path + " puts no profile in effect (no active: and no default profile) — there is no " +
			"profile name to confirm, and an agent's instance must not be allow-all; nothing was written"}
	}

	doc, err := buildReview(ctx, cfg, pol)
	if err != nil {
		return fmt.Errorf("activate: %w; nothing was written", err)
	}
	_, _ = io.WriteString(out, renderReview(doc))
	_, _ = fmt.Fprintf(out, "replaces: %s\n", replacesText(cfg.Dir, target))
	_, _ = fmt.Fprintf(out, "type the profile name to activate it (%s), anything else cancels: ", pol.Active())

	answer, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(answer) != pol.Active() {
		_, _ = fmt.Fprintln(out)
		return &activateRefusalError{msg: "cancelled — nothing was written"}
	}

	prev, err := activatePolicyFile(cfg.Dir, data)
	if err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	_, _ = fmt.Fprintf(out, "activated: %s — profile %s is in effect from the next command\n", target, pol.Active())
	if prev != "" {
		_, _ = fmt.Fprintf(out, "previous: %s\n", prev)
	}
	return nil
}

// samePath reports whether a and b name one file: equal cleaned paths, or
// both exist and are the same file.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, aErr := os.Stat(a)
	bi, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && os.SameFile(ai, bi)
}

// replacesText says what activation replaces: the profile in effect now,
// a broken file, or nothing.
func replacesText(dir, target string) string {
	cur, err := policy.Load(dir)
	var le *policy.LoadError
	switch {
	case errors.As(err, &le):
		return target + " (broken)"
	case err != nil:
		return target + " (unreadable: " + err.Error() + ")"
	case !cur.Exists():
		return "nothing (no policy.yaml yet)"
	case !cur.InEffect():
		return target + " (no profile in effect)"
	}
	return target + " (profile " + cur.Active() + ")"
}

// activatePolicyFile is the one writer of policy.yaml besides agent add's
// starter (D-148). It keeps the previous file as policy.yaml.prev — only when
// there was one — then writes data to a temporary file in dir, syncs it and
// renames it over policy.yaml, so a reader sees the old file or the new one,
// never half of either; no temporary file is left behind (D-163). It returns
// the .prev path, "" when there was no previous file.
func activatePolicyFile(dir string, data []byte) (string, error) {
	target := filepath.Join(dir, policy.FileName)
	prevPath := target + ".prev"
	mode := fs.FileMode(0o644)
	prev := ""
	switch info, err := os.Lstat(target); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", fmt.Errorf("cannot inspect %s: %w; nothing was written", target, err)
	default:
		old, readErr := os.ReadFile(target) //nolint:gosec // the instance's own policy file
		if readErr != nil {
			return "", fmt.Errorf("cannot keep the previous %s as %s: %w; nothing was written", target, prevPath, readErr)
		}
		if info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
		if err := atomicWrite(dir, prevPath, old, mode); err != nil {
			return "", fmt.Errorf("cannot keep the previous %s as %s: %w; nothing was written", target, prevPath, err)
		}
		prev = prevPath
	}
	if err := atomicWrite(dir, target, data, mode); err != nil {
		return "", fmt.Errorf("writing %s: %w (the previous file is unchanged)", target, err)
	}
	return prev, nil
}

// atomicWrite writes data to a temporary file in dir, syncs it, and renames
// it to path; on any failure the temporary file is removed.
func atomicWrite(dir, path string, data []byte, mode fs.FileMode) (err error) {
	tmp, err := os.CreateTemp(dir, ".policy.yaml.tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, openErr := os.Open(dir); openErr == nil { //nolint:gosec // the instance directory
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
