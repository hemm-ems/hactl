package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestVersionFlagAgreesWithVersionCommand — `hactl --version` answers, and its
// first line is the same one `hactl version` prints, so the two surfaces can
// never drift apart. The flag form is what the Homebrew formula's `test do`
// runs (homebrew-tap/hactl.rb, mirrored from .goreleaser.yaml); until issue
// #106 it was an unknown flag, so `brew test hactl` failed on every release.
func TestVersionFlagAgreesWithVersionCommand(t *testing.T) {
	var flagOut bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "--version"}, &flagOut); err != nil {
		t.Fatalf("hactl --version: %v", err)
	}

	var cmdOut bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "version"}, &cmdOut); err != nil {
		t.Fatalf("hactl version: %v", err)
	}

	flagLine, _, _ := strings.Cut(flagOut.String(), "\n")
	cmdLine, _, _ := strings.Cut(cmdOut.String(), "\n")
	if flagLine == "" || flagLine != cmdLine {
		t.Errorf("--version prints %q, version prints %q — the flag and the subcommand must answer with the same first line", flagLine, cmdLine)
	}
	if !strings.HasPrefix(flagLine, "hactl ") {
		t.Errorf("--version output %q does not start with %q", flagLine, "hactl ")
	}
}
