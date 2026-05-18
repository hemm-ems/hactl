package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var flagTplFile string
var flagTplConfirm bool
var flagTplDomain string

var tplCmd = &cobra.Command{
	Use:   "tpl",
	Short: "Manage templates (eval, create, delete)",
	Long:  "Evaluate Jinja2 templates and manage template sensor definitions.",
}

var tplEvalCmd = &cobra.Command{
	Use:   "eval [template]",
	Short: "Evaluate a template",
	Long:  "Evaluate an inline template string or a template from a file (-f).",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTplEval(cmd.Context(), cmd.OutOrStdout(), args)
	},
}

func init() {
	tplEvalCmd.Flags().StringVarP(&flagTplFile, "file", "f", "", "read template from file")
	tplCreateCmd.Flags().StringVarP(&flagTplFile, "file", "f", "", "YAML file for the new template sensor")
	tplCreateCmd.Flags().StringVar(&flagTplDomain, "domain", "sensor", "template domain (sensor or binary_sensor)")
	tplCreateCmd.Flags().BoolVar(&flagTplConfirm, "confirm", false, "actually create (default is dry-run)")
	tplDeleteCmd.Flags().BoolVar(&flagTplConfirm, "confirm", false, "actually delete (default is dry-run)")
	tplCmd.AddCommand(tplEvalCmd, tplCreateCmd, tplDeleteCmd)
	rootCmd.AddCommand(tplCmd)
}

var tplCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new template sensor (dry-run by default)",
	Long:  "Create a new template sensor from a YAML file via the companion. Use --confirm to apply.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTplCreate(cmd.Context(), cmd.OutOrStdout())
	},
}

var tplDeleteCmd = &cobra.Command{
	Use:   "delete <unique_id>",
	Short: "Delete a template sensor (dry-run by default)",
	Long:  "Delete a template sensor from HA via the companion. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTplDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func runTplEval(ctx context.Context, w io.Writer, args []string) error {
	tpl, err := resolveTemplate(args)
	if err != nil {
		return err
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	result, err := client.RenderTemplate(ctx, tpl)
	if err != nil {
		return fmt.Errorf("rendering template: %w", err)
	}

	_, _ = fmt.Fprintln(w, result)
	return nil
}

func resolveTemplate(args []string) (string, error) {
	if flagTplFile != "" {
		data, err := os.ReadFile(flagTplFile) //nolint:gosec // file path provided by user via CLI flag
		if err != nil {
			return "", fmt.Errorf("reading template file: %w", err)
		}
		return string(data), nil
	}
	if len(args) == 0 {
		return "", errors.New("provide a template string or use -f <file>")
	}
	return args[0], nil
}

func runTplCreate(ctx context.Context, w io.Writer) error {
	if flagTplFile == "" {
		return errors.New("--file / -f is required for create")
	}

	data, err := os.ReadFile(flagTplFile) //nolint:gosec // file path provided by user via CLI flag
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}
	content := string(data)

	if !flagTplConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would create template sensor")
		_, _ = fmt.Fprintf(w, "  file:   %s\n", flagTplFile)
		_, _ = fmt.Fprintf(w, "  domain: %s\n", flagTplDomain)
		_, _ = fmt.Fprintf(w, "  size:   %d bytes\n", len(data))
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.CreateTemplate(ctx, content, flagTplDomain)
	if err != nil {
		return fmt.Errorf("creating template: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created template %q (domain=%s)\n", resp.UniqueID, flagTplDomain)
	return nil
}

func runTplDelete(ctx context.Context, w io.Writer, uniqueID string) error {
	if !flagTplConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete template sensor")
		_, _ = fmt.Fprintf(w, "  unique_id: %s\n", uniqueID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	if _, err := cc.DeleteTemplate(ctx, uniqueID); err != nil {
		return fmt.Errorf("deleting template: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted template %q\n", uniqueID)
	return nil
}
