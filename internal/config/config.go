package config

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the resolved instance configuration.
type Config struct {
	Dir            string // resolved instance directory (absolute path)
	URL            string // HA_URL from .env (no trailing slash)
	Token          string // HA_TOKEN from .env
	TZ             string // optional timezone, defaults to ""
	CompanionURL   string // optional COMPANION_URL from .env (no trailing slash)
	CompanionToken string // optional COMPANION_TOKEN from .env; falls back to Token
}

// Load resolves the instance directory and loads .env.
// dirFlag is the value of --dir (may be empty).
// Returns a validated Config or an error with a clear user-facing message.
func Load(dirFlag string) (*Config, error) {
	dir, err := resolveDir(dirFlag)
	if err != nil {
		return nil, err
	}

	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot make path absolute: %w", err)
	}

	envPath := filepath.Join(dir, ".env")
	slog.Debug("loading .env", "path", envPath)

	env, err := parseEnvFile(envPath)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(env["HA_URL"], "/")
	token := env["HA_TOKEN"]
	tz := env["TZ"]
	companionURL := strings.TrimRight(env["COMPANION_URL"], "/")
	companionToken := env["COMPANION_TOKEN"]
	if companionToken == "" {
		companionToken = token // fall back to the HA token
	}

	if url == "" {
		return nil, fmt.Errorf("no HA_URL in .env at %s", envPath)
	}
	if token == "" {
		return nil, fmt.Errorf("no HA_TOKEN in .env at %s", envPath)
	}

	return &Config{
		Dir:            dir,
		URL:            url,
		Token:          token,
		TZ:             tz,
		CompanionURL:   companionURL,
		CompanionToken: companionToken,
	}, nil
}

// resolveDir determines the instance directory by checking candidates in order:
// 1. --dir flag, 2. HACTL_DIR env var, 3. cwd (if .env exists), 4. ~/.hactl/default/
func resolveDir(dirFlag string) (string, error) {
	if dirFlag != "" {
		slog.Debug("trying instance dir", "path", dirFlag, "source", "--dir flag")
		return dirFlag, nil
	}

	if envDir := os.Getenv("HACTL_DIR"); envDir != "" {
		slog.Debug("trying instance dir", "source", "HACTL_DIR")
		return envDir, nil
	}

	cwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		candidate := filepath.Join(cwd, ".env")
		slog.Debug("trying instance dir", "path", cwd, "source", "cwd")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return cwd, nil
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	defaultDir := filepath.Join(home, ".hactl", "default")
	slog.Debug("trying instance dir", "path", defaultDir, "source", "~/.hactl/default")
	return defaultDir, nil
}

// parseEnvFile reads a .env file and returns key-value pairs.
// It supports blank lines, # comments, and optional quoting of values.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no .env found at %s — run 'hactl setup' to create one", path)
		}
		return nil, fmt.Errorf("cannot open .env: %w", err)
	}
	defer func() { _ = f.Close() }()

	env := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = stripQuotes(value)
		env[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading .env at %s: %w", path, err)
	}
	return env, nil
}

// stripQuotes removes matching surrounding single or double quotes from a value.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
