//go:build integration

// Package hatest provides test helpers for spinning up a real Home Assistant
// container via testcontainers-go, automating headless onboarding, and
// returning a ready-to-use Instance with URL + long-lived token.
//
// Usage (one container per test package via TestMain):
//
//	var ha *hatest.Instance
//
//	func TestMain(m *testing.M) {
//	    var code int
//	    ha, code = hatest.StartMain(m, hatest.WithFixture("basic"))
//	    if code != 0 { os.Exit(code) }
//	    os.Exit(m.Run())
//	}
package hatest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	container "github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	defaultImage   = "ghcr.io/home-assistant/home-assistant:stable"
	defaultTimeout = 3 * time.Minute
	haPort         = "8123/tcp"

	onboardUser = "testowner"
	onboardPass = "testpass1234!" // test-only credentials
	onboardName = "Test Owner"
	clientID    = "http://hactl-test"
)

// Instance holds the running HA container and credentials.
type Instance struct {
	container testcontainers.Container
	url       string
	token     string
	dir       string // temp dir with .env
}

// URL returns the base URL of the running HA instance (http://localhost:<port>).
func (i *Instance) URL() string { return i.url }

// Token returns the long-lived access token.
func (i *Instance) Token() string { return i.token }

// Dir returns a temporary directory containing a .env file pointing to this instance.
// Suitable for use with hactl --dir.
func (i *Instance) Dir() string { return i.dir }

// Stop terminates the container and cleans up.
func (i *Instance) Stop() {
	if i.container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = i.container.Terminate(ctx)
	}
}

// Option configures the HA test container.
type Option func(*options)

type options struct {
	image   string
	fixture string
	timeout time.Duration
}

// WithImage overrides the default HA Docker image (default: stable).
func WithImage(image string) Option {
	return func(o *options) { o.image = image }
}

// WithFixture mounts testdata/fixtures/<name>/ as /config/ in the container.
func WithFixture(name string) Option {
	return func(o *options) { o.fixture = name }
}

// WithTimeout overrides the startup timeout (default: 3 min).
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// StartMain starts an HA container for use in TestMain.
// Returns the Instance and a non-zero exit code if setup fails.
// The caller should call os.Exit(m.Run()) after this.
func StartMain(m *testing.M, opts ...Option) (*Instance, int) {
	_ = m // unused but kept for symmetry with standard TestMain pattern

	inst, err := start(opts...)
	if err != nil {
		slog.Error("hatest: failed to start HA container", "error", err)
		return nil, 1
	}
	return inst, 0
}

// Start starts an HA container for use inside a single test.
// Registers cleanup via t.Cleanup.
func Start(t *testing.T, opts ...Option) *Instance {
	t.Helper()

	inst, err := start(opts...)
	if err != nil {
		t.Fatalf("hatest: failed to start HA container: %v", err)
	}
	t.Cleanup(inst.Stop)
	return inst
}

// StartShared starts an HA container that is NOT tied to a single test's cleanup.
// Use this when sharing a container across multiple tests via sync.Once.
// The caller is responsible for calling Stop() when done.
func StartShared(t *testing.T, opts ...Option) *Instance {
	t.Helper()

	inst, err := start(opts...)
	if err != nil {
		t.Fatalf("hatest: failed to start HA container: %v", err)
	}
	return inst
}

func start(opts ...Option) (*Instance, error) {
	o := &options{
		image:   defaultImage,
		timeout: defaultTimeout,
	}
	for _, fn := range opts {
		fn(o)
	}

	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()

	// Build container request
	req := testcontainers.ContainerRequest{
		Image:        o.image,
		ExposedPorts: []string{haPort},
		WaitingFor:   wait.ForHTTP("/api/onboarding").WithPort(haPort).WithStartupTimeout(o.timeout),
	}

	// Mount fixture directory if specified (copy to temp dir to avoid polluting originals)
	if o.fixture != "" {
		fixtureDir, err := resolveFixtureDir(o.fixture)
		if err != nil {
			return nil, fmt.Errorf("resolving fixture dir: %w", err)
		}
		tmpConfig, cpErr := copyFixtureToTemp(fixtureDir)
		if cpErr != nil {
			return nil, fmt.Errorf("copying fixture to temp dir: %w", cpErr)
		}
		req.HostConfigModifier = func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, tmpConfig+":/config")
		}
	}

	slog.Info("hatest: starting HA container", "image", o.image, "fixture", o.fixture)

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("starting container: %w", err)
	}

	// Resolve URL
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("getting container host: %w", err)
	}
	port, err := container.MappedPort(ctx, haPort)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("getting mapped port: %w", err)
	}
	baseURL := "http://" + net.JoinHostPort(host, port.Port())

	slog.Info("hatest: container ready", "url", baseURL)

	// Complete headless onboarding
	token, err := completeOnboarding(ctx, baseURL)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("onboarding: %w", err)
	}

	slog.Info("hatest: onboarding complete, token acquired")

	// Create temp dir with .env
	dir, err := createInstanceDir(baseURL, token)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("creating instance dir: %w", err)
	}

	return &Instance{
		container: container,
		url:       baseURL,
		token:     token,
		dir:       dir,
	}, nil
}

// completeOnboarding performs the full headless onboarding sequence:
// 1. POST /api/onboarding/users → auth_code
// 2. POST /auth/token → access_token + refresh_token
// 3. POST /api/onboarding/core_config (skip)
// 4. POST /api/onboarding/analytics (skip)
// 5. WebSocket auth/long_lived_access_token → long-lived token
func completeOnboarding(ctx context.Context, baseURL string) (string, error) {
	// Step 1: Create owner user
	authCode, err := createOwnerUser(ctx, baseURL)
	if err != nil {
		return "", fmt.Errorf("creating owner: %w", err)
	}

	// Step 2: Exchange auth code for tokens
	accessToken, err := exchangeAuthCode(ctx, baseURL, authCode)
	if err != nil {
		return "", fmt.Errorf("exchanging auth code: %w", err)
	}

	// Step 3: Complete core_config step
	if stepErr := completeStep(ctx, baseURL, accessToken, "/api/onboarding/core_config"); stepErr != nil {
		return "", fmt.Errorf("completing core_config: %w", stepErr)
	}

	// Step 4: Complete analytics step
	if stepErr := completeStep(ctx, baseURL, accessToken, "/api/onboarding/analytics"); stepErr != nil {
		return "", fmt.Errorf("completing analytics: %w", stepErr)
	}

	// Step 5: Create long-lived token via WebSocket
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
		return "", fmt.Errorf("empty auth_code in onboarding response: %s", string(data))
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
	defer resp.Body.Close() //nolint:errcheck // response body close
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

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil) //nolint:bodyclose // websocket manages connection
	if err != nil {
		return "", fmt.Errorf("ws connect: %w", err)
	}
	defer conn.Close() //nolint:errcheck // websocket close

	// Read auth_required
	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		return "", fmt.Errorf("reading auth_required: %w", err)
	}

	// Send auth
	if err := conn.WriteJSON(map[string]string{
		"type":         "auth",
		"access_token": accessToken,
	}); err != nil {
		return "", fmt.Errorf("sending auth: %w", err)
	}

	// Read auth_ok
	if err := conn.ReadJSON(&msg); err != nil {
		return "", fmt.Errorf("reading auth_ok: %w", err)
	}
	if msg["type"] != "auth_ok" {
		return "", fmt.Errorf("expected auth_ok, got: %v", msg["type"])
	}

	// Create long-lived token
	if err := conn.WriteJSON(map[string]any{
		"id":          1,
		"type":        "auth/long_lived_access_token",
		"client_name": "hactl-e2e",
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

func doJSONPost(ctx context.Context, url, token string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
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
	defer resp.Body.Close() //nolint:errcheck // response body close
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func resolveFixtureDir(name string) (string, error) {
	// Try relative to working directory first (standard Go test pattern)
	candidates := []string{
		filepath.Join("testdata", "fixtures", name),
		filepath.Join("..", "..", "testdata", "fixtures", name),
		filepath.Join("..", "..", "..", "testdata", "fixtures", name),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
			return abs, nil
		}
	}
	return "", fmt.Errorf("fixture %q not found in any candidate path", name)
}

func createInstanceDir(baseURL, token string) (string, error) {
	dir, err := os.MkdirTemp("", "hatest-*")
	if err != nil {
		return "", err
	}
	// Create .env
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=%s\n", baseURL, token)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		return "", err
	}
	// Create cache dir
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o750); err != nil {
		return "", err
	}
	return dir, nil
}

// copyFixtureToTemp copies a fixture directory to a temporary directory.
// This prevents HA from writing .storage/ etc. into the original fixtures.
func copyFixtureToTemp(srcDir string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "hatest-config-*")
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", fmt.Errorf("reading fixture dir: %w", err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(tmpDir, entry.Name())

		if entry.IsDir() {
			continue // skip subdirectories in fixtures
		}

		data, readErr := os.ReadFile(srcPath) //nolint:gosec // fixture files from testdata
		if readErr != nil {
			return "", fmt.Errorf("reading %s: %w", entry.Name(), readErr)
		}
		if writeErr := os.WriteFile(dstPath, data, 0o600); writeErr != nil { //nolint:gosec // dstPath is constructed from a temp dir + fixture filename
			return "", fmt.Errorf("writing %s: %w", entry.Name(), writeErr)
		}
	}

	return tmpDir, nil
}
