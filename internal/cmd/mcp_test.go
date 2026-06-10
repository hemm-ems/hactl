package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/mcpserver"
)

// TestMCPGateExhaustive keeps the MCP gate and the command tree in lockstep:
// every runnable command must be classified (a new command added without a
// gate entry fails here, not at runtime), and every gate entry must still
// exist (catches renames and removals).
func TestMCPGateExhaustive(t *testing.T) {
	leaves := map[string]bool{}
	for _, p := range LeafCommandPaths() {
		leaves[p] = true
	}

	classified := map[string]string{}
	for set, paths := range mcpserver.ClassifiedPaths() {
		for _, p := range paths {
			classified[p] = set
		}
	}

	for p := range leaves {
		// Cobra adds completion subcommands lazily on Execute; the gate
		// blocks the whole subtree by prefix.
		if strings.HasPrefix(p, "hactl completion ") {
			continue
		}
		set, ok := classified[p]
		if !ok {
			t.Errorf("command %q is not classified in internal/mcpserver/gate.go; add it to readCommands, writeCommands, or alwaysBlocked", p)
			continue
		}
		if set == "group" {
			t.Errorf("runnable command %q is classified as a group; move it to readCommands, writeCommands, or alwaysBlocked", p)
		}
	}

	for p, set := range classified {
		if leaves[p] {
			continue
		}
		// Groups and cobra's lazily-added builtins (help, completion)
		// are not leaves; verify they at least resolve to a command.
		if set == "group" || p == "hactl help" || p == "hactl completion" {
			args := strings.Fields(p)[1:] // drop "hactl"
			if _, err := FindCommandPath(args); err != nil && set == "group" {
				t.Errorf("gate classifies %q (%s) but no such command exists", p, set)
			}
			continue
		}
		t.Errorf("gate classifies %q (%s) but no such command exists; remove the stale entry", p, set)
	}
}

// TestMCPFindCommandPathSkipsFlags ensures flags interleaved before the
// subcommand do not break resolution (models often put --dir first).
func TestMCPFindCommandPathSkipsFlags(t *testing.T) {
	path, err := FindCommandPath([]string{"--dir", "/tmp/x", "ent", "ls", "--domain", "light"})
	if err != nil {
		t.Fatalf("FindCommandPath: %v", err)
	}
	if path != "hactl ent ls" {
		t.Errorf("path = %q, want %q", path, "hactl ent ls")
	}
}

// TestRunWithOutputContextNoLeak guards against cobra's context caching:
// cobra only assigns the root context to a subcommand when the subcommand's
// ctx is nil, so without an explicit reset the second invocation of a command
// runs with the first invocation's (long cancelled) context. Over MCP that
// cancelled every HA request of a repeated tool call.
func TestRunWithOutputContextNoLeak(t *testing.T) {
	probe := &cobra.Command{
		Use:  "ctxprobe",
		RunE: func(c *cobra.Command, _ []string) error { return c.Context().Err() },
	}
	rootCmd.AddCommand(probe)
	defer rootCmd.RemoveCommand(probe)

	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer
	if err := RunWithOutputContext(ctx, []string{"hactl", "ctxprobe"}, &buf); err != nil {
		t.Fatalf("first run: %v", err)
	}
	cancel()
	if err := RunWithOutputContext(context.Background(), []string{"hactl", "ctxprobe"}, &buf); err != nil {
		t.Errorf("cancelled context leaked into the next invocation: %v", err)
	}
}

// TestMCPRunnerIntegration drives RunWithOutput exactly as the MCP tool
// handler does, without needing a Home Assistant instance.
func TestMCPRunnerIntegration(t *testing.T) {
	t.Run("version", func(t *testing.T) {
		var buf bytes.Buffer
		if err := RunWithOutput([]string{"hactl", "version"}, &buf); err != nil {
			t.Fatalf("version: %v", err)
		}
		if !strings.Contains(buf.String(), "hactl") {
			t.Errorf("version output = %q", buf.String())
		}
	})

	t.Run("rtfm", func(t *testing.T) {
		var buf bytes.Buffer
		if err := RunWithOutput([]string{"hactl", "rtfm"}, &buf); err != nil {
			t.Fatalf("rtfm: %v", err)
		}
		if buf.Len() < 1000 {
			t.Errorf("rtfm output suspiciously short: %d bytes", buf.Len())
		}
	})

	t.Run("missing config surfaces error", func(t *testing.T) {
		var buf bytes.Buffer
		err := RunWithOutput([]string{"hactl", "--dir", t.TempDir(), "ent", "ls"}, &buf)
		if err == nil {
			t.Fatal("expected error for missing config")
		}
	})
}
