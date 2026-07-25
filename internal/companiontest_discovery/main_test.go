//go:build companion_discovery

package companiontest_discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/companiontestutil"
)

var (
	composeDir   string
	companionURL string
	fakeSup      *fakeSupervisor
)

// TestMain delegates to runTestMain so the deferred cancel below actually runs:
// calling os.Exit from inside the setup sequence skips every pending defer
// (gocritic exitAfterDefer).
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	// Every docker subprocess below runs under this context so a future
	// deadline can abort a stack that never comes up.
	rootCtx := context.Background()

	composeDir = resolveComposeDir()
	slog.Info("discovery-test: starting stack", "dir", composeDir)

	if err := buildCompanion(rootCtx); err != nil {
		slog.Error("discovery-test: build companion failed", "error", err)
		return 1
	}
	if err := composeUp(rootCtx); err != nil {
		slog.Error("discovery-test: compose up failed", "error", err)
		return 1
	}

	var err error
	companionURL, err = mappedURL(rootCtx, "companion", "9100")
	if err != nil {
		slog.Error("discovery-test: get companion port", "error", err)
		composeDown(rootCtx)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if readyErr := waitForURL(ctx, companionURL+"/v1/health"); readyErr != nil {
		slog.Error("discovery-test: companion not ready", "error", readyErr)
		composeDown(rootCtx)
		return 1
	}
	slog.Info("discovery-test: companion ready", "url", companionURL)

	if seedErr := companiontestutil.SeedRelatedFixture(rootCtx, filepath.Join(composeDir, "docker-compose.yaml"), "companion"); seedErr != nil {
		slog.Error("discovery-test: seed related fixture", "error", seedErr)
		composeDown(rootCtx)
		return 1
	}
	slog.Info("discovery-test: related fixture seeded")

	fakeSup, err = startFakeSupervisor(rootCtx, companionURL)
	if err != nil {
		slog.Error("discovery-test: start fake supervisor", "error", err)
		composeDown(rootCtx)
		return 1
	}
	slog.Info("discovery-test: fake supervisor ready", "url", fakeSup.BaseURL())

	code := m.Run()

	if shutdownErr := fakeSup.Shutdown(); shutdownErr != nil {
		slog.Warn("discovery-test: fake supervisor shutdown", "error", shutdownErr)
	}
	composeDown(rootCtx)
	return code
}

func resolveComposeDir() string {
	candidates := []string{
		".",
		filepath.Join("..", "companiontest_discovery"),
		filepath.Join("..", "..", "internal", "companiontest_discovery"),
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
	abs, _ := filepath.Abs(".")
	return abs
}

func buildCompanion(ctx context.Context) error {
	slog.Info("discovery-test: building companion image")
	//nolint:gosec // G204: fixed docker CLI, arguments are test-owned paths and service names
	cmd := exec.CommandContext(ctx, "docker", "compose",
		"-f", filepath.Join(composeDir, "docker-compose.yaml"),
		"build", "companion")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose build companion: %w", err)
	}
	return nil
}

func composeUp(ctx context.Context) error {
	//nolint:gosec // G204: fixed docker CLI, arguments are test-owned paths and service names
	cmd := exec.CommandContext(ctx, "docker", "compose",
		"-f", filepath.Join(composeDir, "docker-compose.yaml"),
		"up", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func composeDown(ctx context.Context) {
	//nolint:gosec // G204: fixed docker CLI, arguments are test-owned paths and service names
	cmd := exec.CommandContext(ctx, "docker", "compose",
		"-f", filepath.Join(composeDir, "docker-compose.yaml"),
		"down", "-v")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func mappedURL(ctx context.Context, service, port string) (string, error) {
	//nolint:gosec // G204: fixed docker CLI, arguments are test-owned paths and service names
	cmd := exec.CommandContext(ctx, "docker", "compose",
		"-f", filepath.Join(composeDir, "docker-compose.yaml"),
		"port", service, port)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("get port for %s:%s: %w", service, port, err)
	}
	hostPort := strings.TrimSpace(string(out))
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
		time.Sleep(500 * time.Millisecond)
	}
}
