package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var flagDashView string
var flagDashRaw bool
var flagDashFile string
var flagDashConfirm bool
var flagDashTitle string
var flagDashURLPath string
var flagDashIcon string
var flagDashSidebar bool
var flagDashAdmin bool

var dashCmd = &cobra.Command{
	Use:   "dash",
	Short: "Manage Lovelace dashboards",
	Long:  "List, inspect, create, and modify Home Assistant Lovelace dashboards.",
}

var dashLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List dashboards",
	Long:  "Show all Lovelace dashboards registered in Home Assistant.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var dashShowCmd = &cobra.Command{
	Use:   "show [url_path]",
	Short: "Show dashboard config",
	Long:  "Display dashboard views summary or raw JSON config. Omit url_path for the default dashboard.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		urlPath := ""
		if len(args) > 0 {
			urlPath = args[0]
		}
		return runDashShow(cmd.Context(), cmd.OutOrStdout(), urlPath)
	},
}

var dashSaveCmd = &cobra.Command{
	Use:   "save [url_path]",
	Short: "Save dashboard config (dry-run by default)",
	Long:  "Write a full dashboard config from JSON file or stdin. Use --confirm to apply.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		urlPath := ""
		if len(args) > 0 {
			urlPath = args[0]
		}
		return runDashSave(cmd.Context(), cmd.OutOrStdout(), urlPath)
	},
}

var dashCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new dashboard (dry-run by default)",
	Long:  "Create a new storage-mode Lovelace dashboard. Use --confirm to apply.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashCreate(cmd.Context(), cmd.OutOrStdout())
	},
}

var dashDeleteCmd = &cobra.Command{
	Use:   "delete <url_path>",
	Short: "Delete a dashboard (dry-run by default)",
	Long:  "Delete a Lovelace dashboard by url_path. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var dashResourcesCmd = &cobra.Command{
	Use:   "resources",
	Short: "List registered resources",
	Long:  "Show custom card/CSS resources registered in Lovelace.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashResources(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	dashShowCmd.Flags().StringVar(&flagDashView, "view", "", "show only the view with this path")
	dashShowCmd.Flags().BoolVar(&flagDashRaw, "raw", false, "output raw HA JSON (for LLM round-trip editing)")
	dashSaveCmd.Flags().StringVarP(&flagDashFile, "file", "f", "", "JSON config file (default: read from stdin)")
	dashSaveCmd.Flags().BoolVar(&flagDashConfirm, "confirm", false, "actually save (default is dry-run)")
	dashCreateCmd.Flags().StringVar(&flagDashURLPath, "url-path", "", "dashboard URL path (must contain a hyphen)")
	dashCreateCmd.Flags().StringVar(&flagDashTitle, "title", "", "dashboard title")
	dashCreateCmd.Flags().StringVar(&flagDashIcon, "icon", "", "dashboard icon (e.g. mdi:view-dashboard)")
	dashCreateCmd.Flags().BoolVar(&flagDashSidebar, "sidebar", true, "show in sidebar")
	dashCreateCmd.Flags().BoolVar(&flagDashAdmin, "admin", false, "require admin access")
	dashCreateCmd.Flags().BoolVar(&flagDashConfirm, "confirm", false, "actually create (default is dry-run)")
	dashDeleteCmd.Flags().BoolVar(&flagDashConfirm, "confirm", false, "actually delete (default is dry-run)")

	_ = dashCreateCmd.MarkFlagRequired("url-path")
	_ = dashCreateCmd.MarkFlagRequired("title")

	dashCmd.AddCommand(dashLsCmd, dashShowCmd, dashSaveCmd, dashCreateCmd, dashDeleteCmd, dashResourcesCmd)
	rootCmd.AddCommand(dashCmd)
}

func connectWS(ctx context.Context) (*haapi.WSClient, error) {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return nil, err
	}
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connecting to HA: %w", err)
	}
	return ws, nil
}

func runDashLs(ctx context.Context, w io.Writer) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	dashboards, err := ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}

	if len(dashboards) == 0 {
		_, _ = fmt.Fprintln(w, "no dashboards")
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"url_path", "title", "mode", "icon", "sidebar", "admin"},
		Rows:    make([][]string, len(dashboards)),
	}
	for i, d := range dashboards {
		tbl.Rows[i] = []string{
			d.URLPath,
			d.Title,
			d.Mode,
			d.Icon,
			strconv.FormatBool(d.ShowInSidebar),
			strconv.FormatBool(d.RequireAdmin),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func runDashShow(ctx context.Context, w io.Writer, urlPath string) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	// Raw mode: output unmodified HA JSON
	if flagDashRaw || flagJSON {
		raw, rawErr := ws.DashboardConfigRaw(ctx, urlPath)
		if rawErr != nil {
			return fmt.Errorf("fetching dashboard config: %w", rawErr)
		}
		if flagDashRaw {
			_, writeErr := w.Write(append(raw, '\n'))
			return writeErr
		}
		// --json: pretty-print
		var buf json.RawMessage
		if unmarshalErr := json.Unmarshal(raw, &buf); unmarshalErr != nil {
			_, writeErr := w.Write(append(raw, '\n'))
			return writeErr
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(buf)
	}

	cfg, err := ws.DashboardConfig(ctx, urlPath)
	if err != nil {
		return fmt.Errorf("fetching dashboard config: %w", err)
	}

	if len(cfg.Views) == 0 {
		_, _ = fmt.Fprintln(w, "no views")
		return nil
	}

	// If --view is set, find and display that specific view
	if flagDashView != "" {
		return showSingleView(w, cfg)
	}

	tbl := &format.Table{
		Headers: []string{"#", "title", "path", "type", "cards", "sections", "badges"},
		Rows:    make([][]string, len(cfg.Views)),
	}
	for i, raw := range cfg.Views {
		s := haapi.ParseViewSummary(raw)
		tbl.Rows[i] = []string{
			strconv.Itoa(i),
			s.Title,
			s.Path,
			viewType(s.Type),
			strconv.Itoa(s.Cards),
			strconv.Itoa(s.Sections),
			strconv.Itoa(s.Badges),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Full: true,
	})
}

func showSingleView(w io.Writer, cfg *haapi.LovelaceConfig) error {
	for _, raw := range cfg.Views {
		s := haapi.ParseViewSummary(raw)
		if s.Path == flagDashView || s.Title == flagDashView {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			var v any
			_ = json.Unmarshal(raw, &v)
			return enc.Encode(v)
		}
	}
	return fmt.Errorf("view %q not found", flagDashView)
}

func viewType(t string) string {
	if t == "" {
		return "masonry"
	}
	return t
}

func runDashSave(ctx context.Context, w io.Writer, urlPath string) error {
	// Read config JSON from file or stdin
	var data []byte
	var err error
	if flagDashFile != "" {
		data, err = os.ReadFile(filepath.Clean(flagDashFile))
		if err != nil {
			return fmt.Errorf("reading config file: %w", err)
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
	}

	// Validate JSON
	if !json.Valid(data) {
		return errors.New("invalid JSON in config input")
	}

	if !flagDashConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would save dashboard config")
		_, _ = fmt.Fprintf(w, "  url_path: %s\n", dashDisplayPath(urlPath))
		_, _ = fmt.Fprintf(w, "  config size: %d bytes\n", len(data))
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	if err := ws.DashboardConfigSave(ctx, urlPath, data); err != nil {
		return fmt.Errorf("saving dashboard config: %w", err)
	}

	_, _ = fmt.Fprintf(w, "saved dashboard config for %s\n", dashDisplayPath(urlPath))
	return nil
}

func runDashCreate(ctx context.Context, w io.Writer) error {
	if !flagDashConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would create dashboard")
		_, _ = fmt.Fprintf(w, "  url_path: %s\n", flagDashURLPath)
		_, _ = fmt.Fprintf(w, "  title:    %s\n", flagDashTitle)
		_, _ = fmt.Fprintf(w, "  icon:     %s\n", flagDashIcon)
		_, _ = fmt.Fprintf(w, "  sidebar:  %v\n", flagDashSidebar)
		_, _ = fmt.Fprintf(w, "  admin:    %v\n", flagDashAdmin)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	d, err := ws.DashboardCreate(ctx, haapi.DashboardCreateParams{
		URLPath:       flagDashURLPath,
		Title:         flagDashTitle,
		Icon:          flagDashIcon,
		ShowInSidebar: flagDashSidebar,
		RequireAdmin:  flagDashAdmin,
	})
	if err != nil {
		return fmt.Errorf("creating dashboard: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created dashboard %q (id: %s)\n", d.URLPath, d.ID)
	return nil
}

func runDashDelete(ctx context.Context, w io.Writer, urlPath string) error {
	if !flagDashConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete dashboard")
		_, _ = fmt.Fprintf(w, "  url_path: %s\n", urlPath)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	// Need dashboard ID for deletion â€” list and find by url_path
	dashboards, err := ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}

	var dashID string
	for _, d := range dashboards {
		if d.URLPath == urlPath {
			dashID = d.ID
			break
		}
	}
	if dashID == "" {
		return fmt.Errorf("dashboard %q not found", urlPath)
	}

	if err := ws.DashboardDelete(ctx, dashID); err != nil {
		return fmt.Errorf("deleting dashboard: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted dashboard %q\n", urlPath)
	return nil
}

func runDashResources(ctx context.Context, w io.Writer) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	resources, err := ws.ResourceList(ctx)
	if err != nil {
		return fmt.Errorf("listing resources: %w", err)
	}

	if len(resources) == 0 {
		_, _ = fmt.Fprintln(w, "no resources")
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"id", "type", "url"},
		Rows:    make([][]string, len(resources)),
	}
	for i, r := range resources {
		tbl.Rows[i] = []string{r.ID, r.Type, r.URL}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func dashDisplayPath(urlPath string) string {
	if urlPath == "" {
		return "(default)"
	}
	return urlPath
}
