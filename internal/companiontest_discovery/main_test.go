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

const companionToken = "integration-test-token-discovery"

var (
	composeDir   string
	companionURL string
	fakeSup      *fakeSupervisor
)

func TestMain(m *testing.M) {
	composeDir = resolveComposeDir()
	slog.Info("discovery-test: starting stack", "dir", composeDir)

	if err := buildCompanion(); err != nil {
		slog.Error("discovery-test: build companion failed", "error", err)
		os.Exit(1)
	}
	if err := composeUp(); err != nil {
		slog.Error("discovery-test: compose up failed", "error", err)
		os.Exit(1)
	}

	var err error
	companionURL, err = mappedURL("companion", "9100")
	if err != nil {
		slog.Error("discovery-test: get companion port", "error", err)
		composeDown()
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := waitForURL(ctx, companionURL+"/v1/health"); err != nil {
		slog.Error("discovery-test: companion not ready", "error", err)
		composeDown()
		os.Exit(1)
	}
	slog.Info("discovery-test: companion ready", "url", companionURL)

	if err := companiontestutil.SeedRelatedFixture(filepath.Join(composeDir, "docker-compose.yaml"), "companion"); err != nil {
		slog.Error("discovery-test: seed related fixture", "error", err)
		composeDown()
		os.Exit(1)
	}
	slog.Info("discovery-test: related fixture seeded")

	fakeSup, err = startFakeSupervisor(companionURL)
	if err != nil {
		slog.Error("discovery-test: start fake supervisor", "error", err)
		composeDown()
		os.Exit(1)
	}
	slog.Info("discovery-test: fake supervisor ready", "url", fakeSup.BaseURL())

	code := m.Run()

	if shutdownErr := fakeSup.Shutdown(); shutdownErr != nil {
		slog.Warn("discovery-test: fake supervisor shutdown", "error", shutdownErr)
	}
	composeDown()
	os.Exit(code)
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

func buildCompanion() error {
	slog.Info("discovery-test: building companion image")
	cmd := exec.Command("docker", "compose",
		"-f", filepath.Join(composeDir, "docker-compose.yaml"),
		"build", "companion")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose build companion: %w", err)
	}
	return nil
}

func composeUp() error {
	cmd := exec.Command("docker", "compose",
		"-f", filepath.Join(composeDir, "docker-compose.yaml"),
		"up", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func composeDown() {
	cmd := exec.Command("docker", "compose",
		"-f", filepath.Join(composeDir, "docker-compose.yaml"),
		"down", "-v")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func mappedURL(service, port string) (string, error) {
	cmd := exec.Command("docker", "compose",
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
		resp, err := http.Get(targetURL) //nolint:gosec // test URL
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}
