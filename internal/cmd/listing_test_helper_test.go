package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
)

// listingCmd returns the live tree's command at `path`, carrying ctx — what a
// listing entrypoint takes now that its empty answer is composed from the
// narrowing flags the caller supplied (see listing.go).
//
// Tests hand it the real command rather than a fresh cobra.Command so that the
// flag set the message reads is the same object the flag variables are bound
// to. A stand-in would let a test prove a message about flags the command does
// not have.
func listingCmd(ctx context.Context, path ...string) *cobra.Command {
	c, _, err := rootCmd.Find(path)
	if err != nil || c == rootCmd {
		panic("no command at path " + strings.Join(path, " "))
	}
	c.SetContext(ctx)
	return c
}
