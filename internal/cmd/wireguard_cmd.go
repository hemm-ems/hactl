package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
)

var (
	flagWGTunnel   string
	flagWGConfFile string
	flagWGConfirm  bool
)

var wireguardCmd = family(&cobra.Command{
	Use:   "wireguard",
	Short: "Manage the companion WireGuard tunnel (remote lifeline)",
	Long: "Configure, bring up/down, and inspect the companion's WireGuard tunnel.\n\n" +
		"This is the lifeline hactl rides over for remote access. The endpoints are\n" +
		"Ingress-only; this command handles the Supervisor Ingress session auth\n" +
		"automatically (a plain bearer-token request gets 401). Mutations are dry-run\n" +
		"by default — pass --confirm to apply.",
})

var wireguardStatusCmd = &cobra.Command{
	Use:   "status",
	Args:  takesNone(),
	Short: "Show WireGuard tunnel status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWireguardStatus(cmd.Context(), cmd.OutOrStdout())
	},
}

var wireguardConfigCmd = &cobra.Command{
	Use:   "config",
	Args:  takesNone(),
	Short: "Push a WireGuard .conf to the companion (persisted on /data)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWireguardConfig(cmd.Context(), cmd.OutOrStdout())
	},
}

var wireguardUpCmd = &cobra.Command{
	Use:   "up",
	Args:  takesNone(),
	Short: "Bring the tunnel up",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWireguardUp(cmd.Context(), cmd.OutOrStdout())
	},
}

var wireguardDownCmd = &cobra.Command{
	Use:   "down",
	Args:  takesNone(),
	Short: "Bring the tunnel down",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWireguardDown(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	wireguardCmd.PersistentFlags().StringVar(&flagWGTunnel, "tunnel", "wg0", "tunnel name")

	wireguardConfigCmd.Flags().StringVarP(&flagWGConfFile, "file", "f", "", "path to a WireGuard .conf file")
	wireguardConfigCmd.Flags().BoolVar(&flagWGConfirm, "confirm", false, "actually push (default is dry-run)")

	wireguardUpCmd.Flags().BoolVar(&flagWGConfirm, "confirm", false, "actually start (default is dry-run)")

	wireguardDownCmd.Flags().BoolVar(&flagWGConfirm, "confirm", false, "actually stop (default is dry-run)")

	wireguardCmd.AddCommand(wireguardStatusCmd, wireguardConfigCmd, wireguardUpCmd, wireguardDownCmd)
	companionCmd.AddCommand(wireguardCmd)
}

func runWireguardStatus(ctx context.Context, w io.Writer) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	st, err := cc.WireGuardStatus(ctx, flagWGTunnel)
	if err != nil {
		return err
	}
	if flagJSON {
		return writeJSON(w, st)
	}
	writeWireguardStatus(w, st)
	return nil
}

func writeWireguardStatus(w io.Writer, st *companion.WireGuardStatusResponse) {
	if st.State != "active" {
		_, _ = fmt.Fprintf(w, "wireguard %s  %s\n", st.Tunnel, st.State)
		return
	}
	_, _ = fmt.Fprintf(w, "wireguard %s  active\n", st.Tunnel)
	if st.Interface != nil {
		_, _ = fmt.Fprintf(w, "  iface  pub=%s  port=%d\n", st.Interface.PublicKey, st.Interface.ListeningPort)
	}
	for _, p := range st.Peers {
		hs := p.LatestHandshake
		if hs == "" {
			hs = "(none)"
		}
		_, _ = fmt.Fprintf(w, "  peer   %s  hs=%s  rx=%s tx=%s\n", p.Endpoint, hs, p.TransferRx, p.TransferTx)
	}
	writeWireguardMonitor(w, st.Monitor)
}

func writeWireguardMonitor(w io.Writer, m *companion.WireGuardMonitor) {
	if m == nil || !m.Running {
		_, _ = fmt.Fprintln(w, "  monitor  not running (no hostname-endpoint peer)")
		return
	}
	_, _ = fmt.Fprintf(w, "  monitor  running  hostnames=%d\n", len(m.Hostnames))
	if m.LastReresolveSecsAgo != nil {
		// Every resolved endpoint, in a fixed order.
		//
		// This printed one arbitrary entry of a map, labelled "the most recent
		// resolved address" — a value with no relationship to recency, chosen
		// by Go's randomised map iteration. Sixty runs against a byte-identical
		// companion response produced three different outputs, which is H-16
		// ("an answer is a function of the instance, never of map iteration
		// order") failing outright on a read command.
		hosts := make([]string, 0, len(m.Resolved))
		for h := range m.Resolved {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		resolved := make([]string, 0, len(hosts))
		for _, h := range hosts {
			resolved = append(resolved, m.Resolved[h])
		}
		ip := strings.Join(resolved, " ")
		_, _ = fmt.Fprintf(w, "    last re-resolve  %s ago", fmtAge(*m.LastReresolveSecsAgo))
		if ip != "" {
			_, _ = fmt.Fprintf(w, " → %s", ip)
		}
		_, _ = fmt.Fprintln(w)
	}
	if m.Healthy {
		_, _ = fmt.Fprintln(w, "    state  healthy")
	} else {
		next := ""
		if m.NextRetrySecs != nil {
			next = ", next in " + fmtAge(*m.NextRetrySecs)
		}
		_, _ = fmt.Fprintf(w, "    state  reconnecting (attempt %d%s)\n", m.Attempt, next)
	}
	if m.LastError != "" {
		_, _ = fmt.Fprintf(w, "    last error  %s\n", m.LastError)
	}
}

// fmtAge renders a non-negative seconds count compactly (e.g. "1m46s").
func fmtAge(secs int) string {
	if secs < 0 {
		secs = 0
	}
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm%ds", secs/60, secs%60)
	case secs < 86400:
		return fmt.Sprintf("%dh%dm", secs/3600, (secs%3600)/60)
	default:
		return fmt.Sprintf("%dd%dh", secs/86400, (secs%86400)/3600)
	}
}

func runWireguardConfig(ctx context.Context, w io.Writer) error {
	if flagWGConfFile == "" {
		return errors.New("--file is required")
	}
	raw, err := os.ReadFile(flagWGConfFile) //nolint:gosec // file path provided by user via CLI flag
	if err != nil {
		return fmt.Errorf("reading config file: %w", err)
	}
	conf := string(raw)
	if !flagWGConfirm {
		return dryRun("push config to tunnel "+flagWGTunnel).
			with("tunnel", flagWGTunnel).
			with("bytes", len(conf)).
			with("lines", strings.Count(conf, "\n")).
			render(w)
	}
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	res, err := cc.WireGuardConfig(ctx, flagWGTunnel, conf)
	if err != nil {
		return err
	}
	return writeWireguardAction(w, res, "configured")
}

func runWireguardUp(ctx context.Context, w io.Writer) error {
	if !flagWGConfirm {
		return dryRun("start tunnel "+flagWGTunnel).with("tunnel", flagWGTunnel).render(w)
	}
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	res, err := cc.WireGuardStart(ctx, flagWGTunnel)
	if err != nil {
		return err
	}
	return writeWireguardAction(w, res, "started")
}

func runWireguardDown(ctx context.Context, w io.Writer) error {
	if !flagWGConfirm {
		return dryRun("stop tunnel "+flagWGTunnel).with("tunnel", flagWGTunnel).render(w)
	}
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	res, err := cc.WireGuardStop(ctx, flagWGTunnel)
	if err != nil {
		return err
	}
	return writeWireguardAction(w, res, "stopped")
}

func writeWireguardAction(w io.Writer, res *companion.WireGuardActionResponse, _ string) error {
	if flagJSON {
		return writeJSON(w, res)
	}
	_, _ = fmt.Fprintf(w, "wireguard %s  %s", res.Tunnel, res.Status)
	_, _ = fmt.Fprintln(w)
	return nil
}

// writeJSON encodes v as indented JSON to w.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
