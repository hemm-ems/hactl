package config

import (
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

func TestLoad_FallbackNoEnv(t *testing.T) {
	// CWD with no .env, no HACTL_DIR — falls back to ~/.hactl/default/ which doesn't exist
	dir := t.TempDir() // empty dir, no .env
	t.Setenv("HACTL_DIR", "")
	t.Chdir(dir)

	_, err := Load("")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot open .env") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "cannot open .env")
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
