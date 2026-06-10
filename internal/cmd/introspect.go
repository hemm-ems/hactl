package cmd

import (
	"github.com/spf13/cobra"
)

// FindCommandPath resolves raw CLI args (without the binary name) to the
// canonical command path, e.g. "hactl ent set-label". Flags may appear
// anywhere in args; cobra's Find skips them using the registered flag sets.
func FindCommandPath(args []string) (string, error) {
	c, _, err := rootCmd.Find(args)
	if err != nil {
		return "", err
	}
	return c.CommandPath(), nil
}

// LeafCommandPaths returns the canonical path of every runnable leaf command,
// e.g. "hactl ent ls". Group commands that only hold subcommands are omitted;
// groups with their own Run (none today) would be included.
func LeafCommandPaths() []string {
	var paths []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		children := c.Commands()
		if len(children) == 0 || c.Runnable() {
			paths = append(paths, c.CommandPath())
		}
		for _, child := range children {
			walk(child)
		}
	}
	walk(rootCmd)
	return paths
}
