# Invariants

Cross-cutting rules the CLI must satisfy regardless of which command grows
next. Server-side counterparts (auth on every route, dry-run defaults,
backup-before-mutate) live in the hactl-companion repo's `INVARIANTS.md`.

**Discipline:** a rule without an enforcing test does not get added here.
When behavior changes intentionally, the test and this file change in the
same PR.

## H-1 — Non-idempotent requests are never auto-retried

A `POST` is retried only when the request provably never left the client
(dial/connection-refused class). A 5xx or lost response means the server may
have acted, so only idempotent methods (GET/HEAD/PUT/DELETE/OPTIONS) retry —
a create is never silently duplicated. A signed 401 (expired Ingress session)
is safe to retry for any method: the server rejected before acting.

- Enforced by: `internal/companion/client_retry_test.go`
  (`shouldRetry` truth table, `TestPostNotRetriedOn5xx`, `TestGetRetriedOn5xx`)

## H-2 — Mutating commands are dry-run by default

Every command family that writes (config, dash, ref, …) reports what it
*would* do unless `--confirm` is given, and the companion request carries
`dry_run=true` accordingly. The first `--confirm` of a write family in a
session is refused with the family how-to, so an uninformed apply cannot be
the first thing that executes.

**A preview fails exactly where the confirmed run would.** The target is
resolved, and the input file parsed, *before* the plan is printed. Thirteen
write commands used to accept a fabricated id and print a confident
"would delete X" at exit 0 while `--confirm` failed on the same argument;
`script create`/`helper create` reported a file's size without ever reading
it, so an unusable file previewed happily. The manual tells agents to stop at
the first miss, which turned a typo into a verified plan. Where the confirmed
run accepts more identifiers than a naive lookup does, the preview must accept
them too: `auto delete` takes a config id, an alias or a live entity_id, so
its check is "the companion has the definition **or** HA has the entity" — a
stricter dry run is the same dishonesty pointing the other way.

**A preview is machine-readable.** `--json` used to be a byte-for-byte no-op
on nearly every preview, so an agent that asked for JSON got prose. Previews
share one shape (`internal/cmd/dryrun.go`) that renders as text or as an
object stating `"dry_run": true` — a caller must be able to tell a plan from a
result by looking at the answer, not by remembering which flags it passed.

- Enforced by: `internal/cmd/ref_test.go` (dry-run default asserted against a
  stubbed companion), `internal/cmd/confirm_guard_test.go` (first-write
  refusal + informed retry), `internal/cmd/config_delete_test.go`
  (`TestConfigDeleteDryRunRefusesUnknownEntry`),
  `internal/cmd/create_delete_test.go` (`TestRegistryDeleteDryRunResolvesTarget`,
  `TestCreateDryRunRejectsUnusableInput`, `TestPreviewJSONIsMachineReadable`,
  the four `…DryRunRefusesUnresolvable` cases),
  `internal/integration/dash_test.go`
  (`TestDashDeleteAgreesOnUnknownDashboard`),
  `internal/companiontest/write_config_test.go`
  (`TestE2EDryRunRejectsFabricatedTargetCLI`,
  `TestE2ECreateDryRunValidatesInputCLI`,
  `TestE2EConfirmedRunRejectsWhatDryRunRejectsCLI`)

## H-3 — The vendored companion contract must not drift

`testdata/companion-v1.yaml` is a verbatim copy of the companion's generated
spec (`make sync-spec` to refresh); the CLI is coded against it. Contract
tests run the real CLI against a companion built from source, so an
incompatible companion change fails before release.

- Enforced by: `make check-spec-drift` (byte-level diff against
  `../hactl-companion`), `internal/companiontest/contract_test.go`
  (CI "Companion Tests" job, companion built from `main`). The
  "Companion Tests" CI job now runs `make check-spec-drift
  COMPANION_DIR=companion-src` right after checking out
  `hactl-companion`, so a drifted vendored spec fails CI before it can
  reach a release.

## H-4 — An automation write is verified against HA, not against hactl's output

`auto apply` and `auto rollback` are proven by reading the config back from HA
and comparing it, never by asserting on the CLI's echo. `applied: <id>` is
printed unconditionally once the write call returns nil, so an assertion on it
holds whether or not anything reached HA.

This exists because stubbing `haapi.Client.UpdateAutomationConfig` to
`return nil` — discarding every automation write — left both the unit tier and
the whole integration package green. The prior test asserted only that the
automation still existed after each step, which is true either way.

Comparison folds HA's legacy singular keys (`trigger`/`condition`/`action`,
and `service` within a step) onto the modern plural ones, because writing
through the Config API migrates the schema; everything else must match exactly.

- Enforced by: `internal/integration/write_roundtrip_test.go`
  (`TestAutoApplyRollbackRoundTrip`, `make test-int`)

## H-5 — No automation write without a successful backup

`Writer.Apply` returns an error rather than writing when the backup fails.
Without the backup the previous config is unrecoverable, so `auto rollback`
would have nothing to restore; warning and writing anyway traded the user's
only undo for a log line that `HACTL_LOG_LEVEL` routinely hides.

- Enforced by: `internal/writer/writer_test.go`
  (`TestWriter_Apply_BackupFailureAborts`)

## H-6 — A backup belongs to exactly one automation

Backup selection matches the whole id after the timestamp, never a trailing
underscore-delimited segment of it. Segment matching made
`auto rollback door` select `bathroom_light_on_door`'s backup and write that
config back under the id the user asked for — one automation's config restored
over another's. Underscore-suffixed ids are ordinary in real HA configs.

- Enforced by: `internal/writer/writer_test.go` (`TestContainsAutoID`, the
  collision cases)

## H-7 — A decode that yields nothing never renders as success

When a wire decode produces an all-zero value, the result is reported as
`UNPARSED`, not as a passing outcome. `overallResult` returns `StepUnknown`
when both `script_execution` and `state` are empty, and `Condense` forces it
when the decode carries no identity and no steps at all; an empty domain and
item_id render as nothing rather than a bare `.`.

Empty was previously spelled "success": a `trace/get` struct whose tags did not
match HA's wire shape unmarshalled to zero, and every automation run — including
failures — rendered as `  .    PASS` for months while the whole suite stayed
green. The marker is also scanned for by the integration harness itself, so
every command a test runs is checked for it, including tests that assert
nothing of their own.

- Enforced by: `internal/analyze/trace_unparsed_test.go`
  (`TestOverallResult_EmptyIsNotPass`, `TestCondense_EmptyDecodeIsUnknown`,
  `TestFormatCondensed_UnparsedNeverLooksLikePass`),
  `internal/integration/degeneracy_test.go` (`looksDegenerate`, wired into
  `runHactl`/`runHactlDir`/`runHactlErr`/`runHactlDirErr`)

## H-8 — An entity's effective area includes the one it inherits from its device

An entity's effective AREA is `entity.area_id` if set, else
`device_registry[entity.device_id].area_id` — the entity's own area always
wins when set, but falls back to its device's area when it isn't. This
mirrors HA's own rule exactly (`homeassistant/helpers/template/extensions/
areas.py`, `AreaExtension.area_name`/`area_entities`, verified against running
HA 2026.7.2 source): placing the DEVICE in a room is the normal HA pattern,
entity-level area assignment is the exception, so reading only the entity's
own `area_id` (as every call site here used to) makes most real entities in
a device-centric area invisible to `--area` filtering, blank in `area:`
output, and absent from area-based relation-finding.

LABELS do **not** follow the same rule, even though they look like they
should. Confirmed against the same HA source
(`homeassistant/helpers/template/extensions/labels.py`): `label_entities()`
resolves via `entity_registry.async_entries_for_label` with no device (or
area) expansion at all — a label attached only to a device is invisible to
`label_entities()`, even though `label_devices()` finds the device carrying
it. So `registryContext.labelNames` deliberately keeps reading only the
entity's own `labels` field; giving it the same device fallback as area
would make hactl disagree with HA itself.

This was four independent read sites making the same area mistake —
`registryContext.areaName` (label.go; shared by `ent ls`, `ent show`,
`auto ls`, `script ls`), `findAreaNeighbors` (ent.go's `ent related`, which
read `entity.area_id` inline and bypassed the shared helper entirely), and
`registryEntityAreaName` (device.go's `device show` entity table, a separate
hand-rolled copy of the same missing fallback) — plus `filterEntitiesByArea`,
which inherits the fix for free by calling `registryContext.areaName`.

Two lower-severity read-surface defects surfaced by the same investigation
and fixed alongside it, in the same files:

- `ent ls --label`/`device ls --label` used to disagree with their own
  existence pre-check (`labelExistsInRegistry` matched a label id/name
  exactly; the actual filter matched by substring on the entity's *joined*
  "name1, name2" display string — which could also false-positive across the
  ", " separator between two unrelated labels). Both now resolve to a
  `map[string]bool` of matching label ids via `matchingLabelIDs` (substring on
  id or name, per-label — not per joined string) and agree with each other;
  substring semantics are kept because docs/manual.md documents them for
  every `--label`-supporting command and `auto ls`/`script ls` (outside this
  fix's scope) already implement them that way.
- `device ls --pattern` lowercased both sides before matching while
  `ent ls --pattern` didn't, making `device ls` the sole case-insensitive
  outlier among the commands docs/manual.md documents as case-sensitive
  substring/glob. `deviceMatchesPattern` now matches case-sensitively too.
- `ent show --json` encoded only the raw `/api/states` struct, omitting
  `name`/`unit`/`area`/`labels`/`changed_by` even though the human table
  right below it computes and prints all five; `--json` now carries the same
  fields.
- `ent hist`/`ent anomalies`/`ent related` printed a human summary line
  ("`<id>: N points`" etc.) before the JSON table body even under `--json`,
  so piping their `--json` output through a strict decoder failed. All three
  (plus their non-numeric-entity fallback paths, `renderStateTimeline`/
  `renderStateAnomalies`/`renderStaleRefs`) now suppress that line when
  `flagJSON` is set.

- Enforced by: `internal/integration/oracle_inheritance_test.go`
  (`TestEntLsAreaMatchesOracleInheritance`,
  `TestEntLsLabelMatchesOracleInheritance`,
  `TestEntShowOverrideAreaViaDeviceEntities`, `TestEntShowInheritedAreaLine`,
  `TestEntRelatedAreaNeighborsUseInheritedArea`,
  `TestDeviceShowEntitiesShowInheritedArea`, `TestEntLsAreaNegativeControl`,
  `TestEntShowJSONIncludesTableFields`, `TestEntHistJSONParsesStrictly`,
  `TestEntAnomaliesJSONParsesStrictly`, `TestEntRelatedJSONParsesStrictly` —
  all against `oracleAreaEntities`/`oracleLabelEntities`/`oracleEntityArea`,
  HA's own `area_entities()`/`label_entities()`/`area_name()`, invariant H-9),
  `internal/cmd/ent_test.go` (`TestRegistryContext_AreaName_DeviceFallback`,
  `TestRegistryContext_AreaName_OwnAreaWinsOverDevice`,
  `TestRegistryContext_AreaName_NoDeviceStaysEmpty`,
  `TestRegistryContext_LabelNames_NoDeviceFallback`,
  `TestFilterEntitiesByArea_DeviceFallback`,
  `TestFindAreaNeighbors_UsesDeviceFallback`,
  `TestFilterEntitiesByLabel_MatchesPerLabelNotJoinedString`,
  `TestLabelExistsInRegistry_AgreesWithFilter`,
  `TestRunEntShow_JSON_IncludesTableFields`,
  `TestRunEntHist_JSON_NoHeaderLine`, `TestRunEntAnomalies_JSON_NoHeaderLine`,
  `TestRunEntRelated_JSON_NoHeaderLine`),
  `internal/cmd/device_test.go` (`TestDeviceMatchesPattern_CaseSensitive`,
  `TestDeviceHasLabel_SubstringMatchesEnt`,
  `TestRegistryEntityAreaName_DeviceFallback`)

## H-9 — Home Assistant's own identifier is the lookup key, and HA is the oracle

A resource is addressed by the identifier HA itself keys it under, never by one
derived from a different field that happens to look similar. Concretely:
automation **traces** are keyed by the automation's config `id:` (surfaced as
`attributes.id`), while the **logbook** is keyed by `entity_id` — HA derives
`entity_id` from the alias, so the two are independent strings. Using one where
the other belongs fails silently, because a map lookup on the wrong key returns
a zero value rather than an error.

This is not an edge case: HA's UI automation editor assigns a millisecond
timestamp as the config id and derives the entity_id from the alias, so the two
differ for essentially every UI-authored automation. `auto ls --failing`,
`auto ls`'s `errors`/`last_err`, `auto show`'s trace table, and every `trc:`
stable ID were all silently empty for those automations.

The testing half of the rule: for any read HA can answer itself, the expected
value is computed from HA at test time — `area_entities()`, `label_entities()`,
`trace/list`, `manifest/list`, `system_log/list` — never hardcoded and never
golden-filed. A hand-written expectation is written by whoever wrote the
implementation and repeats its modelling mistake; HA's own resolver cannot.
The fixture must also make identifiers *distinguishable* (config id never equal
to the slug of the alias) and must actually *exercise* them: the pre-existing
fixtures already contained divergent automation ids, but nothing ever fired
those automations, so no trace existed and the divergence was inert.

- Enforced by: `internal/cmd/auto_test.go` (`TestAutomationTraceKey`,
  `TestBuildAutoRows_ErrorsWhenConfigIDDiffers`),
  `internal/integration/oracle_identity_test.go`
  (`TestOracleFixtureIsDistinguishing`, `TestOracleFixtureIsExercised`,
  `TestAutoShowFindsTracesForDivergentConfigID`, `TestAutoLsFailingMatchesHA`,
  `TestAutoLsErrorCountsMatchHA`, `TestTraceShowResolvesByEntityIDForm`,
  `TestScriptTracesStillWork` as the no-split control),
  and the oracle harness in `internal/integration/oracle_test.go`

## H-10 — `--json` is a machine contract: it parses, it is complete, and it is never silently truncated

Every read command's `--json` output must (1) parse strictly as JSON with
nothing else on stdout, (2) never shrink because of `--top` — `--top` caps
rows in **text** tables only, and (3) never be preceded by a human header
line. All three were violated at once: `format.Table.visibleRows` applied
`--top` identically to JSON and text, so `hactl ent ls --json` silently
returned 10 of 179 entities as a bare array with no truncation marker —
`--stats` even reported it as comfortably under the token cap, so nothing
signalled the loss. Separately, seven read commands (`auto show`,
`script show`, `trace show`, `cc show`, `log show`, `version`, root
`--help`) never checked `flagJSON` at all and printed plain text with exit 0.
Root `--help` had a third, distinct failure mode: cobra's help writer went
through the same `--tokensmax` cap as normal output, so `hactl --help` was
cut off mid-word.

This matters most over MCP, where `--json` is the machine interface and a
silently-short or silently-ignored `--json` reads as a complete, valid
answer.

Fix shape: `format.Table.visibleRows` now returns every row whenever
`opts.JSON` is set, full stop — `--top` has no code path into JSON output at
all, so no per-command opt-out can regress it. `hactl --help` (and every
path that renders cobra help — `-h`, a bare non-runnable command, the
built-in `help` subcommand) now goes through a wrapped `HelpFunc` that marks
the invocation as `helpRendered`, which `applyTokenPolicy` checks to skip
the cap without touching the `--json` exemption it already had. `version`
and `script show` gained a `flagJSON` branch each, encoded straight from a
struct (`versionInfo`, `scriptShowResult`) with nothing printed before it.

- Enforced by: `internal/format/format_test.go` (`TestRenderJSON_TopN` —
  inverted from asserting the truncation bug as correct to asserting `--top`
  has no effect on `--json`), `internal/cmd/json_contract_test.go`
  (`TestJSONContract`, which walks the live cobra command tree — so a newly
  added read command is covered automatically — and asserts all three
  properties on every non-mutating, non-meta, non-verbatim-by-design leaf it
  can exercise against a fake HA; `TestRootHelp_NeverTokenTruncated`,
  `TestVersionJSON_Shape`, `TestScriptShowJSON_Shape`)

## H-11 — hactl never invents an identifier, and every count it reports reconciles with the count its source reported

A listing command's rows must be a subset of what its source system can
independently confirm, and a count column must sum to the same total the
source itself reports — never a proxy for either (a truncated string that
happens to match sometimes, a record count standing in for an occurrence
count, a shared ID-registry lookup that ignores which namespace minted it).

Four read-surface commands violated this the same way — silently, because
each substituted something *plausible* for the real signal:

- `log --component` / `cc logs <name>` filtered on a logger name truncated to
  its last dot-segment (`systemLogToEntries`), so `--component automation`
  matched zero of HA's own `homeassistant.components.automation.*` entries.
  The full logger name is now kept for matching; only the rendered table
  column shortens it for display (`shortComponent`).
- `log --unique` counted how many records `DeduplicateLogs` merged into a
  group instead of summing each record's own HA-reported `count` — HA's
  `system_log/list` already pre-aggregates, so a message HA reports with
  `count=3` showed as `1`, and the "sorted by count" promise put the
  genuinely-repeating failures at the bottom.
- `ent who` / `changes` attributed a change to the propagated
  `context_user_id` even when the logbook entry also carried a specific
  `context_event_type` (`automation_triggered`/`script_started`) or
  `context_name` (a device). HA propagates the *originating* human's user id
  down the whole causal chain, so the proximate cause must win — `triggerLabel`
  now checks automation/script/device before falling back to the user id.
- `cc ls` / `cc show` treated any `update.*` entity carrying `title` +
  `installed_version` as a custom component with no `is_built_in` check, so a
  built-in integration's own update entities (e.g. `demo`) were reported as
  custom. `manifest/list`'s `is_built_in` is now the sole source that can
  nominate a domain; an `update.*` entity can only enrich a domain manifest/
  list already confirmed non-built-in, never add one.

A fifth command fabricated *fields*, not rows: `log show` resolved any ID
`pkg/ids.Registry` recognized regardless of prefix, so a `trc:` or `anom:` ID
(the latter's key shape — `entity_id|type|start_time` — coincidentally matches
a log key's own pipe-delimited 3-part shape) would resolve and print an
unrelated record's fields as if they were this entry's
timestamp/component/message. `log show` now rejects any ID without a `log:`
prefix before resolving, mirroring the same check `trace.go`'s
`resolveTraceID` already did for `trc:`.

- Enforced by: `internal/analyze/logdedup_test.go`
  (`TestDeduplicateLogs_SumsPreAggregatedCounts`,
  `TestDeduplicateLogs_ZeroCountTreatedAsOne`,
  `TestParseLogLines_CountDefaultsToOne`),
  `internal/cmd/pure_test.go` (`TestSystemLogToEntries_Basic`,
  `TestSystemLogToEntries_CountDefaultsToOneWhenHAOmitsIt`,
  `TestShortComponent`),
  `internal/cmd/whoresolve_test.go` (`TestTriggerLabel` precedence cases),
  `internal/cmd/ws_cmd_test.go` (`TestRunCCLs_ExcludesBuiltInUpdateEntities`,
  `TestRunCCShow_RejectsBuiltInDomain`, `TestRunLogShow_RejectsForeignNamespace`,
  `TestRunLogShow_JSON`, `TestRunCCShow_JSON`),
  `internal/integration/oracle_diagnostics_test.go` (all tests, checked
  against HA's own `system_log/list`, `manifest/list`, and logbook —
  invariant H-9)

## H-12 — A write is proven by reading it back from Home Assistant

Every write family is gated the way H-4 gates automations, generalised: read
the current state **from HA directly**, write via hactl with `--confirm`, read
back **from HA directly**, and compare the whole document — not just the field
the renderer happens to show. At least one assertion is on a field the command
never mentioned, as an independent witness that the whole document was written
and nothing else moved. The dry run is asserted to change nothing, and the
restore is asserted too.

Reading back through hactl does not count: then hactl both writes and verifies,
and a shared modelling mistake agrees with itself. `dash show --raw` reads
faithfully today, but a test built on it proves the pair consistent, not the
write correct.

`docs/testing.md` recorded the gap this closes: `dash save` "can each be
replaced with a stub without any test failing". Deleting the
`DashboardConfigSave` call from `runDashSave` — the exact stub named — now
fails `TestDashCreateSaveDeleteRoundTrip` at the read-back. Discarding the
registry write in `runEntSetLabel` fails `TestEntSetLabelRoundTrip`
("labels are [], want write_rt_a among them"), and a write that additionally
sets a field it was never asked to set (`name`) fails the witness comparison
even though the field it *was* asked to set is correct.

Two commands writing the same registry must also agree on the same input.
`ent set-label` planned a write for an entity that is not in the entity
registry — printing a confident "would set entity labels" plan at exit 0 —
while `ent set-area` resolved the entity first and failed. Under the manual's
stop-at-the-first-miss rule that turns a typo into a successful plan. The dry
run must fail exactly where the confirmed run would, so `set-label` now
resolves the entity first, like `set-area` always did.

**A delete leaves nothing behind.** HA keeps the entity registry entry of
anything that ever had a unique_id, so removing a definition from YAML leaves
the entity listed with `state: unavailable` and `restored: true` — a ghost
`ent ls` still shows, which silently re-adopts the id if the same unique_id
comes back. `auto delete` always cleaned this up; `script delete` and
`tpl delete` did not, so the same operation left a different amount of debris
depending on the family. All three now share `removeOrphanedEntity`.

**A write claims nothing HA did not confirm.** The companion reports whether
HA reloaded, and `tpl create`/`script create` decoded no such field, so both
printed "created …" for a definition HA may never have read. The live case:
`tpl create` writes `template.yaml` even when no `template:` key !include's
it, and the entity never appears. Both now warn when HA confirms no reload —
the same gate `auto create` and `helper create` already had (issue #40).

Write tests mutate registry state that read tests assert on, so they run
against their own HA instance (`getWriteHA`), which — like every lazily
started instance — must have a matching teardown line in
`internal/integration/main_test.go`. The companion tier's own rig had a
matching gap: HA's onboarding config !include's automations, scripts and
scenes and nothing else, so the seeded `template.yaml` was never loaded and no
`tpl` write could be proven against HA at all. `seedConfigFiles` now wires
`template:` in and restarts HA through its own `homeassistant.restart`
service — not `docker compose restart`, which re-allocates the ephemeral host
port and leaves every captured URL pointing at a dead socket.

**The config-flow family is proven end to end, not echoed.** `config
flow-start`/`flow-step`/`options`/`delete --confirm` create and remove real
config entries, but the confirmed path only echoes HA's flow response and
`delete` prints "deleted config entry" the moment the call returns nil — so an
assertion on hactl's output holds whether or not anything reached HA. This was
the last pre-H-4 stubbable mutation surface (W7): the round-trip drives
flow-start → flow-step → create_entry for a flow-capable domain (`met_eireann`,
whose single `user` step is Required-with-default so `--data {}` completes with
no network access and no credentials), reads the created entry back from HA's
own `config_entries` list, asserts its witnessed title/state, then `config
delete --confirm` and asserts the entry is gone from that list. The options flow
is proven the same way against the default `met` entry: a new `elevation` is
submitted through `config options --confirm` + `flow-step --options --confirm`,
then read straight back from a fresh options flow's HA-seeded form default.
Stubbing `StartConfigFlowOnce`/`StepFlow`/`DeleteConfigEntry`/`StartOptionsFlow`
to canned success fails these at the read-back.

**A flow preview resolves the domain the way the confirmed run does.** `config
flow-start`'s dry run validated the domain against `manifest/list` — the
integrations HA has *loaded* — so every not-yet-configured integration (the very
thing you start a flow for) was refused as "no loaded integration" while a
confirmed flow-start lazily loaded it and succeeded. The dry run failed exactly
where the confirmed run worked, the inverse of the H-2 contract, and it broke
the command's whole purpose. It now resolves against HA's `flow_handlers` list
(the authority on what `StartConfigFlow` accepts), so preview and confirm agree.

- Enforced by: `internal/integration/write_roundtrip_test.go`
  (`TestAutoApplyRollbackRoundTrip`, the original H-4 case),
  `internal/integration/write_entity_test.go` (`TestEntSetLabelRoundTrip`
  incl. merge-not-replace and label-deletion detachment,
  `TestEntSetAreaRoundTrip` incl. resolution by name and HA's own
  `area_entities()` as the oracle,
  `TestEntSetLabelAndSetAreaAgreeOnUnknownEntity`),
  `internal/integration/write_dash_test.go`
  (`TestDashCreateSaveDeleteRoundTrip`, `TestDashReplaceRoundTrip`),
  `internal/integration/write_flow_test.go`
  (`TestConfigFlowCreateDeleteRoundTrip`, `TestConfigOptionsRoundTrip`),
  `internal/integration/flow_test.go`
  (`TestConfigFlowStartDryRunPreviewsUnloadedDomain`,
  `TestConfigFlowStartRejectsUnknownDomain`),
  `internal/cmd/ws_cmd_test.go`
  (`TestRunConfigFlowStart_DryRunResolvesViaFlowHandlers`),
  `internal/haapi/client_test.go` (`TestConfigFlowHandlers`) —
  `make test-int`; and the companion-backed families in
  `internal/companiontest/write_config_test.go`
  (`TestE2EScriptWriteRoundTripCLI`, `TestE2ETplWriteRoundTripCLI`,
  `TestE2EHelperWriteRoundTripCLI`) — `make test-companion`

## H-13 — A contract is field-level: every decoded field is documented, and every documented field is decoded

Path-and-method presence is not a contract. For the companion seam, the
contract holds in both directions: every `json:` tag on a Go response struct in
`internal/companion` maps to a property the vendored spec
(`testdata/companion-v1.yaml`) documents, and every response property the spec
documents is decoded by the corresponding struct or carries an explicit,
justified `decodeIgnore` entry. Both sweeps derive from one table —
`companion.Endpoints`, which pairs each (method, path) with its decode target —
so a new client call is covered automatically and a new spec path with no entry
fails loudly.

This exists because a path-only contract cannot see a field. HA's companion
grew a `reloaded` flag; the Go create structs decoded no such field, so
`tpl create`/`script create` printed "created …" for a definition HA never
reloaded (D45). The same class hid `IntegrationManifest` decoding 4 of N
manifest fields (D68). A `(method, path)` presence check — which is all
`TestClientEndpointsInSpec`/`TestSpecPathCountMatchesClient` do — passes through
every one of these: the route is unchanged, only the body drifted, and a
wrong-shape body decodes to a zero value, not an error.

The field sweep surfaced exactly this drift on `main` and it was closed by
decoding, not ignore-listing: `reloaded`/`diff` on the shared write/delete
acknowledgement (`ConfigDeleteResponse`), `validated` on the file-write
response, and the `trigger` flag on the template list/get responses were all
documented in the spec yet decoded by no struct. The `decodeIgnore` list is
empty by design — a growing ignore list would re-hide the class it exists to
expose.

- Enforced by: `internal/companion/contract_conformance_test.go`
  (`TestGoStructTagsAreDocumented` — every Go tag is documented;
  `TestSpecResponseFieldsAreDecoded` — every documented field is decoded;
  `TestEndpointsCoverSpecPaths` — the table covers the spec both ways) —
  `make test` (unit tier, no Docker). The `(method, path)` half stays in
  `internal/companiontest/contract_test.go`, now derived from the same
  `companion.Endpoints` table.

## H-14 — A record that decoded without its identity is spelled UNPARSED, never rendered as an answer

A wrong-shape JSON payload does not produce an error in Go. It produces a zero
value, and a renderer prints a zero value as a plausible answer. Every wire
record hactl decodes therefore declares its *identity* — the field or fields
without which the record cannot be a real answer, an entity with no
`entity_id`, a manifest with no `domain` — and every decode site calls
`degeneracy.Check`. A record that arrives without its identity has that field
overwritten with the literal `UNPARSED` and fails its command, naming the wire
source. Both the poisoned value and the error text carry the marker, so the
text renderer, the `--json` renderer and the error path are covered by one
string.

The converse is half the invariant: a record whose zero value is a *legitimate*
answer must NOT declare an identity. A dashboard with no views is a real empty
dashboard; a `restored` ghost automation really has no attributes; an
automation with no `id:` is legal Home Assistant and the companion reports it
with an empty id. Poisoning those makes the suite cry wolf on correct
behaviour, and a gate that fires on correct behaviour gets deleted by the next
person who trips it. Both of those cry-wolf identities were in the first draft
of this invariant and were removed only after reading the companion routes that
emit the field.

This generalises H-7. `trace/get` decoded every automation run into an all-zero
struct because hactl read the wrong wire tags, and `overallResult` rendered
every run as `PASS` for months while sitting at 100% statement coverage (D1).
The fix at the time — render `UNPARSED`, and have every `runHactl*` helper grep
for it — turned integration tests that assert nothing about their output into
detectors for the class, but it only ever matched the marker
`analyze.FormatCondensed` emitted. Entity, registry, manifest, config-entry,
flow and every companion decode stayed exposed to the identical mechanism.
`analyze.UnparsedMarker` is now defined as `degeneracy.Marker`, so there is one
token and one scan.

- Classification enforced by: `internal/degeneracy/sweep_test.go`
  (`TestSweep_EveryWireStructIsClassified` — every json-tagged struct in
  `internal/{haapi,companion,cmd,analyze}` either declares an `Identity` or is
  listed in `unidentifiedWireStructs` with the reason its zero value is a
  legitimate answer; `TestSweep_EveryDecodeSiteIsChecked` — every
  `json.Unmarshal` in those packages sits in a function that also calls
  `degeneracy.Check`, or is listed in `uncheckedDecodeSites` with a reason).
  Both tables are derived from the source and fail on a *stale* entry as well as
  a missing one, so the classification cannot rot silently and an anonymous
  decode target — which can never declare an identity — cannot ship unnoticed.
- Semantics pinned by: `internal/degeneracy/check_test.go`
  (`TestCheck_PoisonsMissingIdentityAndErrors`,
  `TestCheck_EmptyAndNilAreLegitimateAnswers`,
  `TestCheck_ConditionalIdentityOnlyAppliesWhenItClaimsFailure`,
  `TestCheck_ReachesRecordsThroughSlicesMapsAndPointers`,
  `TestCheck_DetectsThroughANonPointerValue`,
  `TestCheck_TerminatesOnASelfReferentialType`).
- Scanned in every test that runs a command by
  `internal/integration/degeneracy_test.go` (`assertNoDegenerateOutput`, called
  by all four `runHactl*` helpers on stdout *and* on the returned error) and by
  `internal/companiontest/degeneracy_test.go` (`assertNoDegenerateE2EOutput`,
  called by `runHactlE2E`). The predicates are proved against real
  `degeneracy.Check` output rather than a hand-typed token
  (`TestLooksDegenerate_FlagsPoisonedDecode`,
  `TestLooksDegenerateE2E_FlagsPoisonedDecode`), and against legitimate empty
  output for the no-false-positive half.
- Marker/renderer lockstep for the trace half stays with H-7
  (`internal/analyze/trace_unparsed_test.go:TestUnparsedMarkerMatchesRendering`).
- Tier: `make test` for the sweeps and the semantics; `make test-int`,
  `make test-companion` and `make test-int-discovery` for the scans, which are
  the only tiers where a real server produces the payload.

Scope, stated honestly. All 56 identity-declaring records were mutation-swept
(each identity tag renamed to a name the wire never sends; see
`audits-2026-07-25/t6-report.md` for the table). 51 of 56 are detected by the
unit tier. The remaining five — `SystemLogEntry`, `FlowResult`, `SchemaField`,
`addonEntry`, `addonInfo` — have no hand-written JSON fixture, so the unit tier
is *structurally* blind to a tag rename there: a fixture built by marshalling
the same Go struct moves with the mutation and round-trips unchanged. Those
five are arbitrated by a real-wire tier instead. Note also that for ten
companion records the unit-tier detection comes from H-13's static
struct↔spec sweep rather than from this poison; H-13 and H-14 cover the
companion seam together, and only H-13 can see a drift that never reaches a
decode.
## H-15 — A detector is proven against history containing what it looks for, never against an empty answer

`ent anomalies` and long-window `ent hist` are only meaningful over a span long
enough to contain the behaviour they report. Every test of them must therefore
run against recorded history that is **known in advance to contain** a gap, a
stuck run and a spike, and must run against a well-behaved control entity that
contains none of the three. An empty result is a valid answer only about the
control, and only when the size of the series it is empty about has been read
back from Home Assistant in the same test.

This exists because the opposite was true for the entire life of the commands.
The only coverage `ent anomalies` had was `TestEntAnomaliesJSONParsesStrictly`,
which accepted `[]` as success — so a detector that found nothing and a detector
that was broken produced the same green (mechanisms M1 and M3 in one assertion).
The cause was fixture-shaped rather than lazy: HA has no API that can write
past-dated history, a freshly booted test container holds minutes of it, and no
gap, stuck run or spike can exist inside minutes. Nothing could be asserted, so
nothing was.

`hatest.Instance.Backfill` removes the excuse. It stops the container, writes the
rows HA's own recorder would have written straight into `home-assistant_v2.db`,
and restarts it, so the history is authored by the test and served by HA through
its ordinary APIs. Three properties keep that rig from becoming a
fixture-fiction generator, and each is enforced rather than documented:

- **It refuses to write into a recorder schema it was not verified against.**
  `supportedRecorderSchema` is a tripwire, not a formality: writing a stale row
  shape into a future HA would manufacture plausible-but-wrong history, which is
  the exact disease the harness treats. The image in use today reports schema 53.
- **Its rows are reconciled against HA, not against their author.** Every
  backfilled series is read back through HA's own history API over plain HTTP
  with hactl nowhere in the path, and the `last_changed_ts` convention that
  governs HA's significant-changes filter is pinned by writing twelve
  attributes-only updates and requiring HA to return exactly one.
- **The fixture is checked for still being distinguishing before the container
  starts.** A changed cadence or a value collision that silently removed the
  anomaly from the input fails loudly instead of turning the suite green for the
  wrong reason.

The anomaly expectations themselves are hand-stated, which is legitimate here for
one reason and only that reason: the input was authored by the test and HA has
confirmed it holds exactly that input. What is deliberately never used as an
expected value is anything computed by re-running hactl's own gap / stuck /
z-score logic over the series — that would confirm the detector against itself.
The `ent hist` expectations have no such licence and are computed from HA's raw
series at test time (count, min, max, mean, span).

- Enforced by: `internal/integration/backfill_test.go`, `TestRecorderBackfill` —
  `make test-int` (Docker tier). Subtests: `rig_lands_in_has_own_history` (the
  rig writes what HA reads back, and HA's attribute-only filter still behaves as
  the row shape assumes), `anomalies_finds_injected_gap`,
  `anomalies_finds_injected_stuck_run`, `anomalies_finds_injected_spike` (each
  pins the anomaly's position and duration, not merely its presence),
  `anomalies_negative_control_is_quiet` (a detector that flags everything fails
  here), `hist_long_window_buckets` and `hist_long_window_drops_empty_buckets`
  (bucket averaging, and empty buckets omitted rather than rendered as a zero
  reading the recorder never held). The rig itself is
  `internal/hatest/recorder.go`.

## H-17 — An identifier hactl prints is an identifier hactl accepts

Whatever hactl displays as a name for a resource, every hactl command that
filters or resolves that resource must match it. The rule is one-directional and
absolute: printing is a promise. A caller who copies a string out of one command
and pastes it into another is doing the thing the output invited, and the second
command answering "nothing" is hactl contradicting itself.

Home Assistant carries three interchangeable names for one automation: the
config `id:` (surfaced as `attributes.id`, and the key HA files traces under),
the `entity_id` it derives from the alias, and that entity_id's object id. HA's
UI mints a millisecond timestamp for the config id, so for essentially every
UI-authored automation it is a *completely different string* from the object id
`auto ls` prints in its `id` column.

hactl printed all three and resolved all three — `auto show` displays the
entity_id and the `config_id`, `auto create` prints the config id it just wrote,
and `auto cat`/`diff`/`apply`/`delete` key on it — while `auto ls --pattern` and
`ent ls --pattern` matched only the entity_id forms. So an id hactl printed was
an id hactl refused (D6/R2). The consequence is worse than an inconvenience: the
manual routes a caller who cannot find something to `ent ls --pattern` as the
discovery fallback, and under the stop-at-the-first-miss rule an empty listing
there reads as "no such entity" — a wrong answer, not a missing one.

Two bounds keep the rule from degrading into "match anything":

- **Only identifiers hactl actually prints for that resource count.** The
  automation config id is claimed because hactl prints it; a `sensor` that
  happens to carry an `id` attribute is not addressable by it, because nothing
  in hactl ever offers it as that sensor's name.
- **A resource that matches on two of its identifiers is still one row.** The
  filter widens what matches, never how often.

- Enforced by: `internal/cmd/auto_test.go` —
  `TestFilterAutosByPattern_AcceptsTheConfigIDHactlPrints`,
  `TestFilterAutosByPattern_MatchesEachAutomationOnce`,
  `TestBuildAutoRows_CarriesTheConfigID`; `internal/cmd/ent_test.go` —
  `TestFilterEntitiesByPattern_AcceptsTheConfigIDHactlPrints` (its
  `sensor.thermostat` row is the scope bound),
  `TestFilterEntitiesByPattern_MatchesEachEntityOnce`;
  `internal/integration/oracle_pattern_test.go` —
  `TestPatternAcceptsEveryIdentifierHactlPrints` (reads the entity_id/config id
  pairs from the live instance's `/api/states`, asserts `auto show` prints the
  config id HA reports, then requires `auto ls --pattern` and `ent ls --pattern`
  to match on every one of the three forms) and
  `TestPatternStillRejectsWhatDoesNotExist` (the negative control — a filter that
  matched everything would satisfy the first test just as well). `make test-int`
  (Docker tier).

## H-18 — `runs_24h` counts runs, and it counts the same runs `auto show` lists

A **run** is a trigger whose conditions passed: the automation entered its
actions. An errored run is still a run — the `errors` column reports it, not the
absence of a count. A trigger the conditions blocked is not a run, and never
appears in the number, whichever source produced it.

The definition is Home Assistant's own, not hactl's. HA fires
`EVENT_AUTOMATION_TRIGGERED` — the logbook's only record of an automation —
after the conditions have been evaluated and before the action script starts, so
a condition-blocked trigger is traced (`script_execution: failed_conditions`) but
never logged. Two consequences are load-bearing:

- **A logbook that answered and holds no entry for an automation is a zero, not a
  blank.** Conflating "answered, nothing to say" with "could not be read" is
  D65. An automation whose every trigger was condition-blocked has no logbook
  entry, so the count silently fell through to the raw trace count and reported
  runs for an automation that never ran — while for any automation that ran at
  least once it reported the logbook's number, which excludes blocked triggers.
  One column, two meanings, depending on the data. The two states are now a
  named type (`fireCounts`) rather than a nil map.
- **The fallback applies the same definition.** When the logbook genuinely
  cannot be read the in-window traces stand in, filtered by `traceIsRun`. A
  fallback that counted triggers would make the column mean "runs" with a
  logbook and "triggers" without one.

The logbook is preferred over the traces where both exist because HA caps stored
traces per automation (default 5), which would undercount a high-fire rule
badly. It is *not* preferred when it demonstrably records no automation at all:
excluding the automation domain from the recorder or the logbook is ordinary HA
tuning, and that answers 200 with nothing to say about automations forever, which
under "an answered logbook is authoritative" would report `0` for the whole
instance. An automation-less logbook is therefore classified as no answer.

Because `auto ls` counts and `auto show` lists the same underlying traces, the
two commands must not reach two different conclusions about one trace. They
cannot: `traceResult` is the only place this package classifies a trace, and
`traceIsRun` reads its answer back rather than re-deriving one. The difference
between the trace table's row count and `runs_24h` is exactly the rows the table
itself marks `failed_conditions`, with nothing else unaccounted for. This is the
H-11 reconciliation rule applied across two commands.

- Enforced by: `internal/cmd/auto_test.go` —
  `TestFetchAutomationFireCounts_ClassifiesTheLogbooksAnswer` (which wire
  response lands in which state, including the automation-less logbook),
  `TestFireCounts_AnsweredZeroIsNotUnknown`,
  `TestBuildAutoRows_BlockedTriggersAreNotRuns` (the direct D65 regression, run
  in both states), `TestBuildAutoRows_AnsweredSilenceIsZeroNotUnknown`,
  `TestBuildAutoRows_TraceFallbackCountsRunsNotTriggers`,
  `TestTraceIsRun_MatchesTheWordAutoShowPrints`,
  `TestRuns24hReconcilesWithAutoShowTraceTable`;
  `internal/integration/oracle_runcount_test.go` —
  `TestRuns24hMatchesHAsOwnRunCount` (reconciles HA's logbook against HA's
  traces first, so the expectation is never hactl's own model of a run, then
  requires `runs_24h` to equal both), `TestRuns24hIsZeroWhenEveryTriggerWasBlocked`,
  `TestAutoShowTraceTableReconcilesWithRuns24h`, and
  `TestOracleGatedFixtureHasBothOutcomes` — the fixture guard, because an
  automation whose triggers all ran, and one whose triggers were all blocked, are
  each satisfied by counting traces; only `cfgid_gated_charge`, which the rig
  gates shut for three triggers and open for two, can see the difference.
  `make test-int` (Docker tier).
