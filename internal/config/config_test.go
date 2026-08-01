package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, dir, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

const testHAURL = "http://ha:8123"

func TestLoad_DirFlag(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "HA_URL="+testHAURL+"\nHA_TOKEN=secret123\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.URL != testHAURL {
		t.Errorf("URL = %q, want %q", cfg.URL, testHAURL)
	}
	if cfg.Token != "secret123" {
		t.Errorf("Token = %q, want %q", cfg.Token, "secret123")
	}
	absDir, _ := filepath.Abs(dir)
	if cfg.Dir != absDir {
		t.Errorf("Dir = %q, want %q", cfg.Dir, absDir)
	}
}

func TestLoad_EnvVar(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "HA_URL="+testHAURL+"\nHA_TOKEN=fromenv\n")

	t.Setenv("HACTL_DIR", dir)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.URL != testHAURL {
		t.Errorf("URL = %q, want %q", cfg.URL, testHAURL)
	}
	if cfg.Token != "fromenv" {
		t.Errorf("Token = %q, want %q", cfg.Token, "fromenv")
	}
}

func TestLoad_CWD(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "HA_URL="+testHAURL+"\nHA_TOKEN=cwdtoken\n")

	t.Setenv("HACTL_DIR", "") // ensure env var is not set
	t.Chdir(dir)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	absDir, _ := filepath.Abs(dir)
	if cfg.Dir != absDir {
		t.Errorf("Dir = %q, want %q", cfg.Dir, absDir)
	}
	if cfg.Token != "cwdtoken" {
		t.Errorf("Token = %q, want %q", cfg.Token, "cwdtoken")
	}
}

func TestLoad_ParentDir(t *testing.T) {
	// .env with HA_URL in a parent of cwd is discovered (git-style walk).
	parent := t.TempDir()
	writeEnv(t, parent, "HA_URL="+testHAURL+"\nHA_TOKEN=parenttoken\n")
	sub := filepath.Join(parent, "backups", "deep")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HACTL_DIR", "")
	t.Chdir(sub)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Token != "parenttoken" {
		t.Errorf("Token = %q, want %q", cfg.Token, "parenttoken")
	}
}

func TestLoad_ParentDirWithoutHAURL_Skipped(t *testing.T) {
	// A parent .env without HA_URL (e.g. an unrelated project .env) is
	// skipped, and discovery falls through to ~/.hactl/default.
	parent := t.TempDir()
	writeEnv(t, parent, "NODE_ENV=production\n")
	sub := filepath.Join(parent, "src")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	defaultDir := filepath.Join(home, ".hactl", "default")
	if err := os.MkdirAll(defaultDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeEnv(t, defaultDir, "HA_URL="+testHAURL+"\nHA_TOKEN=defaulttoken\n")

	t.Setenv("HOME", home)
	t.Setenv("HACTL_DIR", "")
	t.Chdir(sub)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Token != "defaulttoken" {
		t.Errorf("Token = %q, want %q", cfg.Token, "defaulttoken")
	}
}

func TestLoad_FallbackNoEnv(t *testing.T) {
	// CWD with no .env, no HACTL_DIR — falls back to ~/.hactl/default/ which doesn't exist
	dir := t.TempDir()            // empty dir, no .env
	t.Setenv("HOME", t.TempDir()) // isolate from a real ~/.hactl/default
	t.Setenv("HACTL_DIR", "")
	t.Chdir(dir)

	_, err := Load("")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "hactl setup") {
		t.Errorf("error = %q, want it to contain 'hactl setup'", err.Error())
	}
}

func TestLoad_MissingEnv_HasDiscoveryOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // isolate from a real ~/.hactl/default
	t.Setenv("HACTL_DIR", "")
	t.Chdir(dir)

	_, err := Load("")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"no hactl instance configured",
		"--dir",
		"HACTL_DIR",
		"current directory",
		"~/.hactl/default",
		"hactl setup",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q\nfull message: %s", want, msg)
		}
	}
}

// TestLoad_ExplicitDirNamesThePathItTried is findings #55/#77: an explicit
// --dir or $HACTL_DIR that does not resolve printed the same generic four-step
// discovery block as passing nothing at all, so the one message that knew a
// path had been typed was the one that would not repeat it back.
//
// The oracle for "what should it look like" is the sibling error one branch
// down: a .env that exists but is missing HA_TOKEN answers `no HA_TOKEN in
// .env at <path>`. The last subtest pins that sibling here, because the whole
// finding is a divergence between two errors from the same function and a test
// that watches only one of them cannot see a divergence at all.
func TestLoad_ExplicitDirNamesThePathItTried(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-an-instance")

	for _, tc := range []struct {
		name       string
		wantSource string
		load       func(t *testing.T) error
	}{
		{
			name:       "--dir",
			wantSource: "--dir",
			load: func(t *testing.T) error {
				t.Helper()
				t.Setenv("HACTL_DIR", "")
				_, err := Load(missing)
				return err
			},
		},
		{
			name:       "HACTL_DIR",
			wantSource: "$HACTL_DIR",
			load: func(t *testing.T) error {
				t.Helper()
				t.Setenv("HACTL_DIR", missing)
				_, err := Load("")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.load(t)
			if err == nil {
				t.Fatal("expected an error for a directory with no .env")
			}
			msg := err.Error()
			if !strings.Contains(msg, missing) {
				t.Errorf("error does not name the path the caller gave (%s):\n%s", missing, msg)
			}
			if !strings.Contains(msg, tc.wantSource) {
				t.Errorf("error does not say the path came from %s:\n%s", tc.wantSource, msg)
			}
			// The four-step block still follows — `hactl setup` is offered there
			// and a typo'd --dir is exactly when it is wanted.
			if !strings.Contains(msg, "hactl setup") {
				t.Errorf("error dropped the quick-start hint:\n%s", msg)
			}
			// Exit code 2 is the contract for a configuration error and must
			// survive the rewrite.
			type exiter interface{ ExitCode() int }
			var ec exiter
			if !errors.As(err, &ec) || ec.ExitCode() != 2 {
				t.Errorf("explicit-dir failure lost its exit code 2: %T", err)
			}
		})
	}

	// Discovery names nothing, because the caller named nothing.
	t.Run("discovery keeps the generic headline", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("HACTL_DIR", "")
		t.Chdir(t.TempDir())
		_, err := Load("")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "no hactl instance configured") {
			t.Errorf("discovery failure should keep the generic headline:\n%s", err.Error())
		}
	})

	// The sibling this was harmonised toward.
	t.Run("an incomplete .env already named its path", func(t *testing.T) {
		dir := t.TempDir()
		writeEnv(t, dir, "HA_URL="+testHAURL+"\n")
		_, err := Load(dir)
		if err == nil {
			t.Fatal("expected an error for a .env with no HA_TOKEN")
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("the sibling error stopped naming its path — the two have diverged again:\n%s", err.Error())
		}
	})
}

func TestLoad_MissingEnv_ExitCode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // isolate from a real ~/.hactl/default
	t.Setenv("HACTL_DIR", "")
	t.Chdir(dir)

	_, err := Load("")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	type exiter interface{ ExitCode() int }
	var ec exiter
	if !errors.As(err, &ec) {
		t.Fatalf("expected ConfigNotFoundError implementing ExitCode(), got %T", err)
	}
	if ec.ExitCode() != 2 {
		t.Errorf("ExitCode() = %d, want 2", ec.ExitCode())
	}
}

func TestLoad_MissingURL(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "HA_TOKEN=tokenonly\n")

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no HA_URL") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "no HA_URL")
	}
}

func TestLoad_MissingToken(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "HA_URL="+testHAURL+"\n")

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no HA_TOKEN") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "no HA_TOKEN")
	}
}

func TestLoad_QuotedValues(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "HA_URL=\"http://ha:8123\"\nHA_TOKEN='mytoken'\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.URL != testHAURL {
		t.Errorf("URL = %q, want %q", cfg.URL, testHAURL)
	}
	if cfg.Token != "mytoken" {
		t.Errorf("Token = %q, want %q", cfg.Token, "mytoken")
	}
}

func TestLoad_TrailingSlash(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "HA_URL=http://ha:8123/\nHA_TOKEN=tok\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.URL != testHAURL {
		t.Errorf("URL = %q, want %q (trailing slash should be stripped)", cfg.URL, testHAURL)
	}
}

func TestLoad_CompanionToken_Explicit(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, "HA_URL="+testHAURL+"\nHA_TOKEN=hatoken\nCOMPANION_TOKEN=companiontoken\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Token != "hatoken" {
		t.Errorf("Token = %q, want hatoken", cfg.Token)
	}
	if cfg.CompanionToken != "companiontoken" {
		t.Errorf("CompanionToken = %q, want companiontoken", cfg.CompanionToken)
	}
}

func TestLoad_CompanionToken_FallsBackToToken(t *testing.T) {
	dir := t.TempDir()
	// No COMPANION_TOKEN — should fall back to HA_TOKEN
	writeEnv(t, dir, "HA_URL="+testHAURL+"\nHA_TOKEN=hatoken\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CompanionToken != cfg.Token {
		t.Errorf("CompanionToken = %q, want it to fall back to Token %q", cfg.CompanionToken, cfg.Token)
	}
}
