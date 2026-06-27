package cmd

import (
	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/mcpserver"
)

var flagMCPAllowWrites bool

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run hactl as an MCP server (stdio)",
	Long: `Serve hactl over the Model Context Protocol on stdin/stdout, exposing the
CLI as a single 'hactl' tool for MCP clients (Claude Code, Claude Desktop, ...).

Read-only by default: mutating commands (svc call, auto apply, script apply, create/delete, ...)
are rejected unless started with --allow-writes. The server is pinned to one
instance per process; pass --dir to select it explicitly.

Example client registration:

  claude mcp add hactl -- hactl mcp --dir ~/.hactl/default`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Never write to cmd.OutOrStdout() here: Execute() echoes captured
		// command output to stdout afterwards, which would corrupt the
		// JSON-RPC stream the transport owns.
		return mcpserver.Run(cmd.Context(), mcpserver.Options{
			Runner:      RunWithOutputContext,
			ResolvePath: FindCommandPath,
			AllowWrites: flagMCPAllowWrites,
			Dir:         flagDir,
			Version:     version,
		})
	},
}

func init() {
	mcpCmd.Flags().BoolVar(&flagMCPAllowWrites, "allow-writes", false, "permit mutating commands over MCP")
	rootCmd.AddCommand(mcpCmd)
}
