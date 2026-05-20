package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReadmeCommandsExist parses fenced code blocks in README.md for lines of the
// form "hactl <subcommand>" and asserts every referenced top-level subcommand
// exists in the built command tree. This catches README/binary drift early.
func TestReadmeCommandsExist(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine source file path")
	}
	// Navigate from internal/cmd/ up to the hactl module root (two levels up).
	moduleRoot := filepath.Join(filepath.Dir(file), "..", "..")
	readmePath := filepath.Join(moduleRoot, "README.md")

	f, err := os.Open(filepath.Clean(readmePath))
	if err != nil {
		t.Fatalf("cannot open README.md at %s: %v", readmePath, err)
	}
	defer func() { _ = f.Close() }()

	seen := map[string]bool{}
	inFence := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		// Look for lines like "hactl <subcommand> ..."
		if !strings.HasPrefix(trimmed, "hactl ") {
			continue
		}
		parts := strings.Fields(trimmed)
		if len(parts) < 2 {
			continue
		}
		sub := parts[1]
		if strings.HasPrefix(sub, "#") || strings.HasPrefix(sub, "-") || strings.HasPrefix(sub, "|") {
			continue
		}
		seen[sub] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning README.md: %v", err)
	}

	for sub := range seen {
		cmd, _, findErr := rootCmd.Find([]string{sub})
		if findErr != nil || cmd == nil || cmd.Name() != sub {
			t.Errorf("README references 'hactl %s' but command is not registered (cmd=%v err=%v)", sub, cmd, findErr)
		}
	}
}
