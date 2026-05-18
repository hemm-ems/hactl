package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var flagSvcData string

var svcCmd = &cobra.Command{
	Use:   "svc",
	Short: "Call Home Assistant services",
	Long:  "Invoke HA service calls (e.g. group.set, input_boolean.turn_on).",
}

var svcCallCmd = &cobra.Command{
	Use:   "call <domain>.<service>",
	Short: "Call a service",
	Long:  "Call a HA service. Use --data for JSON service data.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSvcCall(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	svcCallCmd.Flags().StringVarP(&flagSvcData, "data", "d", "{}", "JSON service data (use @file.json to read from file)")
	svcCmd.AddCommand(svcCallCmd)
	rootCmd.AddCommand(svcCmd)
}

func runSvcCall(ctx context.Context, w io.Writer, target string) error {
	domain, service, found := strings.Cut(target, ".")
	if !found {
		return fmt.Errorf("invalid service format %q: expected domain.service (e.g. group.set)", target)
	}

	jsonData, err := resolveData(flagSvcData)
	if err != nil {
		return err
	}

	var data map[string]any
	if unmarshalErr := json.Unmarshal(jsonData, &data); unmarshalErr != nil {
		return fmt.Errorf("invalid --data JSON: %w", unmarshalErr)
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	if err := client.CallService(ctx, domain, service, data); err != nil {
		return fmt.Errorf("calling %s.%s: %w", domain, service, err)
	}

	_, _ = fmt.Fprintf(w, "called %s.%s\n", domain, service)
	return nil
}

// resolveData returns JSON bytes from either inline JSON or a @file reference.
func resolveData(s string) ([]byte, error) {
	if after, ok := strings.CutPrefix(s, "@"); ok {
		data, err := os.ReadFile(after) //nolint:gosec // user-provided file path by design
		if err != nil {
			return nil, fmt.Errorf("reading data file %q: %w", after, err)
		}
		// Strip UTF-8 BOM if present
		data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
		return bytes.TrimSpace(data), nil
	}
	return []byte(s), nil
}
