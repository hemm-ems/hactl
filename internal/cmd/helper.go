package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/format"
)

var flagHelperDomain string
var flagHelperFile string
var flagHelperConfirm bool

var helperCmd = &cobra.Command{
	Use:   "helper",
	Short: "Manage HA helpers (input_boolean, counter, timer, etc.)",
	Long:  "List, create, and delete Home Assistant helper entities via the companion.",
}

var helperLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List helpers",
	Long:  "List all helpers, optionally filtered by domain.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var helperShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show helper details",
	Long:  "Show the YAML definition of a helper entity.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var helperCreateCmd = &cobra.Command{
	Use:   "create <domain>",
	Short: "Create a new helper (dry-run by default)",
	Long: `Create a new helper from a YAML file via the companion.
Supported domains: input_boolean, input_number, input_select, input_text,
input_datetime, counter, timer, schedule.
Use --confirm to apply.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperCreate(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var helperDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a helper (dry-run by default)",
	Long:  "Delete a helper entity via the companion. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	helperLsCmd.Flags().StringVar(&flagHelperDomain, "domain", "", "filter by domain (e.g. input_boolean)")
	helperCreateCmd.Flags().StringVarP(&flagHelperFile, "file", "f", "", "YAML file for the new helper")
	helperCreateCmd.Flags().BoolVar(&flagHelperConfirm, "confirm", false, "actually create (default is dry-run)")
	helperDeleteCmd.Flags().BoolVar(&flagHelperConfirm, "confirm", false, "actually delete (default is dry-run)")
	helperCmd.AddCommand(helperLsCmd, helperShowCmd, helperCreateCmd, helperDeleteCmd)
	rootCmd.AddCommand(helperCmd)
}

func runHelperLs(ctx context.Context, w io.Writer) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.ListHelpers(ctx, flagHelperDomain)
	if err != nil {
		return fmt.Errorf("listing helpers: %w", err)
	}

	if len(resp.Helpers) == 0 {
		_, _ = fmt.Fprintln(w, "no helpers")
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"id", "name", "domain", "icon"},
		Rows:    make([][]string, len(resp.Helpers)),
	}
	for i, h := range resp.Helpers {
		tbl.Rows[i] = []string{h.ID, h.Name, h.Domain, h.Icon}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func runHelperShow(ctx context.Context, w io.Writer, helperID string) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.GetHelper(ctx, helperID)
	if err != nil {
		return fmt.Errorf("fetching helper: %w", err)
	}

	_, _ = fmt.Fprintf(w, "id:     %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "domain: %s\n", resp.Domain)
	_, _ = fmt.Fprintf(w, "---\n%s", resp.Content)
	return nil
}

func runHelperCreate(ctx context.Context, w io.Writer, domain string) error {
	if flagHelperFile == "" {
		return errors.New("--file / -f is required for create")
	}

	data, err := os.ReadFile(flagHelperFile) //nolint:gosec // file path provided by user via CLI flag
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}
	content := string(data)

	if !flagHelperConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would create helper")
		_, _ = fmt.Fprintf(w, "  domain: %s\n", domain)
		_, _ = fmt.Fprintf(w, "  file:   %s\n", flagHelperFile)
		_, _ = fmt.Fprintf(w, "  size:   %d bytes\n", len(data))
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.CreateHelper(ctx, content, domain)
	if err != nil {
		return fmt.Errorf("creating helper: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created helper %q (domain=%s)\n", resp.ID, domain)
	return nil
}

func runHelperDelete(ctx context.Context, w io.Writer, helperID string) error {
	if !flagHelperConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete helper")
		_, _ = fmt.Fprintf(w, "  id: %s\n", helperID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	if _, err := cc.DeleteHelper(ctx, helperID); err != nil {
		return fmt.Errorf("deleting helper: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted helper %q\n", helperID)
	return nil
}
