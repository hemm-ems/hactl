package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// helperDomains lists every HA domain a helper entity can live in — the
// superset used to discover storage-backed (UI-created) helpers via
// /api/states. This is wider than the companion's YAML-managed domain set
// (see hactl-companion routes/helpers.py ALLOWED_DOMAINS): input_button has
// no YAML equivalent, so it only ever shows up here, sourced from storage.
var helperDomains = []string{
	"input_boolean", "input_number", "input_select", "input_text",
	"input_datetime", "input_button", "counter", "timer", "schedule",
}

// helperCreateDomains is the narrower set `helper create` can write: the
// companion's own ALLOWED_DOMAINS, which is helperDomains minus input_button —
// input_button exists only as a UI collection and has no YAML schema at all.
//
// Two sets rather than one, because read and write have different reach and a
// single list would have to be wrong for one of them. The companion says the
// same thing in the same words (`STORAGE_DOMAINS > ALLOWED_DOMAINS`), and
// TestE2EHelperCreateDomainSetMatchesTheCompanionCLI holds this copy to the
// companion's by asking it, so the mirror cannot drift into folklore.
//
// The set is checked locally because the companion's is not the only gate a
// create passes, and the other one answers a DIFFERENT question. C-10's wiring
// probe asks whether Home Assistant reads `<domain>` config from a file this
// route can extend — true of `script`, `automation` and `template`, none of
// which `helper create` may write. So `helper create script -f x.yaml` printed
// "dry-run: would create helper" at exit 0 while --confirm answers 400
// "Invalid helper domain" (H-2). One predicate was standing in for two
// questions, which is the same shape as live-fire #24 and D-24.
var helperCreateDomains = []string{
	"counter", "input_boolean", "input_datetime", "input_number",
	"input_select", "input_text", "schedule", "timer",
}

// helperRow is one row in the merged helper listing: either a YAML helper
// (companion-managed, editable via `helper create`/`set`/`delete`) or a
// storage-backed helper entity discovered live in HA's .storage — not
// editable through hactl's helper CRUD. See issue #71.
type helperRow struct {
	ID     string
	Name   string
	Domain string
	Icon   string
	Source string // "yaml" or "storage"
}

var flagHelperDomain string
var flagHelperPattern string
var flagHelperName string
var flagHelperFile string
var flagHelperConfirm bool

var helperCmd = family(&cobra.Command{
	Use:        "helper",
	SuggestFor: []string{"helpers", "input_boolean", "input_number"},
	Short:      "Manage HA helpers (input_boolean, counter, timer, etc.)",
	Long:       "List, create, and delete Home Assistant helper entities via the companion.",
})

var helperLsCmd = &cobra.Command{
	Use:   "ls",
	Args:  takesNone(),
	Short: "List helpers",
	Long: "List all helpers, optionally filtered by domain. Unions YAML helpers (companion-managed) " +
		"with storage-backed helpers created in the HA UI (discovered live via the entity states), " +
		"distinguished by a source column — only the yaml ones are editable via create/set/delete.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperLs(cmd, cmd.OutOrStdout())
	},
}

var helperShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show helper details",
	Long:  "Show the YAML definition of a helper entity.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var helperCatCmd = &cobra.Command{
	Use:   "cat <id>",
	Short: "Print a helper's remote config as YAML",
	Long:  "Fetch and print the current remote YAML definition of a helper, with no header (pipe-friendly).",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperCat(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var helperCreateCmd = &cobra.Command{
	Use:   "create <domain>",
	Short: "Create a new helper (dry-run by default)",
	Long: `Create a new helper from a YAML file via the companion.
Supported domains: input_boolean, input_number, input_select, input_text,
input_datetime, counter, timer, schedule.
Use --confirm to apply.`,
	Args: takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperCreate(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var helperDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a helper (dry-run by default)",
	Long:  "Delete a helper entity via the companion. Use --confirm to apply.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	helperLsCmd.Flags().StringVar(&flagHelperDomain, "domain", "", "filter by domain (e.g. input_boolean)")
	helperLsCmd.Flags().StringVar(&flagHelperPattern, "pattern", "", "filter by helper id (substring or glob)")
	helperLsCmd.Flags().StringVar(&flagHelperName, "name", "", "filter by display name substring")
	helperCreateCmd.Flags().StringVarP(&flagHelperFile, "file", "f", "", "YAML file for the new helper")
	helperCreateCmd.Flags().BoolVar(&flagHelperConfirm, "confirm", false, "actually create (default is dry-run)")
	helperDeleteCmd.Flags().BoolVar(&flagHelperConfirm, "confirm", false, "actually delete (default is dry-run)")
	helperCmd.AddCommand(helperLsCmd, helperShowCmd, helperCatCmd, helperCreateCmd, helperDeleteCmd)
	rootCmd.AddCommand(helperCmd)
}

// runHelperCat prints a helper's remote YAML config verbatim, without the
// id/domain header that `helper show` prints — pipe-friendly and consistent
// with `auto cat` / `script cat` / `tpl cat`.
func runHelperCat(ctx context.Context, w io.Writer, helperID string) error {
	markStructuredOutput()
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.GetHelper(ctx, helperID)
	if err != nil {
		return fmt.Errorf("fetching helper: %w", err)
	}
	_, _ = fmt.Fprint(w, resp.Content)
	return nil
}

// runHelperLs lists the union of YAML-sourced helpers (companion's per-domain
// files) and storage-backed helper entities (created in the HA UI, living in
// .storage — invisible to the companion, which only ever reads/writes YAML).
// Most real instances create helpers in the UI, so listing YAML alone reports
// "no helpers" while dozens exist live. See issue #71.
func runHelperLs(cmd *cobra.Command, w io.Writer) error {
	ctx := cmd.Context()
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	// Fetch unfiltered: --domain is applied below, after storage helpers are
	// merged in, so it also reaches storage-only domains (e.g. input_button)
	// the companion's YAML CRUD doesn't manage.
	resp, err := cc.ListHelpers(ctx, "")
	if err != nil {
		return fmt.Errorf("listing helpers: %w", err)
	}

	rows := make([]helperRow, 0, len(resp.Helpers))
	yamlEntityIDs := make(map[string]bool, len(resp.Helpers))
	for _, h := range resp.Helpers {
		rows = append(rows, helperRow{ID: h.ID, Name: h.Name, Domain: h.Domain, Icon: h.Icon, Source: "yaml"})
		yamlEntityIDs[h.Domain+"."+h.ID] = true
	}
	storage, err := fetchStorageHelpers(ctx, yamlEntityIDs)
	if err != nil {
		return err
	}
	rows = append(rows, storage...)
	// Before the filters run: the number that tells a caller whether their
	// filter missed or the instance is empty. See emptyListing.
	total := len(rows)

	if flagHelperDomain != "" {
		rows = filterHelperRowsByDomain(rows, flagHelperDomain)
	}
	if flagHelperPattern != "" {
		rows = filterHelperRowsByPattern(rows, flagHelperPattern)
	}
	if flagHelperName != "" {
		rows = filterHelperRowsByName(rows, flagHelperName)
	}

	if len(rows) == 0 {
		return emptyListing(cmd, w, "helpers", total)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Domain != rows[j].Domain {
			return rows[i].Domain < rows[j].Domain
		}
		return rows[i].ID < rows[j].ID
	})

	tbl := &format.Table{
		Headers: []string{"id", "name", "domain", "icon", "source"},
		Rows:    make([][]string, len(rows)),
	}
	for i, r := range rows {
		tbl.Rows[i] = []string{r.ID, r.Name, r.Domain, r.Icon, r.Source}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

// fetchStorageHelpers discovers UI-created helper entities by scanning
// /api/states for entities in a helper domain (helperDomains) that the
// companion's YAML listing didn't already report (skip, keyed by entity_id).
// The id shown is the full entity_id, not a bare slug: unlike YAML helpers,
// there is no `helper show`/`cat` lookup for these — use `ent show` instead.
// Failures here are logged and treated as "no storage helpers found" rather
// than failing the whole command: an unreachable HA shouldn't hide the
// YAML-sourced list that already answered above.
func fetchStorageHelpers(ctx context.Context, skip map[string]bool) ([]helperRow, error) {
	cfg, err := config.Load(flagDir)
	if err != nil {
		slog.Warn("could not load config for storage helper discovery", "error", err)
		return nil, nil
	}

	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetStates(ctx)
	if err != nil {
		slog.Warn("could not fetch states for storage helper discovery", "error", err)
		return nil, nil
	}

	var states []entityState
	if unmarshalErr := json.Unmarshal(data, &states); unmarshalErr != nil {
		slog.Warn("could not parse states for storage helper discovery", "error", unmarshalErr)
		return nil, nil
	}
	// An unreachable HA is best-effort; a states payload that decoded to
	// nothing is not — it would silently hide every storage helper.
	if degErr := degeneracy.Check("/api/states", &states); degErr != nil {
		return nil, degErr
	}

	domains := make(map[string]bool, len(helperDomains))
	for _, d := range helperDomains {
		domains[d] = true
	}

	var rows []helperRow
	for _, s := range states {
		domain := haapi.EntityIDDomain(s.EntityID)
		if !domains[domain] || skip[s.EntityID] {
			continue
		}
		name, _ := s.Attributes["friendly_name"].(string)
		if name == "" {
			name = s.EntityID
		}
		icon, _ := s.Attributes["icon"].(string)
		rows = append(rows, helperRow{ID: s.EntityID, Name: name, Domain: domain, Icon: icon, Source: "storage"})
	}
	return rows, nil
}

// filterHelperRowsByDomain keeps rows in exactly one domain, matched without
// regard to case (D-2).
//
// It compared with `==` while --pattern and --name in the same command were
// explicitly case-insensitive, so `helper ls --domain INPUT_BOOLEAN` answered
// for 60 real input_boolean helpers — and answered it through the same "no
// helpers" line that claims the instance holds none. Domain names are always
// lowercase in HA, which is precisely why nothing here had a stake in the
// answer and why the pole had to be decided rather than harmonised toward the
// flag with nothing to lose.
func filterHelperRowsByDomain(rows []helperRow, domain string) []helperRow {
	out := make([]helperRow, 0, len(rows))
	for _, r := range rows {
		if strings.EqualFold(r.Domain, domain) {
			out = append(out, r)
		}
	}
	return out
}

// filterHelperRowsByPattern keeps rows whose id matches the glob/substring
// pattern, case-insensitively via the shared matchPattern (D-2). The id is
// the identifier hactl resolves for a helper — the YAML slug for
// companion-managed rows, the full entity_id for storage rows — and per D-1
// --pattern matches identifiers only; display names are --name's job.
func filterHelperRowsByPattern(rows []helperRow, pattern string) []helperRow {
	out := make([]helperRow, 0, len(rows))
	for _, r := range rows {
		if matchPattern(r.ID, pattern) {
			out = append(out, r)
		}
	}
	return out
}

// filterHelperRowsByName keeps rows whose display name contains the needle,
// case-insensitively like every sibling name filter (D-2, cf. device ls).
func filterHelperRowsByName(rows []helperRow, name string) []helperRow {
	out := make([]helperRow, 0, len(rows))
	for _, r := range rows {
		if containsFold(r.Name, name) {
			out = append(out, r)
		}
	}
	return out
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

	// --json used to be a byte-for-byte no-op here: the flag was never read and
	// the text form came back, exit 0, so a caller that standardised on --json
	// got a hard parse error from a command every one of whose siblings returns
	// an object. It survived because `helper show` sat on json_contract_test's
	// `companionRequired` list, which the sweep printed instead of asserting
	// on. That list is gone: the sweep's fixture now stands up a companion stub
	// (json_contract_companion_test.go), so TestJSONContract asserts this
	// branch. Its verbatim sibling is `helper cat`; this command is not
	// verbatim.
	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		out := map[string]any{
			"id":      resp.ID,
			"domain":  resp.Domain,
			"content": resp.Content,
		}
		if resp.Source != "" {
			out["source"] = resp.Source
		}
		return enc.Encode(out)
	}

	_, _ = fmt.Fprintf(w, "id:     %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "domain: %s\n", resp.Domain)
	// The same column `helper ls` shows, on the same helper. Omitted rather than
	// guessed when the companion predates the field: printing "yaml" for an
	// unknown source would be inventing the one fact this line exists to state.
	if resp.Source != "" {
		_, _ = fmt.Fprintf(w, "source: %s\n", resp.Source)
	}
	_, _ = fmt.Fprintf(w, "---\n%s", resp.Content)
	return nil
}

// helperSourceStorage is the companion's marker for a helper created in HA's
// UI: readable, never editable through this CLI.
const helperSourceStorage = "storage"

func runHelperCreate(ctx context.Context, w io.Writer, domain string) error {
	if flagHelperFile == "" {
		return errors.New("--file / -f is required for create")
	}

	// The domain first, and locally: it decides which file is written, it is
	// knowable without asking anything, and the remote probe below cannot
	// answer it (see helperCreateDomains).
	if domainErr := validHelperCreateDomain(domain); domainErr != nil {
		return domainErr
	}

	data, err := os.ReadFile(flagHelperFile) //nolint:gosec // file path provided by user via CLI flag
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}
	content := string(data)

	// Parse the input before planning. The companion requires a mapping keyed
	// by the helper id; a bare `name:`/`icon:` mapping is a 400 — and nothing
	// documented that, while the preview reported the file's size and happily
	// planned the write.
	helperID, err := helperCreateID(content)
	if err != nil {
		return err
	}

	// The id, still locally, and before the network: it is the object_id half
	// of the entity_id this create promises to mint, and Home Assistant's own
	// rule decides whether that is a usable one.
	if idErr := validHelperCreateID(domain, helperID); idErr != nil {
		return idErr
	}

	if !flagHelperConfirm {
		cc, connErr := connectCompanion(ctx)
		if connErr != nil {
			return connErr
		}
		// H-2: the preview fails where --confirm would. Parsing the input is not
		// enough — on an instance whose `input_boolean:` is written inline in
		// configuration.yaml (rather than `!include`-ing a file), every create is
		// a structural 400 and every preview used to print "would create" anyway:
		// 8 domains, 8 confident plans, 8 deterministic failures. The layout is
		// knowable in advance, and only the companion can answer it without
		// hactl re-deriving its include-resolution rules in Go.
		if wiringErr := checkHelperDomainWired(ctx, cc, domain); wiringErr != nil {
			return wiringErr
		}
		return dryRun("create helper").
			with("id", helperID).
			with("domain", domain).
			with("file", flagHelperFile).
			with("bytes", len(data)).
			render(w)
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.CreateHelper(ctx, content, domain)
	if err != nil {
		return fmt.Errorf("creating helper: %w", err)
	}

	res := done("create helper").
		with("id", resp.ID).
		with("domain", domain).
		with("reloaded", resp.Reloaded).
		withIf(resp.EntityCreated != nil, "entity_created", resp.EntityCreated).
		withIf(resp.EntityID != "", "entity_id", resp.EntityID).
		text("created helper %q (domain=%s)", resp.ID, domain)
	switch {
	case !resp.Reloaded:
		res = res.warn("helper written but HA did not confirm reload%s", reloadReasonSuffix(resp.ReloadError))
	case resp.EntityCreated != nil && !*resp.EntityCreated:
		res = res.warn("helper reloaded but entity %q was not found in HA's live state", resp.EntityID)
	default:
		res = res.text("entity_id: %s", resp.EntityID)
	}
	res = warnIfReformatted(res, resp.Reformatted)
	return res.render(w)
}

// validHelperCreateDomain refuses a domain `helper create` cannot write, in
// the companion's own words and before anything is planned or sent.
func validHelperCreateDomain(domain string) error {
	if slices.Contains(helperCreateDomains, domain) {
		return nil
	}
	return fmt.Errorf("invalid helper domain: %s. Allowed: %s",
		domain, strings.Join(helperCreateDomains, ", "))
}

// validHelperCreateID refuses an id Home Assistant cannot turn into an entity.
//
// The helper id is the object_id half of `<domain>.<id>` — the companion mints
// exactly that entity_id and hactl prints it on success — so the rule is
// haapi.ValidEntityID applied to the id this create will produce, which is HA's
// own VALID_ENTITY_ID mirrored clause by clause and pinned to the running
// container by TestOracleEntityIDRule.
//
// Nothing checked it anywhere: hactl echoed `id: pg w1 space`, `id:
// pg_w1_umlaut_öäü` and a 226-character id back as a plan at exit 0, and the
// companion writes whatever key it is handed. The harm is not the plan. HA
// validates a helper file with `cv.schema_with_slug_keys`, which fails the
// WHOLE mapping on one bad key — so a single unusable id written into a shared
// per-domain file takes every working helper in that file down with it, and
// `check_config` reports it as a config error nowhere near the write.
//
// HA's slug rule and this one differ on exactly one input, the empty string:
// `cv.slug("")` passes (slugify("") == "") while `input_boolean.` is not an
// entity_id anybody can address. Being stricter there is refusing something HA
// would accept into the file and then fail to build an entity from — and a
// blank identifier never resolves to anything in this CLI anyway (#45/#92).
func validHelperCreateID(domain, helperID string) error {
	if haapi.ValidEntityID(domain + "." + helperID) {
		return nil
	}
	return fmt.Errorf("helper id %q is not usable: Home Assistant would create %q, and an entity_id "+
		"is lowercase letters, digits and single underscores, not starting or ending with one "+
		"(no spaces, accents, capitals or dots)", helperID, domain+"."+helperID)
}

// helperCreateID validates a `helper create` input and returns the helper id
// it declares: the file must be a mapping with exactly one top-level key,
// which is the id (`my_toggle: {name: …}`).
func helperCreateID(content string) (string, error) {
	var top map[string]any
	if err := yaml.Unmarshal([]byte(content), &top); err != nil {
		return "", fmt.Errorf("parsing helper YAML: %w", err)
	}
	if len(top) != 1 {
		return "", fmt.Errorf("helper YAML must be a mapping with exactly one top-level key (the helper id), "+
			"got %d — e.g. `my_toggle:` with `name:`/`icon:` nested under it", len(top))
	}
	for id, body := range top {
		if _, ok := body.(map[string]any); !ok {
			return "", fmt.Errorf("helper %q must be a YAML mapping", id)
		}
		return id, nil
	}
	return "", errors.New("helper YAML must not be empty")
}

func runHelperDelete(ctx context.Context, w io.Writer, helperID string) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	// Resolve before planning, so the dry run fails exactly where the
	// confirmed run would rather than describing a delete that cannot happen.
	// Only YAML helpers are addressable here — a storage-backed helper (the
	// `source: storage` rows in `helper ls`) has no companion definition and
	// this is where the caller finds that out.
	//
	// One confirm-time failure this cannot predict: the same slug may exist in
	// two domains, where the companion's GET returns the first match but its
	// DELETE refuses with a 409 listing the candidates.
	remote, err := cc.GetHelper(ctx, helperID)
	if err != nil {
		return fmt.Errorf("helper %q not found among the YAML helpers "+
			"(use 'helper ls' — rows with source=storage are not editable through hactl): %w", helperID, err)
	}

	// The other half of the same law. The companion now *resolves* a
	// storage-backed helper — that is what makes `helper show`/`cat` work at all
	// on a UI-managed instance — so a lookup that succeeds no longer means the
	// target is deletable. Without this check the preview would print a plan for
	// a delete whose --confirm is a 409, which is exactly the H-2 inversion the
	// resolve-before-planning rule exists to prevent.
	if remote.Source == helperSourceStorage {
		return fmt.Errorf("helper %q is storage-backed (created in the HA UI, source=storage in 'helper ls'): "+
			"it has no YAML definition to delete. Remove it in the UI; 'helper show' can still read it", helperID)
	}

	if !flagHelperConfirm {
		return dryRun("delete helper").
			with("id", remote.ID).
			with("domain", remote.Domain).
			render(w)
	}

	// Resolve while the entity is still real; afterwards a registry entry is
	// indistinguishable from an older ghost.
	//
	// This cleanup was the fourth family's share of a fix that reached three.
	// removeOrphanedEntity was extracted precisely so `auto`, `script` and
	// `tpl` would stop leaving different amounts of debris for the same
	// operation — and `helper delete` was not in the sentence, so it kept
	// leaving the `unavailable`/`restored: true` row that docs/manual.md
	// promises is removed, in a block that names `helper delete` explicitly.
	cfg, cfgErr := config.Load(flagDir)
	orphan := ""
	if cfgErr == nil {
		orphan = registeredEntityID(ctx, cfg, remote.Domain+"."+remote.ID)
	}

	if _, err := cc.DeleteHelper(ctx, helperID); err != nil {
		return fmt.Errorf("deleting helper: %w", err)
	}

	if orphan != "" {
		removeOrphanedEntity(ctx, cfg, orphan)
	}
	return done("delete helper").
		with("id", helperID).
		with("domain", remote.Domain).
		withIf(orphan != "", "entity_id", orphan).
		text("deleted helper %q", helperID).
		render(w)
}
