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
)

const (
	companionToken = "integration-test-token-12345"
	clientID       = "http://hactl-companion-test"
	onboardUser    = "testowner"
	onboardPass    = "testpass1234!"
	onboardName    = "Test Owner"
)

var (
	testClient  *companion.Client
	haURL       string
	compURL     string
	composeDir  string
	haToken     string // long-lived HA token for E2E tests
	instanceDir string // temp dir with .env for hactl CLI E2E tests
	hactlBin    string // path to built hactl binary
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

	// Start stack
	if err := composeUp(); err != nil {
		slog.Error("companion-test: compose up failed", "error", err)
		os.Exit(1)
	}

	// Resolve mapped ports
	var err error
	haURL, err = getMappedURL("homeassistant", "8123")
	if err != nil {
		slog.Error("companion-test: get HA port", "error", err)
		composeDown()
		os.Exit(1)
	}
	compURL, err = getMappedURL("companion", "9100")
	if err != nil {
		slog.Error("companion-test: get companion port", "error", err)
		composeDown()
		os.Exit(1)
	}

	slog.Info("companion-test: stack URLs", "ha", haURL, "companion", compURL)

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
	slog.Info("companion-test: onboarding complete")

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

func composeUp() error {
	cmd := exec.Command("docker", "compose", "-f", filepath.Join(composeDir, "docker-compose.yaml"), "up", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

	return nil
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
