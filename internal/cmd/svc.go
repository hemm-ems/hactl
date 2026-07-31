package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var flagSvcData string
var flagSvcReturn bool
var flagSvcConfirm bool

var svcCmd = family(&cobra.Command{
	Use:        "svc",
	SuggestFor: []string{"service", "services", "call"},
	Short:      "Call Home Assistant services",
	Long:       "Invoke HA service calls (e.g. group.set, input_boolean.turn_on).",
})

var svcCallCmd = &cobra.Command{
	Use:   "call <domain>.<service>",
	Short: "Call a service (dry-run by default)",
	Long:  "Call a HA service. Use --data for JSON service data. Dry-run by default: prints the planned call without executing it; pass --confirm to actually call.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSvcCall(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	svcCallCmd.Flags().StringVarP(&flagSvcData, "data", "d", "{}", "JSON service data (use @file.json to read from file)")
	svcCallCmd.Flags().BoolVar(&flagSvcReturn, "return", false, "request and print the service response (return_response=true)")
	svcCallCmd.Flags().BoolVar(&flagSvcConfirm, "confirm", false, "actually call the service (default is dry-run)")
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

	// Resolve the target before printing a plan (H-2). A preview that only
	// checked for a dot presented "would call: light.turn_onn" as a verified
	// action; --confirm then failed with HA's 400. A probe that cannot reach
	// HA warns rather than refuses — an unreachable instance is not evidence
	// that the service is missing.
	switch exists, probeErr := client.ServiceExists(ctx, domain, service); {
	case probeErr != nil:
		slog.Warn("could not check the service registry; the plan below is unverified", "error", probeErr)
	case !exists:
		return fmt.Errorf("service %s.%s is not registered in Home Assistant — check the domain and spelling (HA lists them under Developer Tools → Actions)", domain, service)
	}

	if !flagSvcConfirm {
		return dryRun(fmt.Sprintf("call %s.%s", domain, service)).
			with("domain", domain).
			with("service", service).
			with("data", string(jsonData)).
			withHint("re-run with --confirm after the user explicitly confirms this exact action").
			render(w)
	}

	if flagSvcReturn {
		resp, err := client.CallServiceWithResponse(ctx, domain, service, data)
		if err != nil {
			return fmt.Errorf("calling %s.%s: %w", domain, service, err)
		}
		if flagJSON {
			_, _ = w.Write(resp)
			_, _ = fmt.Fprintln(w)
		} else {
			_, _ = fmt.Fprintf(w, "called %s.%s\nresponse: %s\n", domain, service, resp)
		}
		return nil
	}

	if err := client.CallService(ctx, domain, service, data); err != nil {
		return fmt.Errorf("calling %s.%s: %w", domain, service, err)
	}
	return done(fmt.Sprintf("call %s.%s", domain, service)).
		with("domain", domain).
		with("service", service).
		with("data", string(jsonData)).
		text("called %s.%s", domain, service).
		render(w)
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
