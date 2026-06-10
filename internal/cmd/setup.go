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

var (
	flagSetupURL   string
	flagSetupToken string
	flagSetupForce bool
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "First-time setup — creates .env in the current directory",
	Long: "Guides you through connecting hactl to a Home Assistant instance.\n\n" +
		"Non-interactive (for scripts and agents): pass both --url and --token.\n" +
		"Use --token - to read the token from stdin instead of the command line.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Write directly to os.Stdout so interactive prompts are visible
		// immediately — the root Execute() buffers cmd.OutOrStdout(), which
		// would hide prompts until after stdin is fully read (silent hang).
		return runSetup(cmd.Context(), os.Stdout, os.Stdin)
	},
}

func init() {
	setupCmd.Flags().StringVar(&flagSetupURL, "url", "", "HA URL (with --token: non-interactive setup)")
	setupCmd.Flags().StringVar(&flagSetupToken, "token", "", "long-lived access token; use - to read from stdin")
	setupCmd.Flags().BoolVar(&flagSetupForce, "force", false, "overwrite an existing .env without asking (non-interactive mode)")
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
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine current directory: %w", err)
		}
		dir = cwd
	}
	envPath := filepath.Join(dir, ".env")

	reader := bufio.NewReader(in)

	haURL, haToken, aborted, err := setupGatherInput(out, reader, dir, envPath)
	if err != nil || aborted {
		return err
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

// setupGatherInput determines HA_URL and HA_TOKEN, either from the --url and
// --token flags (non-interactive) or by prompting. aborted is true when the
// user declined to overwrite an existing config (a graceful no-op).
func setupGatherInput(out io.Writer, reader *bufio.Reader, dir, envPath string) (haURL, haToken string, aborted bool, err error) {
	if (flagSetupURL != "") != (flagSetupToken != "") {
		return "", "", false, errors.New("non-interactive setup needs both --url and --token")
	}

	if flagSetupURL != "" {
		_, _ = fmt.Fprintf(out, "hactl setup (non-interactive)\n")
		if _, statErr := os.Stat(envPath); statErr == nil && !flagSetupForce {
			return "", "", false, fmt.Errorf("%s already exists — pass --force to overwrite", envPath)
		}
		haURL = strings.TrimRight(strings.TrimSpace(flagSetupURL), "/")
		haToken = strings.TrimSpace(flagSetupToken)
		if haToken == "-" {
			haToken = strings.TrimSpace(readLine(reader))
		}
		if haToken == "" {
			return "", "", false, errors.New("HA_TOKEN is required")
		}
		return haURL, haToken, false, nil
	}

	_, _ = fmt.Fprintf(out, "hactl setup\n")
	_, _ = fmt.Fprintf(out, "===========\n")
	_, _ = fmt.Fprintf(out, "This will create %s\n\n", envPath)

	if !setupConfirmOverwrite(out, reader, dir, envPath) {
		return "", "", true, nil
	}

	// Prompt for HA_URL
	_, _ = fmt.Fprintf(out, "Home Assistant URL [http://homeassistant.local:8123]: ")
	haURL = strings.TrimSpace(readLine(reader))
	if haURL == "" {
		haURL = "http://homeassistant.local:8123"
	}
	haURL = strings.TrimRight(haURL, "/")

	// Prompt for HA_TOKEN
	_, _ = fmt.Fprintf(out, "\nLong-lived access token:\n")
	_, _ = fmt.Fprintf(out, "  HA → Profile → Long-lived access tokens → Create token\n")
	_, _ = fmt.Fprintf(out, "HA_TOKEN: ")
	haToken = strings.TrimSpace(readLine(reader))
	if haToken == "" {
		return "", "", false, errors.New("HA_TOKEN is required")
	}
	return haURL, haToken, false, nil
}

// setupConfirmOverwrite shows any existing config and asks before overwriting.
// Returns false when the user wants to keep the existing config.
func setupConfirmOverwrite(out io.Writer, reader *bufio.Reader, dir, envPath string) bool {
	if _, statErr := os.Stat(envPath); statErr != nil {
		return true
	}
	cfg, loadErr := config.Load(dir)
	if loadErr != nil {
		return true
	}
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
		return false
	}
	return true
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
