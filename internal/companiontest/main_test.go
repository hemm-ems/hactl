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
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/companiontestutil"
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

func TestMain(m *testing.M) {
	// Resolve compose file location
	composeDir = resolveComposeDir()

	slog.Info("companion-test: starting stack", "dir", composeDir)

	// Build companion image from local source
	if err := buildCompanionImage(composeDir); err != nil {
		slog.Error("companion-test: build companion image failed", "error", err)
		os.Exit(1)
	}

	// Start HA only — companion's SUPERVISOR_TOKEN needs a real HA
	// long-lived token (there's no real Supervisor in this stack), and that
	// token only exists after onboarding, so HA must come up first.
	if err := composeUpServices("homeassistant"); err != nil {
		slog.Error("companion-test: compose up homeassistant failed", "error", err)
		os.Exit(1)
	}

	var err error
	haURL, err = getMappedURL("homeassistant", "8123")
	if err != nil {
		slog.Error("companion-test: get HA port", "error", err)
		composeDown()
		os.Exit(1)
	}

	slog.Info("companion-test: HA URL", "ha", haURL)

	// Wait for HA
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := waitForURL(ctx, haURL+"/api/onboarding"); err != nil {
		slog.Error("companion-test: HA not ready", "error", err)
		composeDown()
		os.Exit(1)
	}
	slog.Info("companion-test: HA ready")

	// Onboard HA
	var onboardErr error
	if haToken, onboardErr = completeOnboarding(ctx, haURL); onboardErr != nil {
		slog.Error("companion-test: onboarding failed", "error", onboardErr)
		composeDown()
		os.Exit(1)
	}
	companionToken = haToken
	slog.Info("companion-test: onboarding complete")

	// Start companion with the real HA token as SUPERVISOR_TOKEN.
	envFile, envErr := writeSupervisorTokenEnvFile(companionToken)
	if envErr != nil {
		slog.Error("companion-test: writing supervisor token env file failed", "error", envErr)
		composeDown()
		os.Exit(1)
	}
	defer os.Remove(envFile) //nolint:errcheck // best-effort cleanup of a temp file

	if err := composeUpCompanionWithEnv(envFile); err != nil {
		slog.Error("companion-test: compose up companion failed", "error", err)
		composeDown()
		os.Exit(1)
	}

	compURL, err = getMappedURL("companion", "9100")
	if err != nil {
		slog.Error("companion-test: get companion port", "error", err)
		composeDown()
		os.Exit(1)
	}

	slog.Info("companion-test: companion URL", "companion", compURL)

	// Wait for companion
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()
	if err := waitForURL(ctx2, compURL+"/v1/health"); err != nil {
		slog.Error("companion-test: companion not ready", "error", err)
		composeDown()
		os.Exit(1)
	}
	slog.Info("companion-test: companion ready")

	// Wait for HA to write config files
	time.Sleep(5 * time.Second)

	// Create client
	testClient = companion.New(compURL, companionToken)

	// Build hactl binary for E2E CLI tests
	var buildErr error
	hactlBin, buildErr = buildHactl()
	if buildErr != nil {
		slog.Error("companion-test: failed to build hactl binary", "error", buildErr)
		composeDown()
		os.Exit(1)
	}
	slog.Info("companion-test: hactl binary built", "path", hactlBin)

	// Create instanceDir with .env for hactl CLI E2E tests
	var instErr error
	instanceDir, instErr = createE2EInstanceDir(haURL, haToken, compURL, companionToken)
	if instErr != nil {
		slog.Error("companion-test: failed to create E2E instance dir", "error", instErr)
		composeDown()
		os.Exit(1)
	}
	slog.Info("companion-test: E2E instance dir created", "path", instanceDir)

	// Seed config files for CRUD tests
	if err := seedConfigFiles(); err != nil {
		slog.Error("companion-test: seeding config files failed", "error", err)
		composeDown()
		os.Exit(1)
	}
	slog.Info("companion-test: config files seeded")

	if err := companiontestutil.SeedRelatedFixture(filepath.Join(composeDir, "docker-compose.yaml"), "companion"); err != nil {
		slog.Error("companion-test: seeding related fixture failed", "error", err)
		composeDown()
		os.Exit(1)
	}
	slog.Info("companion-test: related fixture seeded")

	// Run tests
	code := m.Run()

	// Tear down
	if instanceDir != "" {
		_ = os.RemoveAll(instanceDir)
	}
	if hactlBin != "" {
		_ = os.Remove(hactlBin)
	}
	composeDown()
	os.Exit(code)
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

func composeUpServices(services ...string) error {
	args := append([]string{"compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "up", "-d"}, services...)
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// composeUpCompanionWithEnv starts the companion service with SUPERVISOR_TOKEN
// substituted from envFile — the container's env is fixed at creation, and the
// real HA token doesn't exist until after HA onboarding, so companion must be
// started separately from (and after) homeassistant.
func composeUpCompanionWithEnv(envFile string) error {
	cmd := exec.Command("docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"),
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

func composeDown() {
	cmd := exec.Command("docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "down", "-v")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// buildCompanionImage builds the companion Docker image from the local source tree
// using docker compose build so the image is available when composeUp runs.
func buildCompanionImage(composeDir string) error {
	slog.Info("companion-test: building companion image from local source")
	cmd := exec.Command("docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "build", "companion")
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
	if newConfig == rawConfig.Content {
		return nil
	}
	if _, err := testClient.WriteConfigFile(ctx, "configuration.yaml", newConfig, false); err != nil {
		return fmt.Errorf("wiring includes into configuration.yaml: %w", err)
	}

	// A new top-level key is a new integration, and no reload service sets one
	// up — `template.reload` does not even exist until the template integration
	// has been set up once. Only a restart makes HA read the file.
	return restartHA()
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
		if state, err := coreState(ctx, baseURL, token); err != nil || state != "RUNNING" {
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

func getMappedURL(service, port string) (string, error) {
	cmd := exec.Command("docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "port", service, port)
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
		resp, err := http.Get(targetURL) //nolint:gosec // test URL
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
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

	if err := completeStep(ctx, baseURL, accessToken, "/api/onboarding/core_config"); err != nil {
		return "", fmt.Errorf("completing core_config: %w", err)
	}

	if err := completeStep(ctx, baseURL, accessToken, "/api/onboarding/analytics"); err != nil {
		return "", fmt.Errorf("completing analytics: %w", err)
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
	defer resp.Body.Close()
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
	defer resp.Body.Close()
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
func buildHactl() (string, error) {
	f, err := os.CreateTemp("", "hactl-e2e-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file for binary: %w", err)
	}
	binPath := f.Name()
	f.Close()

	slog.Info("companion-test: building hactl binary", "output", binPath)
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/hemm-ems/hactl/cmd/hactl")
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
