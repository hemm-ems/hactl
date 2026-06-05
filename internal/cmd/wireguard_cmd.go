package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
)

var (
	flagWGTunnel   string
	flagWGConfFile string
	flagWGConfirm  bool
)

var wireguardCmd = &cobra.Command{
	Use:   "wireguard",
	Short: "Manage the companion WireGuard tunnel (remote lifeline)",
	Long: "Configure, bring up/down, and inspect the companion's WireGuard tunnel.\n\n" +
		"This is the lifeline hactl rides over for remote access. The endpoints are\n" +
		"Ingress-only; this command handles the Supervisor Ingress session auth\n" +
		"automatically (a plain bearer-token request gets 401). Mutations are dry-run\n" +
		"by default — pass --confirm to apply.",
}

var wireguardStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show WireGuard tunnel status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWireguardStatus(cmd.Context(), cmd.OutOrStdout())
	},
}

var wireguardConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Push a WireGuard .conf to the companion (persisted on /data)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWireguardConfig(cmd.Context(), cmd.OutOrStdout())
	},
}

var wireguardUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Bring the tunnel up",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWireguardUp(cmd.Context(), cmd.OutOrStdout())
	},
}

var wireguardDownCmd = &cobra.Command{
	Use:   "down",
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
		// Show the most recent resolved address if we have one.
		ip := ""
		for _, v := range m.Resolved {
			ip = v
			break
		}
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
		_, _ = fmt.Fprintf(w, "would push %d-byte config to tunnel %s (%d lines)\n",
			len(conf), flagWGTunnel, strings.Count(conf, "\n"))
		_, _ = fmt.Fprintln(w, "dry-run: use --confirm to apply")
		return nil
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
		_, _ = fmt.Fprintf(w, "would start tunnel %s\n", flagWGTunnel)
		_, _ = fmt.Fprintln(w, "dry-run: use --confirm to apply")
		return nil
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
		_, _ = fmt.Fprintf(w, "would stop tunnel %s\n", flagWGTunnel)
		_, _ = fmt.Fprintln(w, "dry-run: use --confirm to apply")
		return nil
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
