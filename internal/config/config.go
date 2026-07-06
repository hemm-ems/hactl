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

// resolvedDir records the instance directory of the last Load call so error
// paths can tell the user which instance a failing command was talking to.
var resolvedDir string

// ResolvedDir returns the instance directory resolved by the last Load call,
// or "" if no directory has been resolved yet.
func ResolvedDir() string { return resolvedDir }

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
	resolvedDir = dir

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

// BestEffortDir resolves the instance directory with the same candidate order
// as Load but without requiring a .env — for callers that only need a state
// location (e.g. manual-delivery session tracking) even when the command
// never loaded config. Returns "" when resolution fails entirely.
func BestEffortDir(dirFlag string) string {
	dir, err := resolveDir(dirFlag)
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return abs
}

// resolveDir determines the instance directory by checking candidates in order:
// 1. --dir flag, 2. HACTL_DIR env var, 3. cwd and its parents, 4. ~/.hactl/default/
//
// A .env in the cwd itself always wins, even if incomplete — a broken instance
// dir should error loudly rather than silently fall through to another instance.
// Parent directories are only accepted when their .env actually configures
// hactl (contains HA_URL), so unrelated project .env files are skipped.
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
		slog.Debug("trying instance dir", "path", cwd, "source", "cwd")
		if _, statErr := os.Stat(filepath.Join(cwd, ".env")); statErr == nil {
			return cwd, nil
		}
		for dir := filepath.Dir(cwd); ; dir = filepath.Dir(dir) {
			slog.Debug("trying instance dir", "path", dir, "source", "parent")
			if envFileHasHAURL(filepath.Join(dir, ".env")) {
				return dir, nil
			}
			if dir == filepath.Dir(dir) { // reached filesystem root
				break
			}
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

// envFileHasHAURL reports whether path is a readable .env file that contains
// a non-empty HA_URL — i.e. one that plausibly configures a hactl instance.
func envFileHasHAURL(path string) bool {
	env, err := parseEnvFile(path)
	return err == nil && env["HA_URL"] != ""
}

// ConfigNotFoundError is returned when no .env file can be located.
// It carries exit code 2 ("configuration error") so callers can distinguish it
// from generic runtime errors.
type ConfigNotFoundError struct { //nolint:revive // stutter is intentional: config.ConfigNotFoundError is unambiguous
	msg string
}

func (e *ConfigNotFoundError) Error() string { return e.msg }

// ExitCode returns 2 to signal a configuration error rather than a generic program error.
func (e *ConfigNotFoundError) ExitCode() int { return 2 }

const noConfigMsg = "no hactl instance configured\n\n" +
	"hactl looks for a .env in this order:\n" +
	"  1. --dir <path>\n" +
	"  2. $HACTL_DIR\n" +
	"  3. the current directory (and parents)\n" +
	"  4. ~/.hactl/default\n\n" +
	"Quick start:  hactl setup"

// parseEnvFile reads a .env file and returns key-value pairs.
// It supports blank lines, # comments, and optional quoting of values.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &ConfigNotFoundError{msg: noConfigMsg}
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
