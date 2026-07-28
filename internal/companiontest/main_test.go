//go:build companion

package companiontest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/companiontestutil"
	"github.com/hemm-ems/hactl/internal/haapi"
)

const (
	clientID    = "http://hactl-companion-test"
	onboardUser = "testowner"
	onboardPass = "testpass1234!"
	onboardName = "Test Owner"
)

var (
	testClient *companion.Client
	haURL      string
	compURL    string
	composeDir string
	haToken    string // long-lived HA token for E2E tests
	// companionToken authenticates against companion directly. There is no
	// real Supervisor in this stack, so companion's SUPERVISOR_TOKEN (see
	// docker-compose.yaml) is set to this same real HA token once onboarding
	// completes — it doubles as companion's incoming Bearer auth secret and,
	// via CORE_API_URL, its outgoing HA core API token.
	companionToken string
	instanceDir    string // temp dir with .env for hactl CLI E2E tests
	hactlBin       string // path to built hactl binary
)

// TestMain delegates to runTestMain so every `defer` in the setup path actually
// runs. It used to call os.Exit(1) directly from inside the setup sequence,
// which skips deferred work by definition (gocritic exitAfterDefer): the
// context cancels and the temp SUPERVISOR_TOKEN env file — which holds a real
// long-lived HA token — were both leaked on every setup failure.
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	// Every docker/go subprocess below runs under this context, so a future
	// deadline on it can abort a stack that never comes up instead of letting
	// the whole tier hang until the CI job timeout.
	rootCtx := context.Background()

	// Resolve compose file location
	composeDir = resolveComposeDir()

	slog.Info("companion-test: starting stack", "dir", composeDir)

	// Build companion image from local source
	if err := buildCompanionImage(rootCtx, composeDir); err != nil {
		slog.Error("companion-test: build companion image failed", "error", err)
		return 1
	}

	// Start HA only — companion's SUPERVISOR_TOKEN needs a real HA
	// long-lived token (there's no real Supervisor in this stack), and that
	// token only exists after onboarding, so HA must come up first.
	if err := composeUpServices(rootCtx, "homeassistant"); err != nil {
		return setupFailed(rootCtx, "compose up homeassistant failed", err)
	}

	var err error
	haURL, err = getMappedURL(rootCtx, "homeassistant", "8123")
	if err != nil {
		return setupFailed(rootCtx, "get HA port", err)
	}

	slog.Info("companion-test: HA URL", "ha", haURL)

	// Wait for HA
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if readyErr := waitForURL(ctx, haURL+"/api/onboarding"); readyErr != nil {
		return setupFailed(rootCtx, "HA not ready", readyErr)
	}
	slog.Info("companion-test: HA ready")

	// Onboard HA
	var onboardErr error
	if haToken, onboardErr = completeOnboarding(ctx, haURL); onboardErr != nil {
		return setupFailed(rootCtx, "onboarding failed", onboardErr)
	}
	companionToken = haToken
	slog.Info("companion-test: onboarding complete")

	// Start companion with the real HA token as SUPERVISOR_TOKEN.
	envFile, envErr := writeSupervisorTokenEnvFile(companionToken)
	if envErr != nil {
		return setupFailed(rootCtx, "writing supervisor token env file failed", envErr)
	}
	defer os.Remove(envFile) //nolint:errcheck // best-effort cleanup of a temp file

	if upErr := composeUpCompanionWithEnv(rootCtx, envFile); upErr != nil {
		return setupFailed(rootCtx, "compose up companion failed", upErr)
	}

	compURL, err = getMappedURL(rootCtx, "companion", "9100")
	if err != nil {
		return setupFailed(rootCtx, "get companion port", err)
	}

	slog.Info("companion-test: companion URL", "companion", compURL)

	// Wait for companion.
	//
	// This ceiling used to be 60s and it expired on a developer machine that was
	// running three Docker tiers against one daemon: the tier failed before a
	// single test body ran, and — because the next thing that happened was
	// `composeDown -v` — the container that would have said why was already
	// gone. Two things follow, and both are here.
	//
	// The number matches HA's wait above rather than being a second guess at how
	// long a container "should" take. It is a liveness bound, not a performance
	// assertion: nothing is asserted about how long readiness took, and the real
	// upper bound on a hung stack is the tier's own `go test -timeout` and CI's
	// job timeout. A number small enough to be a load detector is a number that
	// fails on someone else's load — the same reasoning that removed the
	// wall-clock ceilings from TestE2EEntRelatedCompanionGraphCLI.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel2()
	if compReadyErr := waitForURL(ctx2, compURL+"/v1/health"); compReadyErr != nil {
		return setupFailed(rootCtx, "companion not ready", compReadyErr)
	}
	slog.Info("companion-test: companion ready")

	// Wait for HA to write config files
	time.Sleep(5 * time.Second)

	// Create client
	testClient = companion.New(compURL, companionToken)

	// Build hactl binary for E2E CLI tests
	var buildErr error
	hactlBin, buildErr = buildHactl(rootCtx)
	if buildErr != nil {
		return setupFailed(rootCtx, "failed to build hactl binary", buildErr)
	}
	slog.Info("companion-test: hactl binary built", "path", hactlBin)

	// Create instanceDir with .env for hactl CLI E2E tests
	var instErr error
	instanceDir, instErr = createE2EInstanceDir(haURL, haToken, compURL, companionToken)
	if instErr != nil {
		return setupFailed(rootCtx, "failed to create E2E instance dir", instErr)
	}
	slog.Info("companion-test: E2E instance dir created", "path", instanceDir)

	// Seed config files for CRUD tests
	if err := seedConfigFiles(); err != nil {
		return setupFailed(rootCtx, "seeding config files failed", err)
	}
	slog.Info("companion-test: config files seeded")

	// This seeding is a starting point, not a guarantee: it writes
	// .storage/core.{config_entries,entity_registry,device_registry} into an
	// already-running HA, which holds those registries in memory and rewrites
	// the files from memory on its own delayed save. Measured on 2026-07-28
	// against HA stable in this very stack: creating one input_boolean helper
	// rewrote core.entity_registry 10.44s later, the fixture's four entity rows
	// were gone (23614 → 19144 bytes), and the companion answered
	// `GET /v1/related/entity` with `404 Entity not found:
	// sensor.hactl_related_source`. So every test that depends on the fixture
	// must re-establish it for itself — see requireRelatedFixture in
	// e2e_test.go — and this call only makes the fixture present for the tests
	// that happen to run before the first flush.
	if err := companiontestutil.SeedRelatedFixture(rootCtx, filepath.Join(composeDir, "docker-compose.yaml"), "companion"); err != nil {
		return setupFailed(rootCtx, "seeding related fixture failed", err)
	}
	slog.Info("companion-test: related fixture seeded")

	// Run tests
	code := m.Run()

	// A failing test body is the case that most needs the containers' own
	// account of what happened, and it was the only case that had none:
	// dumpComposeLogs was wired into the setup-failure paths above, while the
	// `composeDown -v` below runs unconditionally and deletes both containers.
	// Two CI round trips were spent on this tier in two days reasoning about
	// failures whose evidence had already been destroyed — a `tpl create` that
	// reported "HA did not confirm reload" with no record of what HA answered,
	// and an `ent related` whose fixture had been rewritten under it.
	//
	// This cannot mask the real failure: `code` is already decided, both dumps
	// swallow their own errors, and nothing below inspects them.
	if code != 0 {
		slog.Error("companion-test: tests failed — dumping container logs before teardown", "code", code)
		dumpComposeLogs(rootCtx, "homeassistant", failureLogTail)
		dumpComposeLogs(rootCtx, "companion", failureLogTail)
	}

	// Tear down
	if instanceDir != "" {
		_ = os.RemoveAll(instanceDir)
	}
	if hactlBin != "" {
		_ = os.Remove(hactlBin)
	}
	composeDown(rootCtx)
	return code
}

func resolveComposeDir() string {
	// Look for docker-compose.yaml relative to the test file
	candidates := []string{
		".",
		filepath.Join("..", "companiontest"),
		filepath.Join("..", "..", "internal", "companiontest"),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(abs, "docker-compose.yaml")); statErr == nil {
			return abs
		}
	}
	// Fallback: use the directory of this file
	abs, _ := filepath.Abs(".")
	return abs
}

func composeUpServices(ctx context.Context, services ...string) error {
	args := append([]string{"compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "up", "-d"}, services...)
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: fixed docker/go CLI, arguments are test-owned paths and service names
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// composeUpCompanionWithEnv starts the companion service with SUPERVISOR_TOKEN
// substituted from envFile — the container's env is fixed at creation, and the
// real HA token doesn't exist until after HA onboarding, so companion must be
// started separately from (and after) homeassistant.
func composeUpCompanionWithEnv(ctx context.Context, envFile string) error {
	//nolint:gosec // G204: fixed docker CLI, arguments are test-owned paths
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"),
		"--env-file", envFile, "up", "-d", "companion")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func writeSupervisorTokenEnvFile(token string) (string, error) {
	f, err := os.CreateTemp("", "hactl-companiontest-*.env")
	if err != nil {
		return "", fmt.Errorf("creating supervisor token env file: %w", err)
	}
	defer f.Close() //nolint:errcheck // best-effort close, write error checked below
	if _, err := fmt.Fprintf(f, "SUPERVISOR_TOKEN=%s\n", token); err != nil {
		return "", fmt.Errorf("writing supervisor token env file: %w", err)
	}
	return f.Name(), nil
}

// setupLogTail / failureLogTail — how many container log lines each caller of
// dumpComposeLogs asks for. A setup failure happens while HA is still booting,
// so the interesting lines are the last ones; a test-body failure happens
// minutes later, with a whole suite's worth of HA chatter in between, so it gets
// a longer tail. Both are bounded: an unbounded dump of HA's log is megabytes of
// recorder noise that buries the twenty lines that matter.
const (
	setupLogTail   = 200
	failureLogTail = 400
)

// setupFailed reports a setup failure with both containers' logs, then tears the
// stack down. Every failure in runTestMain goes through it, because the two that
// used to dump logs were not the two that needed them: the seedConfigFiles path
// below can fail because HA never set an integration up, and it reported that
// with the containers already deleted. `docker compose logs` for a service that
// was never created prints nothing, which is the right answer for the early
// failures.
func setupFailed(ctx context.Context, what string, err error) int {
	slog.Error("companion-test: "+what, "error", err)
	dumpComposeLogs(ctx, "homeassistant", setupLogTail)
	dumpComposeLogs(ctx, "companion", setupLogTail)
	composeDown(ctx)
	return 1
}

// composeLogs returns the tail of a service's container logs as a string, so a
// failing assertion can quote them inline (see containerDiagnostics) rather than
// only stream them past the test output. Errors are folded into the returned
// text: this is called while something is already failing and must never
// replace the real finding with its own.
func composeLogs(ctx context.Context, service string, tail int) string {
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), //nolint:gosec // G204: fixed docker CLI, arguments are test-owned paths and service names
		"logs", "--no-color", "--tail="+strconv.Itoa(tail), service)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("<could not read %s logs: %v>\n%s", service, err, out)
	}
	return string(out)
}

// dumpComposeLogs prints a service's container logs. Every caller is about to
// run `composeDown -v`, which deletes the only record of what went wrong:
// without this, a readiness timeout reports the URL it gave up on and nothing
// about the process that never answered. Failures here are ignored on purpose —
// this runs while the tier is already failing and must not replace the real
// error with its own.
func dumpComposeLogs(ctx context.Context, service string, tail int) {
	slog.Error("companion-test: dumping container logs before teardown", "service", service)
	_, _ = fmt.Fprintln(os.Stderr, composeLogs(ctx, service, tail))
}

// containerDiagnostics renders what both containers were doing, for a test-body
// failure to carry inline. The companion's `call_service` logs HA's HTTP status
// and body and then returns a bare bool, so when a write reports "HA did not
// confirm reload" the reason exists in exactly two places — HA's log and the
// companion's — and in neither of the strings the test can see. Quoting them at
// the failure means the next occurrence explains itself instead of costing
// another CI round trip.
func containerDiagnostics(ctx context.Context) string {
	var b strings.Builder
	for _, service := range []string{"homeassistant", "companion"} {
		log := composeLogs(ctx, service, failureLogTail)
		_, _ = fmt.Fprintf(&b, "\n--- %s: ERROR/WARNING lines in the last %d ---\n%s", service, failureLogTail, severeLines(log))
		_, _ = fmt.Fprintf(&b, "\n--- %s: last %d log lines ---\n%s", service, failureLogTail, log)
	}
	return b.String()
}

// severeLines extracts the ERROR/WARNING lines from a container log. HA reports
// a rejected service call as one ERROR line inside hundreds of recorder and
// state-machine lines, so the filtered view is what makes the dump readable —
// the full tail is printed alongside it, never instead of it.
func severeLines(log string) string {
	var kept []string
	for line := range strings.SplitSeq(log, "\n") {
		if strings.Contains(line, "ERROR") || strings.Contains(line, "WARNING") {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return "(none)\n"
	}
	return strings.Join(kept, "\n") + "\n"
}

func composeDown(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "down", "-v") //nolint:gosec // G204: fixed docker/go CLI, arguments are test-owned paths and service names
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// buildCompanionImage builds the companion Docker image from the local source tree
// using docker compose build so the image is available when composeUp runs.
func buildCompanionImage(ctx context.Context, composeDir string) error {
	slog.Info("companion-test: building companion image from local source")
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "build", "companion") //nolint:gosec // G204: fixed docker/go CLI, arguments are test-owned paths and service names
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose build companion: %w", err)
	}
	slog.Info("companion-test: companion image built")
	return nil
}

func seedConfigFiles() error {
	ctx := context.Background()

	// Seed template.yaml with a template sensor definition
	templateYAML := `- sensor:
    - name: "Seeded Test Sensor"
      unique_id: "seeded_test_sensor"
      state: "{{ 42 }}"
      unit_of_measurement: "W"
`
	if _, err := testClient.WriteConfigFile(ctx, "template.yaml", templateYAML, false); err != nil {
		return fmt.Errorf("seeding template.yaml: %w", err)
	}

	// Seed scripts.yaml with an empty dict (HA format)
	scriptsYAML := `seeded_test_script:
  alias: "Seeded Test Script"
  mode: single
  sequence:
    - delay: "00:00:01"
`
	if _, err := testClient.WriteConfigFile(ctx, "scripts.yaml", scriptsYAML, false); err != nil {
		return fmt.Errorf("seeding scripts.yaml: %w", err)
	}

	// Seed automations.yaml with a list (HA format)
	automationsYAML := `- id: "seeded_test_auto"
  alias: "Seeded Test Automation"
  mode: single
  trigger:
    - platform: time
      at: "12:00:00"
  action:
    - delay: "00:00:01"
`
	if _, err := testClient.WriteConfigFile(ctx, "automations.yaml", automationsYAML, false); err != nil {
		return fmt.Errorf("seeding automations.yaml: %w", err)
	}

	// HA's default onboarding config wires up automations, scripts and scenes
	// but nothing else, so the files seeded above are dead until we add the
	// !include ourselves. Without `input_boolean:` the companion's helper route
	// refuses the write outright; without `template:` it writes happily and HA
	// never reads the file — no template entity ever appears, and `tpl create`
	// cannot be proven against HA at all.
	if _, err := testClient.WriteConfigFile(ctx, "input_boolean.yaml", "# seeded by companiontest\n", false); err != nil {
		return fmt.Errorf("seeding input_boolean.yaml: %w", err)
	}
	rawConfig, err := testClient.ReadConfigFileRaw(ctx, "configuration.yaml")
	if err != nil {
		return fmt.Errorf("reading configuration.yaml: %w", err)
	}
	newConfig := strings.TrimRight(rawConfig.Content, "\n")
	for _, wiring := range []struct{ key, line string }{
		{"input_boolean:", "input_boolean: !include input_boolean.yaml"},
		{"template:", "template: !include template.yaml"},
	} {
		if !strings.Contains(newConfig, wiring.key) {
			newConfig += "\n" + wiring.line
		}
	}
	newConfig += "\n"
	if newConfig != rawConfig.Content {
		if _, err := testClient.WriteConfigFile(ctx, "configuration.yaml", newConfig, false); err != nil {
			return fmt.Errorf("wiring includes into configuration.yaml: %w", err)
		}
	}

	// A new top-level key is a new integration, and no reload service sets one
	// up — `template.reload` does not even exist until the template integration
	// has been set up once. Only a restart makes HA read the file.
	//
	// Gated on the services, not on whether this run happened to edit the file:
	// the early `return nil` that used to sit here skipped the restart whenever
	// configuration.yaml already carried both !includes, which is the state of
	// any /config a previous run left behind (`docker compose down` without
	// `-v`, or a stack left up for manual poking). Nothing would then have set
	// the template integration up, so `template.reload` would not exist, every
	// `tpl create` in the tier would report "HA did not confirm reload", and the
	// CLI's warning would blame a missing `template: !include template.yaml`
	// that is demonstrably present. hactl-companion records the same trap from
	// the other side in tests/integration/test_live.py.
	return ensureReloadServices()
}

// reloadServicesNeeded are the `<domain>.reload` services this tier's write
// tests depend on: the companion answers every create/apply/delete with
// `reloaded`, and it obtains that by calling `<domain>.reload` over HA's REST
// API. `template` and `input_boolean` are wired by seedConfigFiles above;
// `automation` and `script` come from HA's own default configuration. All four
// are listed because a missing one is invisible until a test 100 lines away
// fails for a reason that names the wrong cause.
var reloadServicesNeeded = []string{"template", "input_boolean", "script", "automation"}

// ensureReloadServices makes HA's service registry a checked precondition of the
// tier instead of a hope. A reload service that does not exist is a 400 "Service
// not found" inside the companion, which reaches the CLI as a bare
// `reloaded: false` — the reason discarded on the way — so without this the
// tier's own setup can hand every write test a misleading failure.
func ensureReloadServices() error {
	ctx := context.Background()

	// One look before restarting: on a /config that already has the integrations
	// set up there is nothing to do, and a restart is 5-10s of the tier's budget.
	if missing, err := missingReloadServices(ctx); err == nil && len(missing) == 0 {
		slog.Info("companion-test: reload services already registered; no restart needed", "services", reloadServicesNeeded)
		return nil
	}

	if err := restartHA(); err != nil {
		return err
	}

	// Polled, not sampled once: waitForCoreRunning returns when HA reports
	// RUNNING, and HA reaches RUNNING while integrations that are slow to set up
	// are still setting up. The elapsed time is logged on success because it is
	// the near-miss detector — if a run ever needs 80 of these 90 seconds, the
	// next change to this tier should know that before it trips.
	//
	// Failing here cannot create a failure mode the tier did not already have:
	// with `template.reload` absent, every `tpl create` reports "HA did not
	// confirm reload" and the tpl round trip fails anyway. This only decides
	// whether that is reported once, at setup, naming the actual cause — with
	// both containers' logs attached by setupFailed — or four tests later
	// blaming an !include that is present.
	start := time.Now()
	missing, err := waitForReloadServices(ctx, reloadServicesWait)
	if err != nil {
		return fmt.Errorf("reading HA's service registry after restart: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("HA registered no %v reload service(s) in the %s after the restart that wired their !includes; "+
			"every create in those families would report \"HA did not confirm reload\" and blame a missing !include that is present "+
			"(HA's log is above; observed once out of 15 local runs, under saturated CPU)", missing, reloadServicesWait)
	}
	slog.Info("companion-test: reload services registered", "services", reloadServicesNeeded, "after", time.Since(start).Round(time.Millisecond))
	return nil
}

// reloadServicesWait is how long HA gets, after it reports RUNNING, to finish
// registering the reload services. Chosen against the tier's own budget: the
// `go test -timeout` is 300s and a healthy run of this tier is ~55s, so a
// setup that burns this whole window still fails inside the budget with logs
// attached rather than being cut off mid-dump.
const reloadServicesWait = 90 * time.Second

// waitForReloadServices polls until every service in reloadServicesNeeded is
// registered, returning the ones still missing when the deadline passes.
func waitForReloadServices(ctx context.Context, within time.Duration) ([]string, error) {
	deadline := time.Now().Add(within)
	for {
		missing, err := missingReloadServices(ctx)
		if err == nil && len(missing) == 0 {
			return nil, nil
		}
		if time.Now().After(deadline) {
			return missing, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// missingReloadServices reports which of reloadServicesNeeded HA does not have a
// `.reload` service for.
func missingReloadServices(ctx context.Context) ([]string, error) {
	client := haapi.New(haURL, haToken)
	var missing []string
	for _, domain := range reloadServicesNeeded {
		exists, err := client.ServiceExists(ctx, domain, "reload")
		if err != nil {
			return nil, fmt.Errorf("checking %s.reload: %w", domain, err)
		}
		if !exists {
			missing = append(missing, domain+".reload")
		}
	}
	return missing, nil
}

// restartHA restarts Home Assistant through its own `homeassistant.restart`
// service and blocks until the core reports RUNNING again — the point at which
// !include'd config is loaded and its entities are in /api/states.
//
// Deliberately not `docker compose restart`: the compose file publishes 8123 on
// an ephemeral host port, and restarting the container re-allocates it, so
// every URL captured at start-up (haURL, and the .env under instanceDir) would
// silently point at a dead port. Restarting inside the container keeps the
// mapping, and is what an operator would do anyway.
func restartHA() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// HA validates the config before restarting and refuses an invalid one, so
	// a 4xx here means the config we just wrote is bad — worth reporting as
	// such rather than as a timeout three minutes later.
	if _, err := doJSONPost(ctx, haURL+"/api/services/homeassistant/restart", haToken, map[string]string{}); err != nil {
		// A restart tears the connection down mid-response; only refuse on a
		// answered rejection, not on the disconnect that means it worked.
		if strings.Contains(err.Error(), "HTTP 4") || strings.Contains(err.Error(), "HTTP 5") {
			return fmt.Errorf("HA refused to restart: %w", err)
		}
		slog.Info("companion-test: restart call did not answer cleanly (expected while HA goes down)", "error", err)
	}

	// Wait for HA to actually go down before waiting for it to come back:
	// polling straight away would see the pre-restart RUNNING and return at once.
	if err := waitForCoreNotRunning(ctx, haURL, haToken); err != nil {
		return err
	}
	if err := waitForCoreRunning(ctx, haURL, haToken); err != nil {
		return fmt.Errorf("HA did not come back after restart: %w", err)
	}
	slog.Info("companion-test: HA restarted and RUNNING")
	return nil
}

// waitForCoreNotRunning blocks until HA stops reporting RUNNING — either it is
// unreachable or it reports an earlier bootstrap state.
func waitForCoreNotRunning(ctx context.Context, baseURL, token string) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		state, stateErr := coreState(ctx, baseURL, token)
		if stateErr != nil {
			// HA refusing the connection is the strongest possible form of
			// "not RUNNING", so the error is the answer, not a failure.
			return nil //nolint:nilerr // unreachable == not running, by definition
		}
		if state != "RUNNING" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("HA never went down after homeassistant.restart")
}

// waitForCoreRunning polls /api/config until HA reports "RUNNING". HA answers
// that endpoint while still bootstrapping, reporting "STARTING", so a plain
// reachability probe returns long before the config is actually loaded.
// (Duplicated from hatest.waitForRunning to keep this package standalone.)
func waitForCoreRunning(ctx context.Context, baseURL, token string) error {
	last := "<unreachable>"
	for {
		if state, err := coreState(ctx, baseURL, token); err == nil {
			if state == "RUNNING" {
				return nil
			}
			last = state
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("HA not RUNNING before deadline (last state %q): %w", last, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// coreState returns HA's overall core state ("STARTING", "RUNNING", …).
func coreState(ctx context.Context, baseURL, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/config", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close in a poll loop
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d from /api/config", resp.StatusCode)
	}
	var cfg struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return "", err
	}
	return cfg.State, nil
}

func getMappedURL(ctx context.Context, service, port string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "port", service, port) //nolint:gosec // G204: fixed docker/go CLI, arguments are test-owned paths and service names
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("get port for %s:%s: %w", service, port, err)
	}
	hostPort := strings.TrimSpace(string(out))
	// On Windows, docker compose port may return 0.0.0.0:PORT — normalize to localhost
	hostPort = strings.Replace(hostPort, "0.0.0.0", "localhost", 1)
	return "http://" + hostPort, nil
}

func waitForURL(ctx context.Context, targetURL string) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s", targetURL)
		default:
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if reqErr != nil {
			return fmt.Errorf("building readiness request for %s: %w", targetURL, reqErr)
		}
		resp, getErr := http.DefaultClient.Do(req)
		if getErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// --- Headless onboarding (duplicated from hatest.go for package independence) ---

func completeOnboarding(ctx context.Context, baseURL string) (string, error) {
	authCode, err := createOwnerUser(ctx, baseURL)
	if err != nil {
		return "", fmt.Errorf("creating owner: %w", err)
	}

	accessToken, err := exchangeAuthCode(ctx, baseURL, authCode)
	if err != nil {
		return "", fmt.Errorf("exchanging auth code: %w", err)
	}

	if stepErr := completeStep(ctx, baseURL, accessToken, "/api/onboarding/core_config"); stepErr != nil {
		return "", fmt.Errorf("completing core_config: %w", stepErr)
	}

	if stepErr := completeStep(ctx, baseURL, accessToken, "/api/onboarding/analytics"); stepErr != nil {
		return "", fmt.Errorf("completing analytics: %w", stepErr)
	}

	llToken, err := createLongLivedToken(ctx, baseURL, accessToken)
	if err != nil {
		return "", fmt.Errorf("creating long-lived token: %w", err)
	}

	return llToken, nil
}

func createOwnerUser(ctx context.Context, baseURL string) (string, error) {
	body := map[string]string{
		"client_id": clientID,
		"name":      onboardName,
		"username":  onboardUser,
		"password":  onboardPass,
		"language":  "en",
	}
	data, err := doJSONPost(ctx, baseURL+"/api/onboarding/users", "", body)
	if err != nil {
		return "", err
	}
	var resp struct {
		AuthCode string `json:"auth_code"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing onboarding response: %w (body: %s)", err, string(data))
	}
	if resp.AuthCode == "" {
		return "", fmt.Errorf("empty auth_code in response: %s", string(data))
	}
	return resp.AuthCode, nil
}

func exchangeAuthCode(ctx context.Context, baseURL, authCode string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", authCode)
	form.Set("client_id", clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(data))
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &tokenResp); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}
	return tokenResp.AccessToken, nil
}

func completeStep(ctx context.Context, baseURL, token, path string) error {
	_, err := doJSONPost(ctx, baseURL+path, token, map[string]string{})
	return err
}

func createLongLivedToken(ctx context.Context, baseURL, accessToken string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	u.Scheme = "ws"
	u.Path = "/api/websocket"

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil) //nolint:bodyclose
	if err != nil {
		return "", fmt.Errorf("ws connect: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		return "", fmt.Errorf("reading auth_required: %w", err)
	}

	if err := conn.WriteJSON(map[string]string{
		"type":         "auth",
		"access_token": accessToken,
	}); err != nil {
		return "", fmt.Errorf("sending auth: %w", err)
	}

	if err := conn.ReadJSON(&msg); err != nil {
		return "", fmt.Errorf("reading auth_ok: %w", err)
	}
	if msg["type"] != "auth_ok" {
		return "", fmt.Errorf("expected auth_ok, got: %v", msg["type"])
	}

	if err := conn.WriteJSON(map[string]any{
		"id":          1,
		"type":        "auth/long_lived_access_token",
		"client_name": "hactl-companion-e2e",
		"lifespan":    365,
	}); err != nil {
		return "", fmt.Errorf("sending ll token request: %w", err)
	}

	var tokenResp struct {
		Result  string `json:"result"`
		Success bool   `json:"success"`
	}
	if err := conn.ReadJSON(&tokenResp); err != nil {
		return "", fmt.Errorf("reading ll token response: %w", err)
	}
	if !tokenResp.Success {
		return "", errors.New("ll token creation failed")
	}

	return tokenResp.Result, nil
}

func doJSONPost(ctx context.Context, targetURL, token string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

// buildHactl compiles the hactl binary from source into a temp file.
// Returns the path to the binary.
func buildHactl(ctx context.Context) (string, error) {
	f, err := os.CreateTemp("", "hactl-e2e-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file for binary: %w", err)
	}
	binPath := f.Name()
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing temp file for binary: %w", err)
	}

	slog.Info("companion-test: building hactl binary", "output", binPath)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, "github.com/hemm-ems/hactl/cmd/hactl") //nolint:gosec // G204: fixed docker/go CLI, arguments are test-owned paths and service names
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(binPath)
		return "", fmt.Errorf("go build hactl: %w", err)
	}
	return binPath, nil
}

// createE2EInstanceDir writes a .env with HA + companion credentials for CLI E2E tests.
func createE2EInstanceDir(haBaseURL, haAccessToken, companionBaseURL, compToken string) (string, error) {
	dir, err := os.MkdirTemp("", "hactl-e2e-instance-*")
	if err != nil {
		return "", fmt.Errorf("creating E2E instance dir: %w", err)
	}
	env := fmt.Sprintf(
		"HA_URL=%s\nHA_TOKEN=%s\nCOMPANION_URL=%s\nCOMPANION_TOKEN=%s\n",
		haBaseURL, haAccessToken, companionBaseURL, compToken,
	)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("writing .env: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o750); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("creating cache dir: %w", err)
	}
	return dir, nil
}
