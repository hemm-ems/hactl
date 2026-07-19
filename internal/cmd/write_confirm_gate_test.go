package cmd

import (
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/mcpserver"
)

// TestWriteCommandsHaveConfirmFlag is the durable guard for issue #67: every
// command classified as a write in internal/mcpserver/gate.go must resolve to a
// cobra command that defines a --confirm flag. That flag is the hinge the whole
// safety story hangs on — confirmGuard (internal/cmd/inject.go) bails out when
// cmd.Flags().Lookup("confirm") == nil, so a write missing the flag skips BOTH
// the dry-run gate AND the first-of-family refusal and executes immediately,
// breaking the "every write command is dry-run by default" promise the manual
// injects. Walking the live writeCommands table (rather than a hand-listed set)
// keeps this exhaustive: a future write added without a confirm flag fails here,
// named — reclassify it in gate.go if it is genuinely not a write.
func TestWriteCommandsHaveConfirmFlag(t *testing.T) {
	writes := mcpserver.ClassifiedPaths()["write"]
	if len(writes) == 0 {
		t.Fatal("no write commands classified in internal/mcpserver/gate.go; table changed shape")
	}
	for _, path := range writes {
		t.Run(path, func(t *testing.T) {
			args := strings.Fields(path)[1:] // drop the leading "hactl"
			c, _, err := rootCmd.Find(args)
			if err != nil {
				t.Fatalf("write command %q does not resolve to a cobra command: %v", path, err)
			}
			// Use the same lookup confirmGuard uses, so the test reflects the
			// exact condition production code checks.
			if c.Flags().Lookup("confirm") == nil {
				t.Errorf("write command %q defines no --confirm flag: it would execute immediately with no dry-run preview, and confirmGuard cannot refuse it (issue #67). Register a --confirm flag and gate the run behind it, or reclassify the command in internal/mcpserver/gate.go if it is not a write.", path)
			}
		})
	}
}
