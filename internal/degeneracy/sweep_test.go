package degeneracy_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// This file is the classification sweep the package documentation promises: it
// derives the set of wire structs and the set of decode sites from the source
// itself, and fails on anything it cannot account for.
//
// Both halves are deliberately source-derived rather than hand-listed. A
// hand-listed inventory is written once and then rots — which is how
// `trace/get` decoded to an all-zero struct for months while the enumeration
// that would have caught it was "obviously complete". Adding a wire struct or a
// decode site therefore fails this test until the new path is classified, and
// removing one fails it until the stale entry is deleted.

// The package set this sweep quantifies over is degeneracy.WirePackages —
// shared with surfaceaudit.DecodeSurface (H-7), which derives every decode
// site *outside* it, so a package absent from the list surfaces there rather
// than nowhere. See packages.go for the contract.

// unidentifiedWireStructs is the other half of the H-14 classification: every
// json-tagged struct that deliberately has no Identity, and the reason. A zero
// value that is a *legitimate* answer must never be poisoned — a gate that
// fires on correct behaviour gets deleted by the next person to trip it.
//
// The reasons here are load-bearing. Each says why no field of the struct can
// distinguish "the wire said nothing" from "the answer is empty".
var unidentifiedWireStructs = map[string]string{
	// ---- hactl's own output shapes: rendered, never decoded from a wire ----
	"autoShowResult":        "hactl's own --json output shape for `auto show`",
	"autoShowTrace":         "hactl's own --json output shape for `auto show`",
	"scriptShowResult":      "hactl's own --json output shape for `script show`",
	"scriptShowTrace":       "hactl's own --json output shape for `script show`",
	"configShowResult":      "hactl's own --json output shape for `config show`",
	"companionStatusResult": "hactl's own --json output shape for `companion status`",
	"healthResult":          "hactl's own --json output shape for `health`",
	"dryRunJSON":            "hactl's own --json output shape for dry runs",
	"writeResultJSON":       "hactl's own --json output shape for confirmed writes, dryRunJSON's counterpart",
	"entWhoJSON":            "hactl's own --json output shape for `ent who`",
	"entWhoEventJSON":       "hactl's own --json output shape for `ent who`",
	"entWhoSummaryJSON":     "hactl's own --json output shape for `ent who`",
	"entWhoWindowJSON":      "hactl's own --json output shape for `ent who`",
	"versionInfo":           "hactl's own --json output shape for `version`",
	"noStoredConfigReport": "hactl's own --json output shape for `dash show` on a dashboard " +
		"Home Assistant holds no config for",
	"DashboardCreateParams": "a request body hactl sends, not a payload it decodes",
	"witnessData": "hactl's own cache file (confirm-witness.json), written and read by hactl " +
		"itself; nothing on a wire produces it and nothing renders it",

	// ---- transport envelopes: a decode failure here is already an error ----
	"wsMessage":       "WebSocket transport envelope; a bad frame fails the read, not a render",
	"wsResponse":      "WebSocket transport envelope; success/error is checked explicitly",
	"wsError":         "WebSocket error envelope; only read when success is already false",
	"flowRawResponse": "intermediate decode; the FlowResult built from it carries the identity",

	// ---- zero value is a legitimate answer ----
	"LovelaceConfig": "a dashboard with no views is a real, empty dashboard",
	"LovelaceViewSummary": "views are legitimately untitled, pathless and iconless; the counts " +
		"are ints, so no field distinguishes absent from zero",
	"Context": "HA leaves user_id/parent_id empty for its own state changes — which is " +
		"exactly what `ent who` reports",
	"TraceSummaryTimestamp": "a run that is still executing has no finish timestamp",
	"automationAttributes": "a `restored` ghost automation legitimately arrives with an empty " +
		"attribute set, and `auto ls` must keep listing it",
	"scriptAttributes": "same as automationAttributes: a restored ghost has no attributes",
	"ServiceDescriptor": "a service that documents neither fields nor a target is a real answer — " +
		"89 of the reference instance's 434 do exactly that, and svc call's payload check " +
		"treats an unpublished schema as \"nothing may be refused\" rather than as an error",
	"ServiceField":     "a leaf field carries no nested fields; that emptiness is what separates it from a section",
	"WireGuardIface":   "every field is optional; nothing there separates absent from not-decoded",
	"WireGuardMonitor": "every field is optional, and the booleans are honestly false when idle",
	"AutomationDefinition": "an automation with no `id:` is legal HA and the companion reports " +
		"it as an empty id (routes/automations.py)",

	"energySourceRow": "hactl's own --json output shape for `energy show`",
	"EnergyPreferences": "a configured-but-empty energy dashboard is a real answer (both lists " +
		"legitimately empty); the source/flow/device elements carry the identity, and the " +
		"unconfigured case never reaches the decode — HA answers a WS error (\"No prefs\", " +
		"oracle-probed), which EnergyGetPrefs surfaces before anything renders",

	// ---- list wrappers: an empty list is the answer, the elements carry identity ----
	"AutomationsResponse": "empty list is a legitimate answer; AutomationDefinition elements are checked",
	"ScriptsResponse":     "empty list is a legitimate answer; ScriptDefinition elements are checked",
	"TemplatesResponse":   "empty list is a legitimate answer; TemplateDefinition elements are checked",
	"HelpersResponse":     "empty list is a legitimate answer; HelperDefinition elements are checked",
	"ConfigFilesResponse": "empty list is a legitimate answer; the entries are bare strings",
	"RefEntitiesResponse": "empty list is a legitimate answer; RefEntity elements are checked",
	"LogsResponse":        "empty list is a legitimate answer; LogEntry elements are checked",
	"issuesResponse":      "empty list is a legitimate answer; haIssue elements are checked",

	// ---- analyze: the trace renderer spells its own degeneracy ----
	"RawTraceRun": "FormatCondensed renders UnparsedMarker for a run that decoded to nothing " +
		"(H-7) — the original D1 defense, kept where it is",
	"RawTraceMeta":   "part of RawTraceRun, covered by the same renderer",
	"RawTimestamp":   "part of RawTraceRun, covered by the same renderer",
	"CondensedTrace": "analyze's own output shape, built from RawTraceRun rather than decoded",
	"CondensedStep":  "analyze's own output shape",
	"Anomaly":        "analyze's own output shape",
	"DedupedLog":     "analyze's own output shape",
	"LogEntry@internal/analyze": "analyze's own output shape; the companion LogEntry of the " +
		"same name is the decoded one and is checked",
}

// uncheckedDecodeSites are json.Unmarshal calls whose enclosing function
// deliberately does not call degeneracy.Check, keyed "<path>:<func>:<target>".
// Keyed by name rather than by line so the table survives edits above it.
var uncheckedDecodeSites = map[string]string{
	// Targets with no identity to check — see unidentifiedWireStructs.
	"internal/haapi/flow.go:ConfigFlowHandlers:handlers": "decodes into []string",
	// The three legs of reading a modern HA form field's type (#82). None can
	// render a zero value as an answer: an absent or unreadable selector leaves
	// the field's own `type` in place and the table falls back to "string",
	// which is what an unadorned field IS — and a select whose options do not
	// decode prints no choice list, exactly as before this existed.
	"internal/haapi/flow.go:parseSelector:wrapper":           "an unreadable selector leaves the field's declared type",
	"internal/haapi/flow.go:parseSelector:sel":               "a select with no readable options prints no choices",
	"internal/haapi/flow.go:parseSelectOptions:obj":          "one option shape of three; a miss skips that option",
	"internal/haapi/lovelace.go:ParseViewSummary:v":          "LovelaceViewSummary has no identity",
	"internal/haapi/lovelace.go:ParseLovelaceConfig:cfg":     "LovelaceConfig has no identity",
	"internal/haapi/websocket.go:DashboardConfigSave:parsed": "validates hactl's own outgoing body",
	"internal/cmd/dash.go:selectDashboardViewRaw:cfg":        "LovelaceConfig has no identity",
	"internal/haapi/lovelace.go:LovelaceStrategyType:doc": "an ABSENT strategy is the answer for " +
		"every ordinary dashboard, so a zero decode here is the common case rather than a " +
		"degenerate one — the caller has already parsed the same document for its views",
	"internal/cmd/dash.go:showSingleView:v":               "decodes one raw view into any, for YAML re-encoding",
	"internal/cmd/svc.go:runSvcCall:data":                 "decodes the user's own --data argument",
	"internal/cmd/witness.go:loadWitness:loaded": "a witness file that is missing, truncated or " +
		"corrupt must read as NO previews recorded — the direction that REFUSES. A zero decode " +
		"here cannot render as an answer because it renders as `--confirm refused`, naming the " +
		"dry-run to run; the failure mode H-7 guards against is the opposite one",
	"internal/cmd/flow.go:diagnosticsConfigData:envelope": "raw diagnostics passthrough, with an explicit fallback",
	"internal/cmd/flow.go:runConfigFlowStep:rawData":      "decodes the user's own --data argument",
	"internal/cmd/states.go:decodeStateAttributes:attrs": "decodes ONE entity's attributes into " +
		"automationAttributes/scriptAttributes, both of which are legitimately empty on a restored " +
		"ghost (see unidentifiedWireStructs); the record's identity lives on the statesEnvelope " +
		"around it, and fetchDomainStates checks every envelope in the payload (H-21)",

	// Explicitly guarded: the function checks the field it needs is non-empty.
	"internal/haapi/websocket.go:IngressSession:resp": "errors explicitly on an empty session token",
	"internal/haapi/flow.go:parseSchemaFields:field": "the FlowResult these fields hang off is " +
		"checked, which walks into them",
	"internal/haapi/flow.go:parseMenuOptions:ids": "union-shape probe (list-or-map wire form); " +
		"the FlowResult it hangs off is checked, and a menu whose options decode to nothing is " +
		"rendered as exactly that with a --json pointer, never as an empty success",
	"internal/haapi/flow.go:parseMenuOptions:labeled": "second half of the same union-shape probe; " +
		"same reasoning as its sibling line",
	"internal/haapi/flow.go:parseSelectOptions:s": "union-shape probe (string-or-pair option form); " +
		"a non-decoding option is skipped, and the FlowResult above is checked",
	"internal/haapi/flow.go:parseSelectOptions:pair": "second half of the same union-shape probe; " +
		"same reasoning as its sibling line",
	"internal/cmd/flow.go:flowIDOf:v": "best-effort flow_id salvage for cleanup; callers handle " +
		"an empty id",
	"internal/cmd/flow.go:runConfigDelete:haAnswer": "the delete already succeeded when this runs; " +
		"the anonymous struct carries HA's single `require_restart` boolean into the result document, " +
		"and false is the answer HA gives for the common case, so a zero decode is a legitimate value " +
		"rather than a missing one — the identity of the thing deleted comes from the config entry " +
		"resolved before the write, not from this response",
	"internal/cmd/flow.go:optionsFlowCurrentValues:raw":   "raw schema passthrough; fields are filtered below",
	"internal/cmd/flow.go:optionsFlowCurrentValues:field": "skips any field whose name did not decode",

	// Raw JSON walked structurally or re-encoded, never rendered as a record.
	"internal/cmd/dash.go:walkDashboardConfigs:root": "walked by jsonwalk, not rendered; the one " +
		"dashboard walk every scan goes through",
	"internal/cmd/dash.go:dashReplaceOne:root":  "walked by jsonwalk, not rendered",
	"internal/cmd/dash.go:runDashShow:v":        "raw config decoded into any for YAML re-encoding",
	"internal/cmd/dash.go:runDashShow:buf":      "raw config round-trip for re-indenting",
	"internal/cmd/trace.go:runTraceShow:pretty": "re-indents the raw trace for display",
	"internal/cmd/trace.go:runTraceShow:raw":    "analyze.FormatCondensed spells its own UNPARSED (H-7)",

	// internal/analyze: the trace path keeps the H-7 defense it already has —
	// FormatCondensed renders UNPARSED for a run that decoded to nothing, and
	// every extractor below is a sub-read of that same run.
	"internal/analyze/trace.go:UnmarshalJSON:rt.Trace":       "RawTrace metadata; H-7 covers the run",
	"internal/analyze/trace.go:UnmarshalJSON:envelope":       "RawTrace step envelope; H-7 covers the run",
	"internal/analyze/trace.go:parseTrigger:s":               "trigger description is a bare string or list",
	"internal/analyze/trace.go:parseTrigger:arr":             "trigger description is a bare string or list",
	"internal/analyze/trace.go:extractTriggerDetail:cv":      "changed_variables is free-form per integration",
	"internal/analyze/trace.go:extractTriggerDetail:trigger": "trigger payload is free-form per platform",
	"internal/analyze/trace.go:extractActionDetail:result":   "action results are free-form per service",
	"internal/analyze/trace.go:stepOutcome:result":           "step results are free-form per step type",
}

// inheritedUnprobed marks an identity field that was declared before this gate
// existed and whose premise has never been checked against a live instance.
//
// It is debt, written down. It is not an excuse: the baseline below may only
// shrink, so every one of these is a question someone still owes an answer to.
const inheritedUnprobed = "inherited: declared before this gate existed; its premise has not been probed"

// unprobedIdentityFieldBaseline is how many inheritedUnprobed entries existed
// when the gate was built. TestSweep_UnprobedIdentityFieldsOnlyShrink refuses
// any number above it, so the debt is a ratchet rather than a habit. Lower it
// whenever a field is probed — never raise it.
const unprobedIdentityFieldBaseline = 81

// identityFieldEvidence is the field-level half of the H-14 classification, and
// it exists because the struct-level half could not have caught finding #38.
//
// unidentifiedWireStructs asks a question about each STRUCT — does it declare
// an identity, or say why it cannot. Both answers are checkable and the sweep
// is green either way. What no gate asked was whether a declared identity field
// is SOUND: whether the zero value it treats as evidence of a missing wire
// field is a value Home Assistant can legitimately send.
//
// For `state` it could. Five structs declared it, all on one comment — "HA
// rejects an empty state string" — that was never probed and is false. Home
// Assistant served 62 of 407 history records with `"state": ""`, the key
// present on every one, and `ent hist` exited 1 with empty stdout as a result.
// The check compares the decoded value against Go's zero value and cannot see
// whether the wire carried the key, so a legitimate empty value and a renamed
// field are the same observation to it. That makes "empty is never legitimate
// here" a premise every identity field rests on, and this map is where each one
// states its grounds.
//
// A reason of inheritedUnprobed is the honest answer for a field nobody has
// checked. Fabricating a plausible-sounding justification here would be the
// exact failure the map exists to prevent, one layer up.
var identityFieldEvidence = map[string]string{
	"AreaEntry.area_id":                    inheritedUnprobed,
	"AutomationCreateResponse.id":          inheritedUnprobed,
	"AutomationCreateResponse.status":      inheritedUnprobed,
	"AutomationResponse.id":                inheritedUnprobed,
	"CheckConfigResponse.status":           inheritedUnprobed,
	"ConfigBlockResponse.id":               inheritedUnprobed,
	"ConfigBlockResponse.path":             inheritedUnprobed,
	"ConfigDeleteResponse.status":          inheritedUnprobed,
	"ConfigFileResponse.path":              inheritedUnprobed,
	"ConfigWriteResponse.status":           inheritedUnprobed,
	"DeviceConsumption.stat_consumption":   inheritedUnprobed,
	"DeviceRegistryEntry.id":               inheritedUnprobed,
	"EnergyFlow.stat_energy_from":          inheritedUnprobed,
	"EnergySource.type":                    inheritedUnprobed,
	"EntityRegistryEntry.entity_id":        inheritedUnprobed,
	"FloorEntry.floor_id":                  inheritedUnprobed,
	"FlowResult.type":                      inheritedUnprobed,
	"HealthResponse.status":                inheritedUnprobed,
	"HealthResponse.version":               inheritedUnprobed,
	"HelperCreateResponse.id":              inheritedUnprobed,
	"HelperCreateResponse.status":          inheritedUnprobed,
	"HelperDefinition.domain":              inheritedUnprobed,
	"HelperDefinition.id":                  inheritedUnprobed,
	"HelperResponse.domain":                inheritedUnprobed,
	"HelperResponse.id":                    inheritedUnprobed,
	"IntegrationManifest.domain":           inheritedUnprobed,
	"LabelEntry.label_id":                  inheritedUnprobed,
	"LogEntry.level":                       inheritedUnprobed,
	"LogEntry.name":                        inheritedUnprobed,
	"LovelaceDashboard.id":                 inheritedUnprobed,
	"LovelaceDashboard.url_path":           inheritedUnprobed,
	"LovelaceResource.id":                  inheritedUnprobed,
	"LovelaceResource.url":                 inheritedUnprobed,
	"RefChange.after":                      inheritedUnprobed,
	"RefChange.before":                     inheritedUnprobed,
	"RefChange.location":                   inheritedUnprobed,
	"RefEntity.location":                   inheritedUnprobed,
	"RefEntity.matched_value":              inheritedUnprobed,
	"RefReplaceResponse.status":            inheritedUnprobed,
	"RefScanHit.location":                  inheritedUnprobed,
	"RefScanHit.matched_value":             inheritedUnprobed,
	"RefScanResponse.target":               inheritedUnprobed,
	"RelatedEntityEntry.entity_id":         inheritedUnprobed,
	"RelatedEntityEntry.relationship":      inheritedUnprobed,
	"RelatedEntityResponse.entity_id":      inheritedUnprobed,
	"SchemaField.name":                     inheritedUnprobed,
	"ScriptCreateResponse.id":              inheritedUnprobed,
	"ScriptCreateResponse.status":          inheritedUnprobed,
	"ScriptDefinition.id":                  inheritedUnprobed,
	"ScriptResponse.id":                    inheritedUnprobed,
	"ServiceDomain.domain":                 inheritedUnprobed,
	"ServiceStateChange.entity_id":         inheritedUnprobed,
	"SkippedFile.location":                 inheritedUnprobed,
	"SkippedFile.reason":                   inheritedUnprobed,
	"StaleRef.location":                    inheritedUnprobed,
	"StaleRef.matched_value":               inheritedUnprobed,
	"StatusResponse.version":               inheritedUnprobed,
	"SystemLogEntry.level":                 inheritedUnprobed,
	"SystemLogEntry.name":                  inheritedUnprobed,
	"TemplateCreateResponse.status":        inheritedUnprobed,
	"TemplateCreateResponse.unique_id":     inheritedUnprobed,
	"TemplateDefinition.domain":            inheritedUnprobed,
	"TemplateResponse.unique_id":           inheritedUnprobed,
	"TraceSummary.domain":                  inheritedUnprobed,
	"TraceSummary.item_id":                 inheritedUnprobed,
	"TraceSummary.run_id":                  inheritedUnprobed,
	"UserEntry.id":                         inheritedUnprobed,
	"ValidateResult.error":                 inheritedUnprobed,
	"WireGuardActionResponse.status":       inheritedUnprobed,
	"WireGuardActionResponse.tunnel":       inheritedUnprobed,
	"WireGuardPeer.public_key":             inheritedUnprobed,
	"WireGuardStatusResponse.tunnel":       inheritedUnprobed,
	"WiringResponse.domain":                inheritedUnprobed,
	"addonEntry.slug":                      inheritedUnprobed,
	"addonInfo.slug":                       inheritedUnprobed,
	"configEntry.domain":                   inheritedUnprobed,
	"configEntry.entry_id":                 inheritedUnprobed,
	"haConfig.version":                     inheritedUnprobed,
	"haIssue.domain":                       inheritedUnprobed,
	"haIssue.issue_id":                     inheritedUnprobed,
	"logbookEntry.when":                    inheritedUnprobed,

	// ---- probed: each cites what was asked of a live instance, and when ----
	"entityState.entity_id": "probed 2026-08-01: /api/states on the reference instance returned 4488 " +
		"records, entity_id non-empty on every one",
	"statesEnvelope.entity_id": "probed 2026-08-01: the same /api/states payload entityState decodes; " +
		"4488 records, entity_id non-empty on every one",
	"automationEntity.entity_id": "probed 2026-08-01: the same /api/states payload; 4488 records, " +
		"entity_id non-empty on every one",
	"scriptEntity.entity_id": "probed 2026-08-01: the same /api/states payload; 4488 records, " +
		"entity_id non-empty on every one",
	"historyEntry.last_changed": "probed 2026-08-01: 407 history records over 400 days all carried " +
		"last_changed and none was empty — the same payload whose state was empty 62 times (finding #38)",
	"historyEntryFull.last_changed": "probed 2026-08-01: as historyEntry.last_changed, same payload " +
		"and window",
	"WireGuardStatusResponse.state": "contract: the companion spec declares state as an enum of " +
		"active|inactive, so empty is not a value the route can emit",
}

// identityFields derives every field name declared inside an Identity method,
// keyed Type.field. Source-derived like everything else in this sweep: adding a
// field to an Identity fails the gate below until its grounds are written down.
func identityFields(files map[string]*ast.File) map[string]bool {
	got := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Identity" || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			t := fn.Recv.List[0].Type
			if star, isStar := t.(*ast.StarExpr); isStar {
				t = star.X
			}
			id, isIdent := t.(*ast.Ident)
			if !isIdent {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				kv, isKV := n.(*ast.KeyValueExpr)
				if !isKV {
					return true
				}
				key, isKey := kv.Key.(*ast.Ident)
				if !isKey || key.Name != "Name" {
					return true
				}
				if lit, isLit := kv.Value.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
					got[id.Name+"."+strings.Trim(lit.Value, `"`)] = true
				}
				return true
			})
		}
	}
	return got
}

// TestSweep_EveryIdentityFieldStatesItsGrounds is the field-level H-14 gate.
// Every field any Identity declares must appear in identityFieldEvidence, and
// every entry there must still correspond to a declared field — so the ledger
// can neither miss a new field nor keep a stale one.
func TestSweep_EveryIdentityFieldStatesItsGrounds(t *testing.T) {
	root := repoRoot(t)
	declared := map[string]bool{}
	for _, pkg := range degeneracy.WirePackages {
		for k := range identityFields(parsePackage(t, root, pkg)) {
			declared[k] = true
		}
	}

	var undeclared []string
	for field := range declared {
		if reason, ok := identityFieldEvidence[field]; !ok || strings.TrimSpace(reason) == "" {
			undeclared = append(undeclared, field)
		}
	}
	sort.Strings(undeclared)
	for _, field := range undeclared {
		t.Errorf("identity field %s states no grounds — add a row to identityFieldEvidence saying why "+
			"an empty value there cannot be an answer Home Assistant legitimately sends. If nobody has "+
			"asked a live instance, say inheritedUnprobed rather than inventing a reason.", field)
	}

	var stale []string
	for field := range identityFieldEvidence {
		if !declared[field] {
			stale = append(stale, field)
		}
	}
	sort.Strings(stale)
	for _, field := range stale {
		t.Errorf("identityFieldEvidence carries %s, which no Identity declares any more — delete the row", field)
	}
}

// TestSweep_UnprobedIdentityFieldsOnlyShrink ratchets the debt down. An
// identity field whose premise nobody checked is how finding #38 shipped; the
// count may fall as fields get probed and may never rise.
func TestSweep_UnprobedIdentityFieldsOnlyShrink(t *testing.T) {
	unprobed := 0
	for _, reason := range identityFieldEvidence {
		if reason == inheritedUnprobed {
			unprobed++
		}
	}
	if unprobed > unprobedIdentityFieldBaseline {
		t.Errorf("%d identity fields are unprobed, baseline is %d — a new field must state real grounds, "+
			"not inherit the debt", unprobed, unprobedIdentityFieldBaseline)
	}
	if unprobed < unprobedIdentityFieldBaseline {
		t.Errorf("%d identity fields are unprobed, below the baseline of %d — lower "+
			"unprobedIdentityFieldBaseline to %d so the ratchet holds", unprobed, unprobedIdentityFieldBaseline, unprobed)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		t.Fatalf("repo root %s has no go.mod: %v", root, statErr)
	}
	return root
}

// parsePackage returns the non-test files of one package, parsed.
func parsePackage(t *testing.T, root, pkg string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	dir := filepath.Join(root, pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", pkg, err)
	}
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if parseErr != nil {
			t.Fatalf("parsing %s/%s: %v", pkg, name, parseErr)
		}
		files[pkg+"/"+name] = f
	}
	return files
}

// jsonTaggedStructs returns the names of the json-tagged structs declared in
// one parsed file — the set this sweep requires a decision about.
func jsonTaggedStructs(f *ast.File) []string {
	var names []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, isType := spec.(*ast.TypeSpec)
			if !isType {
				continue
			}
			if st, isStruct := ts.Type.(*ast.StructType); isStruct && hasJSONTag(st) {
				names = append(names, ts.Name.Name)
			}
		}
	}
	return names
}

// hasJSONTag reports whether a struct declares at least one `json:"..."` tag,
// which is what makes it a wire shape rather than an internal record.
func hasJSONTag(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if f.Tag != nil && strings.Contains(f.Tag.Value, `json:"`) {
			return true
		}
	}
	return false
}

// identityReceivers returns the type names that declare an Identity method.
func identityReceivers(files map[string]*ast.File) map[string]bool {
	got := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Identity" || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			t := fn.Recv.List[0].Type
			if star, isStar := t.(*ast.StarExpr); isStar {
				t = star.X
			}
			if id, isIdent := t.(*ast.Ident); isIdent {
				got[id.Name] = true
			}
		}
	}
	return got
}

// TestSweep_EveryWireStructIsClassified is the H-14 classification gate. Every
// json-tagged struct in a wire-facing package must either declare an Identity
// or say, in unidentifiedWireStructs, why its zero value is a legitimate
// answer. Neither direction is safe to leave implicit: an unclassified struct
// that renders a zero decode re-creates D1, and one poisoned by reflex makes
// the suite cry wolf on correct behaviour.
func TestSweep_EveryWireStructIsClassified(t *testing.T) {
	root := repoRoot(t)
	accountedFor := map[string]bool{}
	var unclassified []string

	for _, pkg := range degeneracy.WirePackages {
		files := parsePackage(t, root, pkg)
		identified := identityReceivers(files)

		for path, f := range files {
			for _, name := range jsonTaggedStructs(f) {
				switch {
				case identified[name]:
					// Classified as identified.
				case exempt(unidentifiedWireStructs, name, pkg, accountedFor):
					// Classified as legitimately zero-valued.
				default:
					unclassified = append(unclassified, path+": "+name)
				}
			}
		}
	}

	sort.Strings(unclassified)
	for _, u := range unclassified {
		t.Errorf("wire struct %s is neither identified nor exempted — decide whether a zero "+
			"decode of it is a legitimate answer, then add an Identity method or an entry in "+
			"unidentifiedWireStructs with the reason", u)
	}

	for name := range unidentifiedWireStructs {
		if !accountedFor[name] {
			t.Errorf("unidentifiedWireStructs has a stale entry %q: no json-tagged struct of that "+
				"name exists, or it now declares an Identity", name)
		}
	}
}

// exempt looks the struct up by bare name, then by "Name@package" for the two
// packages that both declare a LogEntry.
func exempt(table map[string]string, name, pkg string, accountedFor map[string]bool) bool {
	if _, ok := table[name+"@"+pkg]; ok {
		accountedFor[name+"@"+pkg] = true
		return true
	}
	if _, ok := table[name]; ok {
		accountedFor[name] = true
		return true
	}
	return false
}

// TestSweep_EveryDecodeSiteIsChecked is the other half: a struct can declare an
// identity and still be decoded somewhere that never checks it, which is a
// silent hole exactly the size of the original defect. Every json.Unmarshal in
// a wire-facing package must sit in a function that also calls
// degeneracy.Check, or be listed with a reason.
func TestSweep_EveryDecodeSiteIsChecked(t *testing.T) {
	root := repoRoot(t)
	accountedFor := map[string]bool{}
	var unchecked []string

	for _, pkg := range degeneracy.WirePackages {
		files := parsePackage(t, root, pkg)
		for path, f := range files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				targets, checked := scanFunc(fn)
				if checked {
					continue
				}
				for _, target := range targets {
					key := path + ":" + fn.Name.Name + ":" + target
					if _, ok := uncheckedDecodeSites[key]; ok {
						accountedFor[key] = true
						continue
					}
					unchecked = append(unchecked, key)
				}
			}
		}
	}

	sort.Strings(unchecked)
	for _, u := range unchecked {
		t.Errorf("decode site %s unmarshals a payload without a degeneracy.Check — add one, or "+
			"record in uncheckedDecodeSites why this decode cannot render a zero value as an "+
			"answer", u)
	}

	for key := range uncheckedDecodeSites {
		if !accountedFor[key] {
			t.Errorf("uncheckedDecodeSites has a stale entry %q: that function no longer "+
				"unmarshals into that variable", key)
		}
	}
}

// scanFunc returns the names unmarshalled into by fn, and whether fn calls
// degeneracy.Check anywhere. Function-level granularity is deliberate: the
// check must be reachable from the decode, and a decode helper that hands its
// result straight back is the shape every one of these has.
func scanFunc(fn *ast.FuncDecl) (targets []string, checked bool) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case pkgIdent.Name == "degeneracy" && sel.Sel.Name == "Check":
			checked = true
		case pkgIdent.Name == "json" && sel.Sel.Name == "Unmarshal" && len(call.Args) == 2:
			if name := targetName(call.Args[1]); name != "" {
				targets = append(targets, name)
			}
		}
		return true
	})
	return targets, checked
}

// targetName extracts the variable a decode writes into, unwrapping the &x that
// every json.Unmarshal call takes. Selector targets (`&rt.Trace`) are named in
// full rather than skipped: a decode straight into a struct field is a decode
// site like any other, and silently dropping the ones that are inconvenient to
// name is how an enumeration comes to look complete while it is not.
func targetName(arg ast.Expr) string {
	if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		arg = unary.X
	}
	switch e := arg.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if base := targetName(e.X); base != "" {
			return base + "." + e.Sel.Name
		}
	}
	return ""
}
