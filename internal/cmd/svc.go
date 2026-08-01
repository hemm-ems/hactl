package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
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

	// Resolve the target AND judge the payload before printing a plan (H-2).
	// A preview that only checked for a dot presented "would call:
	// light.turn_onn" as a verified action; --confirm then failed with HA's
	// 400. The second half is the same defect one layer in: the service name
	// resolved and the DATA went unexamined, so `--data '{"target":{…}}'`
	// previewed as a clean plan and --confirm answered 400. A probe that
	// cannot reach HA warns rather than refuses — an unreachable instance is
	// not evidence that the service is missing.
	desc, probeErr := client.LookupService(ctx, domain, service)
	switch {
	case errors.Is(probeErr, haapi.ErrServiceNotRegistered):
		return fmt.Errorf("service %s.%s is not registered in Home Assistant — check the domain and spelling (HA lists them under Developer Tools → Actions)", domain, service)
	case probeErr != nil:
		slog.Warn("could not check the service registry; the plan below is unverified", "error", probeErr)
	default:
		if refusal := refuseUnusableServiceData(domain, service, desc, data); refusal != nil {
			return refusal
		}
	}

	if !flagSvcConfirm {
		plan := dryRun(fmt.Sprintf("call %s.%s", domain, service)).
			with("domain", domain).
			with("service", service).
			with("data", string(jsonData))
		if reach, ok := serviceCallReach(domain, desc, data); ok {
			plan = plan.with("targets", reach)
		}
		return plan.
			withHint("re-run with --confirm after the user explicitly confirms this exact action").
			render(w)
	}

	if flagSvcReturn {
		resp, callErr := client.CallServiceWithResponse(ctx, domain, service, data)
		if callErr != nil {
			return fmt.Errorf("calling %s.%s: %w", domain, service, callErr)
		}
		if flagJSON {
			_, _ = w.Write(resp)
			_, _ = fmt.Fprintln(w)
		} else {
			_, _ = fmt.Fprintf(w, "called %s.%s\nresponse: %s\n", domain, service, resp)
		}
		return nil
	}

	changed, err := client.CallService(ctx, domain, service, data)
	if err != nil {
		return fmt.Errorf("calling %s.%s: %w", domain, service, err)
	}
	// What HA attributed to the call, in its own words. The list used to be
	// discarded, so "called input_boolean.toggle" was the whole report whether
	// an entity had changed or nothing at all had matched — and an agent
	// verifying its own action had to go and read the state back to find out.
	// It is reported as what it is: zero changes is not the claim "nothing
	// matched", because HA answers zero for a call that fired asynchronously
	// too (automation.trigger on a live automation, measured).
	ids := make([]string, 0, len(changed))
	for _, state := range changed {
		ids = append(ids, state.EntityID)
	}
	res := done(fmt.Sprintf("call %s.%s", domain, service)).
		with("domain", domain).
		with("service", service).
		with("data", string(jsonData)).
		with("changed_entities", ids).
		text("called %s.%s", domain, service)
	if len(ids) > 0 {
		res = res.text("changed: %s", strings.Join(ids, ", "))
	} else {
		res = res.text("changed: none reported (Home Assistant attributes no state change to this call; " +
			"a service that acts asynchronously and one that matched no entity both answer this)")
	}
	return res.render(w)
}

// refuseUnusableServiceData ends the command on a payload Home Assistant will
// answer with 400, in preview exactly as under --confirm (H-2). It refuses
// only what HA's own service registry says is wrong: see the measurements in
// internal/haapi/servicedata.go, including the two shapes it must NOT refuse
// (a service that publishes no schema, an entity_id that is merely absent).
func refuseUnusableServiceData(domain, service string, desc *haapi.ServiceDescriptor, data map[string]any) error {
	if unknown := desc.UnknownFields(data); len(unknown) > 0 {
		hint := ""
		if slices.Contains(unknown, "target") {
			hint = " — `target:` is automation/script YAML syntax, which Home Assistant flattens before it " +
				"calls the service; a service call takes those keys at the top level (\"entity_id\": …)"
		}
		return fmt.Errorf("%s.%s does not take %s: Home Assistant refuses an undeclared service field with 400%s. "+
			"It accepts: %s",
			domain, service, quoteAll(unknown), hint, strings.Join(desc.AcceptedFields(), ", "))
	}
	if bad := haapi.MalformedEntityIDs(data); len(bad) > 0 {
		return fmt.Errorf("entity_id %s is not a valid Home Assistant entity id: expected <domain>.<object_id>, "+
			"lowercase letters, digits and single underscores (HA refuses it with 400)", quoteAll(bad))
	}
	return nil
}

// serviceCallReach states how wide the call is when the answer is not the
// obvious one, and reports false when there is nothing worth saying.
//
// Two shapes qualify, and they are opposites. A targeted service called with
// no selector reaches NOTHING — HA's target extraction returns before it looks
// at an entity — and a preview that renders identically to a single-entity
// call is why finding #44 read it as a broadcast. `entity_id: all` IS the
// broadcast, and the preview says so.
func serviceCallReach(domain string, desc *haapi.ServiceDescriptor, data map[string]any) (string, bool) {
	if desc == nil || desc.Target == nil {
		return "", false
	}
	targeted, matchAll := haapi.TargetsAnything(data)
	switch {
	case matchAll:
		return fmt.Sprintf("EVERY entity in the %s domain (entity_id: all)", domain), true
	case !targeted:
		return "none — this service takes a target and the data carries no " +
			strings.Join(haapi.TargetSelectorFields, "/") +
			", so Home Assistant would select no entity and the call would reach nothing", true
	}
	return "", false
}

// quoteAll renders a list of offending values the way an error message reads
// best: one quoted value, or a quoted comma-separated list.
func quoteAll(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return strings.Join(quoted, ", ")
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
