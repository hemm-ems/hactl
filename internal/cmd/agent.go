package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// The agent family (manual "Several agents", D-151, D-160, D-164, D-167): one
// instance directory per agent, each with its own token, policy.yaml and
// audit log, so the file still picks the profile (D-62) and an MCP server
// pinned to the directory gets exactly its own rules (D-64).

var (
	flagAgentFrom  string
	flagAgentPath  string
	flagAgentToken string
	flagAgentRoot  string
)

var agentCmd = family(&cobra.Command{
	Use:   "agent",
	Short: "Give each agent an instance directory of its own — own token, own policy, own audit log",
})

var agentAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create an agent's instance directory: its own token, HACTL_POLICY=required, a read-only starter policy",
	Long: "The owner's command; it needs a terminal. Copies HA_URL, COMPANION_URL and TZ from the source " +
		"instance (--from, default: the instance hactl would use), creates a long-lived token of the same user " +
		"named \"hactl agent <name>\" (or reads one with --token -), writes HACTL_POLICY=required and a read-only " +
		"starter policy.yaml, and prints the MCP config line. The directory is --path or ~/.hactl/agents/<name>; " +
		"one that exists is refused.",
	Args: takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAgentAdd(cmd.Context(), os.Stdin, os.Stdout, args[0])
	},
}

var agentLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List the agent directories, each one's profile in effect and its last write check",
	Long: "Every directory directly under ~/.hactl/agents (or --root) that holds a .env: its profile in effect, " +
		"whether it requires a policy, and the last line of its audit log. Reads local files only.",
	Args: takesNone(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runAgentLs(cmd.OutOrStdout(), flagAgentRoot)
	},
}

func init() {
	agentAddCmd.Flags().StringVar(&flagAgentFrom, "from", "", "source instance directory (default: the instance hactl would use)")
	agentAddCmd.Flags().StringVar(&flagAgentPath, "path", "", "the agent's directory (default: ~/.hactl/agents/<name>)")
	agentAddCmd.Flags().StringVar(&flagAgentToken, "token", "",
		"- reads a long-lived token from the terminal instead of creating one (no other value is accepted)")
	agentLsCmd.Flags().StringVar(&flagAgentRoot, "root", "", "the directory holding agent directories (default: ~/.hactl/agents)")
	agentCmd.AddCommand(agentAddCmd, agentLsCmd)
	rootCmd.AddCommand(agentCmd)
}

// agentNameRE is D-164's name rule.
var agentNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// agentTokenLifespanDays is the life of a created token: the frontend's own
// default for a long-lived token (D-160).
const agentTokenLifespanDays = 3650

// starterPolicy is agent add's read-only starter (D-150): one profile in
// effect that allows reading everything and denies every write.
const starterPolicy = `# Written by ` + "`hactl agent add`" + `: read-only until the owner activates
# this agent's own policy with ` + "`hactl policy activate`" + `.
version: 1
active: readonly
profiles:
  readonly:
    default: deny
    allow:
      "*": [read]
`

// agentsRoot is ~/.hactl/agents, or root when given.
func agentsRoot(root string) (string, error) {
	if root != "" {
		return filepath.Abs(root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".hactl", "agents"), nil
}

// tildePath shows a path under the home directory as ~/…, as the manual does.
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return p
}

// agentRefusalError is an agent add that changed nothing.
type agentRefusalError struct{ msg string }

func (e *agentRefusalError) Error() string { return "agent add: " + e.msg }

// ExitCode is 1.
func (e *agentRefusalError) ExitCode() int { return 1 }

func runAgentAdd(ctx context.Context, in io.Reader, out io.Writer, name string) error {
	if !ownerTerminalCheck() {
		return &ownerOnlyError{what: "agent add"}
	}
	if !agentNameRE.MatchString(name) {
		return &agentRefusalError{msg: fmt.Sprintf("%q is not an agent name: lowercase letters, digits, - and _, starting with a letter or digit, at most 64 characters", name)}
	}
	if flagAgentToken != "" && flagAgentToken != "-" {
		return &agentRefusalError{msg: "--token takes only - (read the token from the terminal), so it never lands in shell history; nothing was changed"}
	}
	src, err := config.Load(flagAgentFromOrDir())
	if err != nil {
		return err
	}
	target := flagAgentPath
	if target == "" {
		root, rootErr := agentsRoot("")
		if rootErr != nil {
			return rootErr
		}
		target = filepath.Join(root, name)
	}
	if target, err = filepath.Abs(target); err != nil {
		return err
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		return &agentRefusalError{msg: target + " already exists — nothing was changed and no token was made; choose another name or --path"}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return &agentRefusalError{msg: "cannot inspect " + target + ": " + statErr.Error()}
	}

	clientName := "hactl agent " + name
	var token, tokenLine string
	if flagAgentToken == "-" {
		token, err = readSecretLine(in, out, "paste the agent's long-lived token (not echoed): ")
		if err != nil {
			return err
		}
		if token == "" {
			return &agentRefusalError{msg: "no token given — nothing was changed"}
		}
		tokenLine = "the pasted token (checked against Home Assistant)"
	} else {
		token, err = createAgentToken(ctx, src, clientName)
		if err != nil {
			return &agentRefusalError{msg: "Home Assistant did not create a token (" + err.Error() + ") — nothing was changed. " +
				"Create one by hand (your profile → Security → Long-lived access tokens) and run again with --token -"}
		}
		tokenLine = fmt.Sprintf("a long-lived token of its own, %q", clientName)
	}
	if _, err := haapi.New(src.URL, token).GetAPIStatus(ctx); err != nil {
		msg := "the token does not authenticate at " + src.URL + " (" + err.Error() + ") — nothing was written"
		if flagAgentToken != "-" {
			msg += fmt.Sprintf("; revoke the token %q in Home Assistant", clientName)
		}
		return &agentRefusalError{msg: msg}
	}

	if err := writeAgentDir(target, src, token); err != nil {
		msg := err.Error()
		if flagAgentToken != "-" {
			msg += fmt.Sprintf(" — the token %q was created; revoke it in Home Assistant (your profile → Security)", clientName)
		}
		return fmt.Errorf("agent add: %s", msg)
	}

	_, _ = fmt.Fprintf(out, "created %s  (from %s)\n", tildePath(target), tildePath(src.Dir))
	_, _ = fmt.Fprintf(out, "  token:   %s\n", tokenLine)
	_, _ = fmt.Fprintln(out, "  policy:  read-only until you activate one (HACTL_POLICY=required)")
	_, _ = fmt.Fprintln(out, "  next:    give the agent `hactl rtfm --skill policy-setup`, then review and activate its draft")
	_, _ = fmt.Fprintf(out, "  mcp:     claude mcp add %s -- hactl mcp --dir %s --allow-writes\n", name, tildePath(target))
	return nil
}

// flagAgentFromOrDir is the source instance: --from, else what --dir (or
// discovery) names.
func flagAgentFromOrDir() string {
	if flagAgentFrom != "" {
		return flagAgentFrom
	}
	return flagDir
}

// createAgentToken asks Home Assistant for a long-lived token of the source
// token's own user (M-6, D-160).
func createAgentToken(ctx context.Context, src *config.Config, clientName string) (string, error) {
	ws := haapi.NewWSClient(src.URL, src.Token)
	if err := ws.Connect(ctx); err != nil {
		return "", err
	}
	defer func() { _ = ws.Close() }()
	return ws.CreateLongLivedToken(ctx, clientName, agentTokenLifespanDays)
}

// readSecretLine prompts on out and reads one line from in, without echo when
// in is a terminal.
func readSecretLine(in io.Reader, out io.Writer, prompt string) (string, error) {
	_, _ = fmt.Fprint(out, prompt)
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) { //nolint:gosec // a file descriptor fits an int
		b, err := term.ReadPassword(int(f.Fd())) //nolint:gosec // a file descriptor fits an int
		_, _ = fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("reading the token: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	return strings.TrimSpace(line), nil
}

// writeAgentDir writes the agent's instance directory (D-164): the directory
// itself (0700; it must not exist), .env (0600) and the starter policy.yaml —
// the second of hactl's two writers of policy.yaml (D-148, D-150).
func writeAgentDir(target string, src *config.Config, token string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(target), err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", target, err)
	}
	var env bytes.Buffer
	fmt.Fprintf(&env, "HA_URL=%s\nHA_TOKEN=%s\n", src.URL, token)
	if src.CompanionURL != "" {
		fmt.Fprintf(&env, "COMPANION_URL=%s\n", src.CompanionURL)
	}
	if src.TZ != "" {
		fmt.Fprintf(&env, "TZ=%s\n", src.TZ)
	}
	env.WriteString("HACTL_POLICY=required\n")
	if err := writeNewFile(filepath.Join(target, ".env"), env.Bytes(), 0o600); err != nil {
		return err
	}
	return writeNewFile(filepath.Join(target, policy.FileName), []byte(starterPolicy), 0o644)
}

// writeNewFile creates path — which must not exist — with data.
func writeNewFile(path string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // the agent's own new directory
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return f.Close()
}

// --- agent ls ----------------------------------------------------------------

type agentRow struct {
	Name           string             `json:"name"`
	Dir            string             `json:"dir"`
	Profile        string             `json:"profile"`
	SelectedBy     string             `json:"selected_by"`
	Required       bool               `json:"required"`
	Policy         string             `json:"policy"`
	LastWriteCheck *enforce.AuditLine `json:"last_write_check"`
}

func runAgentLs(w io.Writer, rootFlag string) error {
	root, err := agentsRoot(rootFlag)
	if err != nil {
		return err
	}
	rows := []agentRow{}
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", root, err)
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(dir, ".env")); statErr != nil {
			continue
		}
		rows = append(rows, agentRowFor(e.Name(), dir))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	if flagJSON {
		markStructuredOutput()
		return writeJSON(w, rows)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintf(w, "no agents under %s\n", tildePath(root))
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tDIR\tPROFILE\tREQUIRED\tLAST WRITE CHECK")
	for _, r := range rows {
		req := "no"
		if r.Required {
			req = "yes"
		}
		last := "never"
		if l := r.LastWriteCheck; l != nil {
			verdict := "denied"
			if l.Allowed {
				verdict = "allowed"
			}
			last = fmt.Sprintf("%s %s %s %s", l.Time, verdict, l.Verb, l.Resource)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, tildePath(r.Dir), r.Profile, req, last)
	}
	return tw.Flush()
}

// agentRowFor reads one agent directory: its policy, its requirement, the
// last line of its audit log (D-167).
func agentRowFor(name, dir string) agentRow {
	row := agentRow{Name: name, Dir: dir}
	row.Required, _ = config.PolicyRequired(dir)
	pol, err := policy.Load(dir)
	switch {
	case err != nil:
		row.Profile, row.Policy = "broken", "broken"
	case !pol.Exists():
		row.Profile, row.Policy, row.SelectedBy = "none", "missing", string(pol.Selection())
	case !pol.InEffect():
		row.Profile, row.Policy, row.SelectedBy = "allow-all", "ok", string(pol.Selection())
	default:
		row.Profile, row.Policy, row.SelectedBy = pol.Active(), "ok", string(pol.Selection())
	}
	row.LastWriteCheck = lastAuditLine(enforce.NewAudit(dir).Path())
	return row
}

// lastAuditLine is the last JSON line of the audit log at path, nil when there
// is none or it does not decode.
func lastAuditLine(path string) *enforce.AuditLine {
	data, err := os.ReadFile(path) //nolint:gosec // an agent directory's own audit log
	if err != nil {
		return nil
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) == 0 || len(lines[len(lines)-1]) == 0 {
		return nil
	}
	var l enforce.AuditLine
	if err := json.Unmarshal(lines[len(lines)-1], &l); err != nil {
		return nil
	}
	return &l
}
