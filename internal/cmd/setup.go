package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactive first-time setup — creates ~/.hactl/default/.env",
	Long:  "Guides you through connecting hactl to a Home Assistant instance.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Write directly to os.Stdout so interactive prompts are visible
		// immediately — the root Execute() buffers cmd.OutOrStdout(), which
		// would hide prompts until after stdin is fully read (silent hang).
		return runSetup(cmd.Context(), os.Stdout, os.Stdin)
	},
}

func init() {
	rootCmd.AddCommand(setupCmd)
}

func runSetup(ctx context.Context, out io.Writer, in io.Reader) error {
	var dir string
	if flagDir != "" {
		abs, err := filepath.Abs(flagDir)
		if err != nil {
			return fmt.Errorf("invalid --dir: %w", err)
		}
		dir = abs
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory: %w", err)
		}
		dir = filepath.Join(home, ".hactl", "default")
	}
	envPath := filepath.Join(dir, ".env")

	reader := bufio.NewReader(in)

	_, _ = fmt.Fprintf(out, "hactl setup\n")
	_, _ = fmt.Fprintf(out, "===========\n")
	_, _ = fmt.Fprintf(out, "This will create %s\n\n", envPath)

	// Check if .env already exists
	if _, statErr := os.Stat(envPath); statErr == nil {
		cfg, loadErr := config.Load(dir)
		if loadErr == nil {
			_, _ = fmt.Fprintf(out, "Existing config found:\n")
			_, _ = fmt.Fprintf(out, "  HA_URL:   %s\n", cfg.URL)
			_, _ = fmt.Fprintf(out, "  HA_TOKEN: %s\n", maskToken(cfg.Token))
			if cfg.CompanionURL != "" {
				_, _ = fmt.Fprintf(out, "  COMPANION_URL: %s\n", cfg.CompanionURL)
			}
			_, _ = fmt.Fprintf(out, "\nOverwrite? [y/N] ")
			answer := readLine(reader)
			if !strings.EqualFold(strings.TrimSpace(answer), "y") {
				_, _ = fmt.Fprintf(out, "Keeping existing config.\n")
				return nil
			}
		}
	}

	// Prompt for HA_URL
	_, _ = fmt.Fprintf(out, "Home Assistant URL [http://homeassistant.local:8123]: ")
	haURL := strings.TrimSpace(readLine(reader))
	if haURL == "" {
		haURL = "http://homeassistant.local:8123"
	}
	haURL = strings.TrimRight(haURL, "/")

	// Prompt for HA_TOKEN
	_, _ = fmt.Fprintf(out, "\nLong-lived access token:\n")
	_, _ = fmt.Fprintf(out, "  HA → Profile → Long-lived access tokens → Create token\n")
	_, _ = fmt.Fprintf(out, "HA_TOKEN: ")
	haToken := strings.TrimSpace(readLine(reader))
	if haToken == "" {
		return errors.New("HA_TOKEN is required")
	}

	// Test connectivity — fail fast: do not write .env on error.
	_, _ = fmt.Fprintf(out, "\nTesting connection to %s ...", haURL)
	client := haapi.New(haURL, haToken)
	if _, err := client.GetAPIStatus(ctx); err != nil {
		_, _ = fmt.Fprintf(out, " FAILED\n")
		errMsg := err.Error()
		if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403") || strings.Contains(errMsg, "Unauthorized") || strings.Contains(errMsg, "Forbidden") {
			return errors.New("authentication failed: HA_TOKEN is invalid or lacks required scope\n\nFix the token in HA → Profile → Long-lived access tokens, then run hactl setup again")
		}
		return fmt.Errorf("cannot reach Home Assistant at %s\n\nCheck that HA_URL is correct and the instance is reachable, then run hactl setup again.\nDetail: %w", haURL, err)
	}
	_, _ = fmt.Fprintf(out, " OK\n")

	// Write .env
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return fmt.Errorf("cannot create config directory: %w", mkErr)
	}
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=%s\n", haURL, haToken)
	if writeErr := os.WriteFile(envPath, []byte(envContent), 0o600); writeErr != nil {
		return fmt.Errorf("cannot write .env: %w", writeErr)
	}
	_, _ = fmt.Fprintf(out, "Config written to %s\n", envPath)

	// Check companion (non-fatal)
	_, _ = fmt.Fprintf(out, "\nChecking for hactl-companion add-on ...\n")
	fakeCfg := &config.Config{URL: haURL, Token: haToken}
	ws := haapi.NewWSClient(haURL, haToken)
	var wsClient *haapi.WSClient
	if wsErr := ws.Connect(ctx); wsErr == nil {
		defer func() { _ = ws.Close() }()
		wsClient = ws
	}
	companionURL, discoverErr := companion.Discover(ctx, fakeCfg, wsClient)
	if discoverErr != nil {
		_, _ = fmt.Fprintf(out, "  Companion not found — install the hactl-companion add-on from\n")
		_, _ = fmt.Fprintf(out, "  Settings → Add-ons to unlock full config editing.\n")
		_, _ = fmt.Fprintf(out, "  No separate secret is needed: HA Ingress handles authentication automatically.\n")
	} else {
		cc := companion.New(companionURL, haToken)
		if wsClient != nil {
			cc = cc.WithIngressAuth(wsClient)
		}
		if h, hErr := cc.Health(ctx); hErr == nil {
			_, _ = fmt.Fprintf(out, "  Companion found: %s (v%s)\n", companionURL, h.Version)
		} else {
			_, _ = fmt.Fprintf(out, "  Companion URL discovered but health check failed: %v\n", hErr)
		}
	}

	_, _ = fmt.Fprintf(out, "\nSetup complete. Run 'hactl health' to verify.\n")
	return nil
}

// maskToken returns the first 4 and last 4 characters of a token with *** in between.
func maskToken(t string) string {
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "***" + t[len(t)-4:]
}

// readLine reads a single line from r, trimming the trailing newline.
func readLine(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}
