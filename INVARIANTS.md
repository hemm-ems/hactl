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

- Enforced by: `internal/cmd/ref_test.go` (dry-run default asserted against a
  stubbed companion), `internal/cmd/confirm_guard_test.go` (first-write
  refusal + informed retry), `internal/cmd/config_delete_test.go`

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

Write tests mutate registry state that read tests assert on, so they run
against their own HA instance (`getWriteHA`), which — like every lazily
started instance — must have a matching teardown line in
`internal/integration/main_test.go`.

- Enforced by: `internal/integration/write_roundtrip_test.go`
  (`TestAutoApplyRollbackRoundTrip`, the original H-4 case),
  `internal/integration/write_entity_test.go` (`TestEntSetLabelRoundTrip`
  incl. merge-not-replace and label-deletion detachment,
  `TestEntSetAreaRoundTrip` incl. resolution by name and HA's own
  `area_entities()` as the oracle,
  `TestEntSetLabelAndSetAreaAgreeOnUnknownEntity`),
  `internal/integration/write_dash_test.go`
  (`TestDashCreateSaveDeleteRoundTrip`, `TestDashReplaceRoundTrip`) —
  `make test-int`
