# Invariants

Cross-cutting rules the CLI must satisfy regardless of which command grows
next. Server-side counterparts (auth on every route, dry-run defaults,
backup-before-mutate) live in the hactl-companion repo's `INVARIANTS.md`.

**Discipline:** a law enters this file only through this checklist, in the PR
that adds it:

- It states a **universal over a set a gate can derive** (cobra tree, source
  scan, spec) — never over "the commands below".
- It declares a **pole**, not parity with a sibling (parity was satisfied by
  the commit that broke `device ls --pattern`).
- It names its enforcing test under "Enforced by:", and that test was
  **watched to fail** against the defect it covers.
- Its site set is dispositioned in `dev/surfaces/invariant.manifest`.
- It contains **no hand-maintained count**.

When behavior changes intentionally, the test and this file change in the
same PR.

## H-1 — Non-idempotent requests are never auto-retried

A `POST` is retried only when the request provably never left the client
(dial/connection-refused class). A 5xx or lost response means the server may
have acted, so only idempotent methods (GET/HEAD/PUT/DELETE/OPTIONS) retry —
a create is never silently duplicated. A signed 401 (expired Ingress session)
is safe to retry for any method: the server rejected before acting.

- Enforced by: `internal/httpretry/idempotent_test.go` (`TestShouldRetry` — the
  policy truth table, one definition shared by every client),
  `internal/haapi/retry_test.go` (`TestPostNotRetriedOn5xx`,
  `TestNonIdempotentWritesAreIssuedOnce` over all seven POST sites,
  `TestGetStillRetriedOn5xx` as the negative control),
  `internal/companion/client_retry_test.go` (`shouldRetry` truth table
  including the signed-401 case this client alone has).
- Quantified by: `internal/surfaceaudit` (`TestRetrySurfaceIsClosed`) — the set
  of non-idempotent call sites is derived from the source, not listed here.

  This invariant was enforced against one of the two HTTP clients for months.
  The rule was stated as a universal and the citation named
  `internal/companion/client_retry_test.go`; `internal/haapi` had no method
  check at all, so `svc call --confirm` could fire a service three times. The
  predicate now lives in `internal/httpretry` and both clients import it, which
  is why this list can be short again.

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

*Input* is the half a create has. `area create` has no target to resolve — the
name does not exist yet — and that reading exempted the three registry creates
from the `confirm` surface until `area create "" --confirm` created a real area
with a blank `area_id` on a production instance, which then failed every `area`
command including `area delete` (H-14 refuses an identity-less record, and
`delete` must list first) until a raw WebSocket call removed it. The oracle
says HA accepts a blank name, mints a blank id for it and files a
whitespace-only name under an id it chose, so the refusal cannot be the
server's and cannot come after the request: it is client-side, in both modes,
and the preview refuses exactly what `--confirm` refuses. An exemption argued
from one half of a rule is how the other half goes unenforced.

**Resolving the target is not the whole check.** Some confirmed runs fail on a
fact about the *instance* rather than about the argument, and the preview owes
that fact too. On a Home Assistant whose `input_boolean:` is written inline in
`configuration.yaml` instead of `!include`-ing a file, every `helper create
--confirm` is a structural 400 — and every dry run printed "would create"
anyway, eight domains out of eight, because the layout check lived inside the
companion's create where nothing could ask it. It is askable now
(`GET /v1/config/wiring`, companion C-10), and the preview asks. Two rules fall
out of that episode: hactl does not re-derive a server-side rule to keep a
promise — it asks the server, so the two cannot drift and the preview quotes
the same refusal the confirmed run would print — and a preview that cannot
reach its check fails rather than proceeding, because a silent fallback
restores exactly the behaviour the check exists to remove.

The converse half is what a *read* fix can break. `helper delete` resolves
through the companion's helper lookup; teaching that lookup to resolve
storage-backed (UI-created) helpers — which is what makes `helper show`/`cat`
work at all on a normal instance — turned "the lookup succeeded" into a weaker
claim than "the delete can happen". The preview now checks the source, so a
target the confirmed run refuses is refused in preview too.

**The identifier and the payload are one question, and the answer belongs to
Home Assistant.** Resolving the target says nothing about the *value* being
written, and two previews shipped as `proven` on this surface while accepting
values HA refuses. `ent rename input_boolean.x 'input_boolean.pg w5 bad'`
printed "would rename … references: 2" at exit 0 for five malformed or
cross-domain ids, each of which `config/entity_registry/update` answers with
"Invalid entity ID" or "New entity ID should be same domain". `svc call
automation.trigger --data '{"target":{"entity_id":[…]}}'` echoed the JSON back
unexamined and --confirm answered 400, because HA validates service data with
PREVENT_EXTRA and `target:` is script syntax it flattens before the call.
Both are now judged by HA's own rule — `homeassistant/core.py`'s
`VALID_ENTITY_ID`, mirrored in `internal/haapi/entityid.go` under an oracle
that reads the regex out of the running container, and the accepted key set of
`GET /api/services` — never by a check invented here.

The same measurement bounds the refusal. HA answers 200 to an `entity_id` that
names nothing, to a payload for a service whose registry entry documents no
fields (`script.<name>` takes arbitrary variables), and to a nested section's
leaf field (`mqtt.publish` `qos`) while refusing the section name itself. A
preview refusing any of those would be this rule pointing the other way, so
the enforcement carries both directions in one table.

**Where a preview cannot refuse, it states.** A targeted service called with no
`entity_id`/`device_id`/`area_id`/`label_id`/`floor_id` reaches *no* entity —
HA's target extraction returns before it looks at one — and `entity_id: all`
reaches every entity of the domain. Both are legal, so neither is refused;
both are stated in the plan, because a preview that renders identically for a
no-op and for a domain-wide broadcast is not a preview of either.

**A preview is machine-readable.** `--json` used to be a byte-for-byte no-op
on nearly every preview, so an agent that asked for JSON got prose. Previews
share one shape (`internal/cmd/dryrun.go`) that renders as text or as an
object stating `"dry_run": true` — a caller must be able to tell a plan from a
result by looking at the answer, not by remembering which flags it passed.

- Enforced by: `internal/cmd/ref_test.go` (dry-run default asserted against a
  stubbed companion), `internal/haapi/servicedata_test.go`
  (`TestUnknownFieldsRefusesWhatHomeAssistantRefuses`,
  `TestMalformedEntityIDsMatchesHomeAssistantsRefusal`,
  `TestTargetsAnythingSeesTheTwoExtremes`,
  `TestAcceptedFieldsFlattensSectionsAndIsSorted`,
  `TestValidEntityIDMirrorsHomeAssistantsRegex` — each table carries the
  payloads HA answered 400 to *and* the ones it answered 200 to); oracle:
  `internal/integration/entityid_oracle_test.go`
  (`TestOracleEntityIDRule`, `make test-int`),
  `internal/cmd/confirm_guard_test.go` (first-write
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
  `TestE2EConfirmedRunRejectsWhatDryRunRejectsCLI`),
  `internal/cmd/registry_write_gate_test.go`
  (`TestRegistryCreateRefusesABlankName` over both modes and all three
  registries, with `TestRegistryCreateAcceptsANameWithSurroundingSpace` as the
  no-false-positive control); oracle:
  `internal/integration/registry_blank_name_oracle_test.go`
  (`TestOracleRegistryCreateAcceptsABlankName`, `make test-int`),
  `internal/cmd/helper_family_test.go`
  (`TestHelperCreatePreviewAgreesWithConfirmOnEveryLayout`,
  `TestHelperDeleteRefusesAStorageHelperInBothModes` — both written as an
  equality between the dry run's verdict and `--confirm`'s, plus equality of
  the explanation, rather than as two hand-written expectations that can drift
  apart), `internal/companiontest/helper_family_e2e_test.go`
  (`TestE2EHelperCreatePreviewMatchesConfirmOnEveryLayoutCLI`,
  `TestE2EHelperDeleteAgreesWithItselfOnAUIHelperCLI` — the same equalities
  against a real HA, on the inline layout and the UI-created helper that
  produced the defect; `make test-companion`)
- Quantified by: `internal/cmd/surface_confirm_test.go`
  (`TestConfirmSurfaceIsClosed`, over `dev/surfaces/confirm.manifest`) — the set
  of `--confirm` commands is walked from the cobra tree, not listed here.

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

The comparison used to fold HA's legacy singular keys (`trigger`/`condition`/
`action`, and `service` within a step) onto the modern plural ones, because
writing through the Config API migrated the schema and a faithful round trip
still came back different. The write goes through the companion's single-entry
route now (D-14, issue #128), which splices the caller's own bytes, so nothing
migrates and the comparison is exact — the entry after a rollback is byte-identical
to the entry before the apply, and so is every other entry in the file.

- Enforced by: `internal/companiontest/auto_write_e2e_test.go`
  (`TestE2EAutoApplyWritesOnlyItsOwnEntryCLI`, `make test-companion`), with
  `internal/integration/write_test.go`
  (`TestAutoWritesRefuseWithoutACompanion`, `make test-int`) pinning that there
  is no fallback to the endpoint that rewrites the file

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

The law's set is derived, not enumerated. H-14's sweep derives every
`json.Unmarshal` in `degeneracy.WirePackages` and forces each to call
`degeneracy.Check` or carry a written reason; `surfaceaudit.DecodeSurface`
derives every decode that sweep structurally cannot see — yaml unmarshals
anywhere, decoder constructions, gorilla's `ReadJSON` (a json decode that
never says json), dot imports of codec packages, and json decodes outside the
wire packages or in shapes the sweep cannot record — and requires a
disposition for each in `dev/surfaces/decode.manifest`. `internal/writer` sat
in exactly that gap: it decoded the live automation config from HA into a bare
map — no tag to drift, but the whole document can decode to nothing without an
error — so an empty answer (`{}`, `null`) rendered as a fictitious full-file
diff, was written out as a backup of nothing standing in for the user's only
undo, and an empty backup file would restore an empty config over the live
one. All three paths now refuse, carrying the marker and
`degeneracy.ErrDegenerate`. What neither gate can see is a codec library the
module has never imported — the same boundary the clock surface accepts for
its layout tokens.

- Enforced by: `internal/analyze/trace_unparsed_test.go`
  (`TestOverallResult_EmptyIsNotPass`, `TestCondense_EmptyDecodeIsUnknown`,
  `TestFormatCondensed_UnparsedNeverLooksLikePass`),
  `internal/integration/degeneracy_test.go` (`looksDegenerate`, wired into
  `runHactl`/`runHactlDir`/`runHactlErr`/`runHactlDirErr`),
  `internal/writer/writer_test.go`
  (`TestWriter_Diff_EmptyRemoteConfigIsUnparsed`,
  `TestWriter_Backup_RefusesEmptyRemoteConfig`,
  `TestWriter_Rollback_RefusesEmptyBackup`)
- Quantified by: `internal/surfaceaudit/surface_test.go`
  (`TestDecodeSurfaceIsClosed`, over `dev/surfaces/decode.manifest`, its
  extractor pinned by `TestDecodeExtractorSeesEveryForm`) together with
  `internal/degeneracy/sweep_test.go` (`TestSweep_EveryDecodeSiteIsChecked`) —
  the set of decode sites is derived from the source, not listed here.

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
  `ent ls --pattern` didn't — one command's filters disagreeing about case.
  It was first harmonised toward case-sensitivity (the outlier deleted, not
  the sibling fixed); D-2 (docs/decisions.md) has since decided the opposite
  pole, so every filter flag folds case (`matchPattern`), and
  `TestFilterFlagsAgreeOnCase` asserts that pole over every filter probe.
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
  `internal/cmd/device_test.go` (`TestDeviceMatchesPattern_IgnoresCase`,
  `TestDeviceMatchesPattern_MatchesEitherNameADeviceCarries`,
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

**A sweep that could only read part of the instance is truncated the same way.**
`ref validate` skipped every dashboard whose config would not fetch, at
`slog.Debug`, and then printed a clean bill of health — so a machine consumer
under `--exit-code` (a CI verdict) or `--json` (a parsed document) received a
certificate covering a tree hactl had never seen. Truncation by unavailability is
not distinguishable, to the consumer, from truncation by `--top`. D-7 fixes the
pole: an unreadable half makes `ref validate` refuse and certify nothing whenever
the answer goes to a machine, and state its scope in the report body when it goes
to a person; `--allow-partial` is the only way to obtain a partial answer, and
passing it is the acknowledgement.

The rule is per *source*, and stating it in terms of dashboards is how it was
first shipped one source short. `ref validate` reads four — the entity registry,
live states, config files, every dashboard — and the registry degraded the live
set at `slog.Warn` and reached no gate at all, so the fix for a silent dashboard
left a silent registry directly beneath it. What is now true of all four is that
each is recorded in one place, `validateScope`, and reaches the reader through
one function, `reportValidateScanScope`: that is the universal, and it is the one
the registry broke. The refusal on top of it is **three plus one**, deliberately.
Three sources — registry, config files, dashboards — share
`validateScanGateError`, which refuses only when `--exit-code` or `--json` makes
the answer unreadable by a human. Live states do not go through it: they refuse
unconditionally, in plain text too, from their own branch in `liveEntitySet`,
because the registry alone omits every state-only entity and is not a usable live
set at all — so the sentence that has to be printed is a different sentence, not
the shared one behind a posture flag. The two directions differ and both are
covered: an unread config file or dashboard hides references, risking a false
clean bill; a degraded live entity set reports entities that exist as dangling,
risking a false alarm. **A source added later joins `validateScope` — that part
is not optional — and takes `validateScanGateError` unless it is unusable in kind
the way live states are, in which case it refuses where it is read and says why
there.** Search commands (`ref scan`, `dash grep`)
answer "where is X?" rather than "is the tree clean?", so they warn and still
answer — and their `--json` shape does not change, because a scope note on stdout
would break clause (1) for them.

Fix shape: `format.Table.visibleRows` now returns every row whenever
`opts.JSON` is set, full stop — `--top` has no code path into JSON output at
all, so no per-command opt-out can regress it. `hactl --help` (and every
path that renders cobra help — `-h`, a bare non-runnable command, the
built-in `help` subcommand) now goes through a wrapped `HelpFunc` that marks
the invocation as `helpRendered`, which `applyTokenPolicy` checks to skip
the cap without touching the `--json` exemption it already had. `version`
and `script show` gained a `flagJSON` branch each, encoded straight from a
struct (`versionInfo`, `scriptShowResult`) with nothing printed before it.

**The contract holds on the branch that WROTE, not only on the branch that
planned.** Every write in hactl is dry-run by default (H-2), and the preview
half of this law was closed one release earlier: no `--confirm`-gated command
may assemble a plan outside `dryRun()`, enforced by the `preview` surface. The
confirmed branch had no such rule and no such gate, so fourteen commands printed
their result as unconditional prose — `svc call --confirm --json` answered
`called script.turn_on` at exit 0 immediately after really firing the script,
and area/label/floor create and delete, tpl create and delete,
script/auto/helper create/delete/apply, dash create/save/delete/replace, ent
set-area/set-label, device set-area/set-label, rollback and `config delete` all
did the same. A caller that scripted the documented global flag received a JSON
parse error at the one moment it could no longer retry safely. The pole: **a
confirmed write renders through `done()`, `dryRunPlan`'s counterpart, so both
branches answer the machine and a caller tells a plan from a result by reading
`dry_run` rather than by remembering which flags it passed.** `config delete`
shows why "valid JSON" is not the rule on its own: it echoed HA's
`{"require_restart":false}`, which parses and still names neither what was done
nor whether it worked.

**A rendered wall clock is not a timestamp.** `format.Table` renders one set of
cells as text and as JSON, so every command that put `clock.Short`'s output in a
row also put it in its machine contract: `ent ls --json` answered
`"last_changed": "06:31"` for an entity whose wire value was
`2026-07-30T04:31:28.653662+00:00`, degrading to `"07-28 11:52"` — undatable —
for anything older than today, while `ent show --json` answered the full instant
for the same field. The pole: **JSON carries the full instant with its UTC
offset; the short form belongs to the text table.** The two audiences diverge in
exactly one place, `format.Table.SetMachine`, and the gate is written against
the SHAPE of the value rather than against a list of timestamp-ish field names —
`first_seen`/`last_seen` on the deduped log view is precisely the column a name
list forgets. Seven commands were affected; three of them no report had named.

**And neither is a rendered absence.** The same mechanism, the same table, one
column over: `dashIfEmpty` puts `-` in a cell that stands for "no value" and
`yesNo` puts `yes`/`no` in a cell that stands for a bool, and both reached
`--json` verbatim. `config entries --json` reported `disabled_by: "-"` on 212 of
213 entries while `config show --json` reported `""` for the same field of the
same entry, so `if entry["disabled_by"]` — the obvious thing for a consumer to
write — answered "every entry is disabled" from one command and the truth from
its sibling; `options` was the string `"yes"` where a machine wanted `true`, and
`"no"` is non-empty, so a boolean read as true in both of its states. The pole
is the clock rule's: **a cell whose text form is a rendering declares its
machine value with `format.Table.SetMachine`.** The gate is clause (5) of the
`TestJSONContract` sweep and is written against the value's SHAPE, with one
per-field exemption that states its reason — `state` is HA's own payload, and an
input_select may honestly hold `yes`.

**And a gate against a rendering is only as wide as its vocabulary.** The
paragraph above was written when `yesNo` and `dashIfEmpty` were fixed, and
clause (5) was built from those two renderers' output — `-`, `yes`, `no`. It
says of itself that it checks "the SHAPE of the value ... a list of column names
is the enumeration that forgets whichever column is added next", and it was
right about columns and wrong about values: `strconv.FormatBool` renders `true`
and `false`, neither of which was in the list, so `dash ls --json` answered
`"admin": "false"` for two years past a gate built to catch precisely that
(finding #59). Three more sites — `ent ls` and `auto ls`'s `restored`, and one
more inside `config` — were in the same state, and no report named any of them.
The vocabulary now covers `strconv`'s pair too, and that is still the weaker
half: `boolCell` renders false as `""`, which no value check can ever
distinguish from a string that is honestly empty. The structural half is
`dev/surfaces/boolcell.manifest` — every place a bool becomes a table cell,
derived from the typed source and each one dispositioned — so a cell whose
wording nothing recognises is still a site nobody may leave silent.

**And neither is a rendered abbreviation.** The third instance of the same
mechanism, in the same table, and the widest: six functions in five files each
did their own `if len(msg) > 60 { msg = msg[:57] + "..." }` while ASSEMBLING the
row, so the value reached `format.Table` already cut and `--json`, `--full` and
`--tokensmax 0` were all downstream of a decision nothing could undo. `hactl log
--json --full --tokensmax 0` answered messages of exactly 60 characters for
entries whose real text was a multi-kilobyte Python traceback — 43 of the
reference instance's 54 — and `log show <id>` was the only way to read a message
hactl had received in full. The report named that one site. Of the five it did
not, two reached a machine: `ent ls --json` answered `"state":
"2026-07-31T03:13:..."` for 76 of 4486 entities while `ent show --json` answered
`"2026-08-01T03:33:44+00:00"` for the same field of the same entity, and `trace
show --json` carried the last forty characters of a step's error. The pole is
the clock rule's, one level up: **a column too wide to print declares its width
with `format.Table.SetWidth`, which only `renderText` consults** — so the cap is
a property of the column rather than a step in building the value, `--full`
lifts it because that is what the flag says it does, and there is one
implementation of "shorten a string" (`format.Clip`) rather than six.

Three things were wrong in those six lines and only one was reported. The cut
was a **length** test, so a message whose first line was under the budget passed
through untouched and put its newline in a table cell: the reference instance
printed 58 lines for 54 rows plus a header, three rows split, the continuation
carrying no columns at all — a row per line is what makes a table a table, and
every line-oriented consumer downstream depends on it. And the cut sliced
**bytes**, so a two-byte character straddling offset 57 was left in half; that
instance's messages are German, and the invalid UTF-8 survives into `--json`,
where the encoder writes U+FFFD. `format.Clip` counts runes and
`format.Table.displayRows` folds a cell onto one line, marking the fold.

**The same table, one column further: a value must be reported as the value it
was matched against.** `--component` matches the full dotted logger name and
`shortComponent` cut it to its last segment — for display, said its own comment,
except that a table cell IS the JSON value. `log --component template --json`
answered rows whose component read `config`, `state` and `trigger`, none of
which contains the filter term, while their real names were
`homeassistant.components.template.config` and two siblings; a caller could
neither audit the match nor grep the answer for their own filter, and `log show
--json` reported the full name for the same field of the same entry. The
machine value is the matched value (`SetMachine("component", …)`), the last
segment stays the reader's column.

**A number hactl re-emits is the number Home Assistant sent.** H-21 was reported
as a decode defect and fixed as one; the encode half shipped standing.
`encoding/json` decodes every JSON number into `float64` for a `map[string]any`
and marshals `float64(5000)` back as `5000`, so `ent show --json` re-emitted
HA's `"max": 5000.0` — a float by construction on every `number.*` entity — as a
bare integer, while `12.7` round-tripped untouched, which is why it survived a
release. The pole: **an attribute map decodes with `json.Number` and re-encodes
byte for byte**, a property of the type (`wireAttributes`) rather than of one
renderer, so every decode of an entity state has it and no second site has to
remember.

**Truncation is not the cap's business when the output is a document.**
`--tokensmax` chops at a byte boundary and appends a plain-English notice.
`--json` was exempt and documented as exempt; nothing else was, although
`dash show --raw` (its own help: "for LLM round-trip editing"), `--yaml`,
`--view`, the verbatim `cat` family, `config file`/`block` and `hactl completion
bash` all write documents. A 91 541-byte dashboard came back as 2 096 bytes of
invalid JSON at exit 0; `auto cat > backup.yaml` truncated inside a quoted
scalar, so the backup did not parse; `completion bash >
/etc/bash_completion.d/hactl`, the line that command's own `--help` prints,
produced a script `bash -n` rejects. The pole is the one `--json` already set:
**output whose contract is "this parses" is never capped — a caller narrows it
with filters — and a command declares itself a document with
`markStructuredOutput`.** Prose is still capped, which is the control the
exemption has to keep passing.

**Two flags naming one format is a question with no answer.** Clause (1) is why
`dash show x --raw --yaml` cannot be fixed by noting which flag won: under
`--json` a note on stdout breaks "it parses, with nothing else on stdout", and a
note on stderr is the same silence one stream over. So the combination is
refused — the only answer that also stays true when a fourth format arrives.
`--raw` beat `--yaml` beat `--json` by the order of three if-statements,
documented nowhere, and a caller who asked for YAML got compact JSON at exit 0
(finding #60). The set of format flags lives in one place
(`outputFormatFlagNames`), read by both the runtime check and the closure gate
over `dev/surfaces/outputformat.manifest`, so a command that grows a second way
to spell its output format is a site somebody has to disposition rather than a
new silent precedence rule.

- Enforced by: `internal/format/format_test.go` (`TestRenderJSON_TopN` —
  inverted from asserting the truncation bug as correct to asserting `--top`
  has no effect on `--json`), `internal/cmd/json_contract_test.go`
  (`TestJSONContract`, which walks the live cobra command tree — so a newly
  added read command is covered automatically — and asserts all three
  properties on every non-mutating, non-meta, non-verbatim-by-design leaf it
  can exercise against a fake HA, and — clause (4) — fails on any string value
  anywhere in any swept document shaped like `clock.Short`/`ShortSeconds`
  output; and — clause (6) — on any string value carrying
  `format.TruncationMarker`, with NO field exemption: `state` is exempt from
  clause (5) and is precisely where `ent ls` truncated, so inheriting that
  exemption would have left the check blind to the site it was written for;
  `TestRootHelp_NeverTokenTruncated`, `TestVersionJSON_Shape`,
  `TestScriptShowJSON_Shape`, `TestLogJSON_CarriesTheWholeMessage` as clause
  (6)'s positive half — a document that simply dropped the column satisfies the
  sweep — and `TestLogText_ComponentIsTheLastSegment` as its control, since a
  fix that showed the whole logger name to the reader too satisfies the
  positive half),
  `internal/format/format_test.go` (`TestSetWidth_JSONCarriesTheWholeValue`,
  `TestSetWidth_FullLiftsTheCap`, `TestSetWidth_ACellIsOneLine`,
  `TestSetWidth_TextCapsTheColumn` as the control that the cap did not become
  "nothing is capped", `TestClip_NeverCutsARuneInHalf` over every width),
  `internal/analyze/trace_test.go` (`TestStepOutcomeKeepsTheWholeError` — the
  condensed step keeps Home Assistant's whole error and `FormatCondensed`
  shortens it),
  `internal/livefire/log_honesty_test.go` (the two-profile sweep, against the
  reference instance as well as the rig: `TestSweepLogJSONCarriesTheWholeMessage`
  — which also fails when the longest message on the instance is too short to
  reach the cut, so the case cannot pass vacuously —
  `TestSweepLogTextIsOneRowPerLine`,
  `TestSweepLogJSONComponentIsWhatTheFilterMatched`,
  `TestSweepLogFamilyAgreesOnItsSchema`),
  `internal/cmd/boolcell_test.go` (`TestBooleanColumnsRenderAsJSONBooleans` —
  every bool column across five commands, each asserted true AND false so a
  constant column cannot satisfy it) with
  `internal/cmd/surface_boolcell_test.go` (`TestBoolCellSurfaceIsClosed`) as its
  closure half,
  `internal/cmd/surface_outputformat_test.go`
  (`TestOutputFormatSurfaceIsClosed`, `TestDashShowRefusesConflictingOutputFormats`),
  `internal/cmd/json_confirm_contract_test.go` (`TestJSONConfirmContract` — the
  write half: every `--confirm`-gated command the fixture can answer is driven
  through BOTH branches and must report `dry_run`, `action` and `ok`; a write in
  the tree that is neither driven nor recorded with a reason fails by name),
  `internal/cmd/json_timestamp_test.go` (the positive half of clause (4):
  `TestEntLsJSON_TimestampIsTheInstantHASent`,
  `TestEntHistJSON_TimestampIsTheInstantHASent`,
  `TestLogJSON_TimestampCarriesAZone`, `TestLogShowJSON_TimestampCarriesAZone`,
  `TestTraceShowJSON_StepTimeCarriesAZone`, plus
  `TestEntShowJSON_WholeFloatAttributeStaysAFloat`, which asserts on the BYTES
  because Go decodes `5000` and `5000.0` to one value),
  `internal/integration/json_wireform_oracle_test.go`
  (`TestOracleEntShowJSONPreservesWireNumberForm` — the same claim against a
  live HA, since a stub answering `5000.0` proves hactl preserves what it is
  handed and not that HA hands it over; `TestOracleEntShowJSONNumberDomains`
  records how wide that oracle is),
  `internal/cmd/tokencap_document_test.go`
  (`TestDocumentOutputIsNeverTokenCapped`,
  `TestCompletionScriptIsNeverTokenCapped`, `TestProseOutputIsStillTokenCapped`
  as the control that the exemption did not become "nothing is capped", and
  `TestVerbatimLeavesAreDispositionedForTheTokenCap` as the closure clause over
  the tree-derived verbatim set),
  `internal/cmd/ref_test.go`
  (`TestRunRefValidate_UnscannableDashboardRefusesUnlessAllowPartial` — the
  unavailability half, watched red against a scanner that swallows the failure;
  `TestRunRefValidate_AutoGeneratedDefaultIsNotAPartialSweep` as the
  cry-wolf control, since a dashboard HA holds no config for has no references
  to miss; `TestRunRefScan_UnscannableDashboardStillAnswers` as the
  search-command control)
- Quantified by: `internal/surfaceaudit` (`TestResultSurfaceIsClosed` — the set
  of confirmed-write result renderings is derived from the source, mirroring
  `TestPreviewSurfaceIsClosed` one branch over, and is empty by design;
  `TestResultExtractorFlagsProseOnConfirm` feeds it a known-bad function so an
  extractor that stopped matching cannot pass while proving nothing;
  `TestTruncationSurfaceIsClosed` over `dev/surfaces/truncation.manifest` — the
  set is every `<something> + <ellipsis>` in the source, so a seventh site
  written from memory is red the day it appears. It is the surface finding #14
  needed: the report named one of six, and nothing in the tree could have said
  how many there were)

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
- `cc show`'s `entities: N` counted only the registry rows HA also holds a
  state for, and said nothing about the rest. The filter was documented as
  removing stale rows for removed devices; measured against a real instance it
  removes nothing but DISABLED entities — 243 of `homematicip_local`'s 402, 56
  of `dwd_weather`'s 75, and across all 5524 registry rows there not one row
  without a live state for any other reason. A caller checking the number
  against the registry saw 159 where HA says 402, with nothing naming the
  difference. `entity_count`, `disabled_count` and `registry_count` are
  reported together now, and a row in neither list is still in the total —
  so a genuinely stale row, the case the filter claimed to handle, shows up as
  the three numbers failing to add up rather than as a silent subtraction.

A fifth command fabricated *fields*, not rows: `log show` resolved any ID
`pkg/ids.Registry` recognized regardless of prefix, so a `trc:` or `anom:` ID
(the latter's key shape — `entity_id|type|start_time` — coincidentally matches
a log key's own pipe-delimited 3-part shape) would resolve and print an
unrelated record's fields as if they were this entry's
timestamp/component/message. `log show` now rejects any ID without a `log:`
prefix before resolving, mirroring the same check `trace.go`'s
`resolveTraceID` already did for `trc:`.

The `anom:` namespace itself no longer exists. `ent anomalies` minted those
ids into `cache/ids.json` and printed them in an `id` column while no command
accepted one — an identifier without a consumer is a fabricated address, this
invariant's class in its purest form — so the minting was deleted rather than
given a consumer (D69; D-5 in `docs/decisions.md`), and a standing gate pins
both anomaly renderers, both output formats and the id-registry file, so
re-minting can only arrive in the same PR as its consumer. `log show`'s
prefix check stays load-bearing regardless: `cache/ids.json` persists across
upgrades, so a registry written by an older hactl can still hold `anom:`
entries, and `Resolve` accepts any prefix.

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
  `TestRunLogShow_JSON`, `TestRunCCShow_JSON`,
  `TestRunCCShow_ReconcilesWithTheRegistry`,
  `TestRunCCShow_StaleRegistryRowIsVisibleNotSubtracted`),
  `internal/cmd/ent_anomalies_id_test.go` (`TestEntAnomaliesMintsNoIdentifier`
  — the D-5 standing gate, watched red against the re-introduced minting),
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

**A write that Home Assistant reports as FAILED claims nothing either.** The
clause above is one direction of the rule and it shipped without the other. On a
url_path long enough that `.storage/lovelace.<id>` passes the filesystem's
255-byte filename limit, HA removes the dashboard from its collection and *then*
fails unlinking the file — from a listener that runs after the removal — so the
websocket answers `Unknown error` about an object that is already gone
(`OSError: [Errno 36]`, traceback captured on the reference instance
2026-07-31). `dash delete --confirm` reported that as a plain failure at exit 1,
which tells a caller the dashboard still exists; a retry then fails with "not
found", and a script gating on the exit code takes the wrong branch either way.
Whether the object changed is a question the error does not answer, and the only
thing that answers it is a read-back: `runDashDelete` re-resolves the url_path
before it decides what to say, reports the delete as done when it is gone, and
carries HA's own error into `warnings` so the failure is not swallowed on the
way. The general rule is that a confirmed write's *outcome* is a fact about the
instance, not about the last call's return value — and that fact is free to
disagree with it in either direction.

**A flow preview resolves the domain the way the confirmed run does.** `config
flow-start`'s dry run validated the domain against `manifest/list` — the
integrations HA has *loaded* — so every not-yet-configured integration (the very
thing you start a flow for) was refused as "no loaded integration" while a
confirmed flow-start lazily loaded it and succeeded. The dry run failed exactly
where the confirmed run worked, the inverse of the H-2 contract, and it broke
the command's whole purpose. It now resolves against HA's `flow_handlers` list
(the authority on what `StartConfigFlow` accepts), so preview and confirm agree.

- Enforced by: `internal/companiontest/auto_write_e2e_test.go`
  (`TestE2EAutoApplyWritesOnlyItsOwnEntryCLI`, the original H-4 case, now
  reading automations.yaml back as bytes),
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
- Quantified by: `internal/cmd/surface_writeback_test.go`
  (`TestWriteBackSurfaceIsClosed`, `make test-surface`) — the set of write
  commands is derived from the live cobra tree (every `--confirm` flag, the same
  walk H-2's gate uses, because H-2 makes `--confirm` the definition of a
  mutating command), and `dev/surfaces/writeback.manifest` dispositions every one
  of them as proven by a read-back from HA, exempt because HA holds no record to
  read, or recorded as debt. The citation list above is what this replaces — it
  names the families somebody wrote a round-trip test for, which is where its
  scope came from, so `dash save` could sit stubbable through a release and the
  `area`/`floor`/`label` writes can still be checked by reading them back through
  `hactl … ls` without leaving a trace anywhere. A write family added tomorrow is
  now unclassified, and unclassified is red.

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

## H-16 — An answer is a function of the instance, never of map iteration order

Two invocations of a read command against an unchanged Home Assistant MUST
produce byte-identical output. Where a command assembles its answer from several
sources — and especially where any of them walks a Go map — the assembled result
MUST be made canonical before rendering, by a comparator that is a **total**
order over everything that distinguishes two rows. Rendering MUST NOT be the
place ordering is decided.

`ent related` is the case that named the rule. It concatenates rows from four
sources: the companion's config/YAML scan, `findDeviceSiblings` and
`findAreaNeighbors` — both of which range over `registryContext.entityByID`, a
map whose iteration order the Go runtime randomises on purpose — and
`findGroupMemberships`. `format.Table` renders rows in slice order and re-sorts
nothing. Only `dedupeAndSortRelated` stands between a randomised walk and the
user's terminal, and it is load-bearing in a way that is easy to lose: it dedups
on the whole `relatedEntry` struct and then sorts on all three of its fields, so
no two surviving rows can compare equal and the unstable `sort.Slice` cannot
choose between them. Weaken the comparator to `entityID` alone — the obvious
"tidy-up" — and two device-siblings of the same entity start swapping places
between runs.

Nothing in the suite watched for this. Every content assertion on `ent related`
is a substring check that passes on any permutation, so a reordering was
invisible; and the one test that ran the command twice spent its second run
asserting a wall-clock ceiling (`cold <=10s`, `warm <=3s`) against a command that
has no cache, which measured process start-up and the machine's load and nothing
about the answer. Determinism is what repeating a command can actually prove.

The unit half of the enforcement is deliberate, not belt-and-braces: the
companion tier's fixture entity is absent from HA's registry, so at that tier the
graph comes entirely from the companion and the two map-walking sources
contribute no rows. The E2E check therefore pins the renderer and the companion
half; the map-iteration half is only pinned where it can be pinned on every
machine without Docker.

- Enforced by: `internal/cmd/pure_test.go`,
  `TestDedupeAndSortRelated_Canonical` (all 120 permutations of a five-row set
  that includes rows differing only in `relationship` and only in `detail`, so a
  first-field-only comparator fails) and
  `TestDedupeAndSortRelated_DropsExactDuplicates` — `make test`; plus
  `internal/companiontest/e2e_test.go`,
  `TestE2EEntRelatedCompanionGraphCLI`, which drives the real binary against a
  real HA and a real companion three times and requires stdout to be identical
  across runs — `make test-companion` (Docker tier). The comparison is over
  stdout alone because hactl's slog handler stamps every stderr line with
  `time=`.
- Quantified by: `internal/surfaceaudit/surface_test.go`
  (`TestMapRangeSurfaceIsClosed`) — the set of map walks is derived from the
  typed source across every build-tag configuration, and
  `dev/surfaces/maprange.manifest` dispositions every one of them, so a new
  map-range site cannot appear silently. The hand sweep this replaces ran once
  (2026-07-26), found `companion wireguard status` printing one arbitrary map
  entry, and was never run again. Mechanising it immediately found a second
  live violation the hand sweep had cleared: `renderFlowResult` printed a
  failed flow-step's per-field errors in map order. Both renders are now pinned
  byte-identical by `internal/cmd/wireguard_format_test.go`
  (`TestWriteWireguardStatus_ResolvedRenderIsDeterministic`) and
  `internal/cmd/flow_render_test.go`
  (`TestRenderFlowResult_ErrorsAreDeterministic`) — `make test`.

## H-17 — An identifier hactl prints is an identifier hactl accepts

Whatever hactl displays as a name for a resource, every hactl command that
filters or resolves that resource must match it. The rule is one-directional and
absolute: printing is a promise. A caller who copies a string out of one command
and pastes it into another is doing the thing the output invited, and the second
command answering "nothing" is hactl contradicting itself.

Home Assistant carries four interchangeable names for one automation: the
config `id:` (surfaced as `attributes.id`, and the key HA files traces under),
the alias (`attributes.friendly_name`, verbatim), the `entity_id` HA derives
from the alias, and that entity_id's object id. HA's UI mints a millisecond
timestamp for the config id, so for essentially every UI-authored automation it
is a *completely different string* from the object id `auto ls` prints in its
`id` column. D-1 (docs/decisions.md) fixes the pole for the family: every
command that takes an automation accepts every one of these forms, and where
one is printed as *the* identifier, it is the config id.

hactl printed all of them and resolved most — `auto show` displays the
entity_id and the `config_id`, `auto create` prints the config id it just wrote,
and `auto cat`/`diff`/`apply`/`delete` key on it — while `auto ls --pattern` and
`ent ls --pattern` matched only the entity_id forms. So an id hactl printed was
an id hactl refused (D6/R2). The consequence is worse than an inconvenience: the
manual routes a caller who cannot find something to `ent ls --pattern` as the
discovery fallback, and under the stop-at-the-first-miss rule an empty listing
there reads as "no such entity" — a wrong answer, not a missing one.

The same mechanism recurred twice after the `--pattern` fix, in the two family
members whose lookup did not route through the shared resolver. `auto rollback`
matched the caller's raw reference against backup filenames, which `auto apply`
keys by config id — so the object id, the entity_id and the alias all answered
"no backup found" for an automation whose backup existed. And `auto delete`
forwarded the raw reference to the companion, whose DELETE resolves a config
id, an alias, or a live entity_id but never the bare object id — the one
identifier `auto ls` prints — so the preview succeeded (the live entity
resolves) and `--confirm` 404'd, H-2's contract inverted. Both are why the set
of resolver sites is now derived from the source rather than remembered: every
entrypoint taking an automation reference must reach `resolveAutomation`.

Two bounds keep the rule from degrading into "match anything":

- **Only identifiers hactl actually prints for that resource count.** The
  automation config id and alias are claimed because hactl prints them; a
  `sensor` that happens to carry an `id` attribute — or a friendly_name — is
  not addressable by it, because nothing in hactl ever offers either as that
  sensor's name or resolves the sensor by it.
- **A resource that matches on two of its identifiers is still one row.** The
  filter widens what matches, never how often.

- Enforced by: `internal/cmd/auto_test.go` —
  `TestFilterAutosByPattern_AcceptsTheConfigIDHactlPrints`,
  `TestFilterAutosByPattern_AcceptsTheAliasHactlPrints`,
  `TestFilterAutosByPattern_MatchesEachAutomationOnce`,
  `TestBuildAutoRows_CarriesTheConfigID`; `internal/cmd/ent_test.go` —
  `TestFilterEntitiesByPattern_AcceptsTheConfigIDHactlPrints`,
  `TestFilterEntitiesByPattern_AcceptsTheAliasHactlPrints` (their
  `sensor.thermostat` rows are the scope bound),
  `TestFilterEntitiesByPattern_MatchesEachEntityOnce`;
  `internal/cmd/rollback_target_test.go` —
  `TestAutoRollbackAcceptsTheIdentifierAutoLsPrints`,
  `TestAutoRollbackStillRefusesWhatDoesNotExist`;
  `internal/cmd/auto_delete_target_test.go` —
  `TestAutoDeleteSendsTheCompanionAnIdItAccepts`,
  `TestAutoDeleteDryRunStillRefusesUnresolvable`;
  `internal/companiontest/e2e_test.go` — `TestE2EAutoDeleteByObjectIDCLI`
  (`make test-companion`, Docker tier);
  `internal/integration/oracle_pattern_test.go` —
  `TestPatternAcceptsEveryIdentifierHactlPrints` (reads the entity_id/config
  id/alias tuples from the live instance's `/api/states`, asserts `auto show`
  prints the config id HA reports, then requires `auto ls --pattern` and
  `ent ls --pattern` to match on every form) and
  `TestPatternStillRejectsWhatDoesNotExist` (the negative control — a filter that
  matched everything would satisfy the first test just as well). `make test-int`
  (Docker tier).
- Quantified by: `internal/surfaceaudit`
  (`TestAutomationRefSurfaceIsClosed`, `make test-surface`) — the set of
  entrypoints taking an automation reference is derived from the source, and
  one that bypasses `resolveAutomation` fails the build the day it appears;
  and `TestTargetSurfaceIsClosed` for the wider any-resource half.

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

## H-19 — A test must be able to fail for a reason other than the process dying

Every test function in this repository, in every tier, must reach a failure site
that observes a value. Running a command, checking that it returned no error and
then discarding its answer is not a test: it goes green whether the command
answered correctly, answered nothing, or answered garbage. That floor is not a
review convention — it is enforced mechanically, and a thirteenth such test
cannot be added.

The rule is deliberately structural rather than lexical. `out := runHactl(t, …)`
followed by `_ = out` is only the most visible spelling; a gate that grepped for
it would be satisfied by renaming the variable. What the analyzer looks for is a
`t.Error*`/`t.Fatal*` whose nearest enclosing condition inspects something other
than an error:

- `if out != want`, `if !strings.Contains(out, x)`, `if len(rows) == 0` — all
  observational, all count.
- `if err == nil { t.Fatal("want error") }` counts: requiring the command to
  refuse is a behavioural claim.
- `if err != nil { t.Fatalf(…) }` does **not** count. That is the liveness check
  the whole rule is named after; every command that runs at all passes it.
  `t.Skip` in any form does not count either — a skip is a silent pass (TC-8).
- A call to a same-package helper counts when the helper is itself
  assertion-bearing, so `assertContains(t, out, "dry-run")` is an assertion even
  though the `t.Errorf` lives a frame down. A helper only qualifies if it
  **returns nothing**: `runHactl` hands its output back, so its internal
  `t.Fatalf(… failed …)` is ambient — it applies identically to every test in
  the tier and therefore distinguishes none of them. Descending into it would
  mark all 274 integration tests as asserting and the gate would prove nothing.

**What it deliberately does not catch.** It does not judge whether the asserted
value was worth asserting, and in particular it cannot see a test that asserts a
value it had itself just supplied ("write X, read back X through the same
tool") — that is a dataflow property across a process boundary, not a syntactic
one. H-12 covers that class by requiring every write family to read back from
Home Assistant directly and assert a witness field the command never mentioned.
It also says nothing about assertion strength: one `assertContains` on a header
row passes. This is the floor beneath H-12, not a substitute for it.

**Opt-out.** A test that genuinely has nothing to observe declares it in its doc
comment:

```go
//test:no-assert <why this test can only prove liveness>
```

The reason is mandatory, is held to a minimum length so `n/a` cannot become the
idiom, and is printed by the gate — an exemption is a visible decision with an
author, in the same spirit as the repo's `//nolint` suppressions. An exemption on
a test that *does* assert is itself a failure, so exemptions cannot rot in place.
**No test in the repository uses one today**, and the tally the gate prints is
the number to keep at zero.

The gate refuses to pass on an empty or lopsided corpus: each tier is known to
hold a non-trivial number of tests, so a walker that stopped early or a build tag
that stopped matching fails loudly instead of reporting a perfect score over
nothing (TC-7).

- Enforced by: `internal/testaudit/assertions.go` and
  `internal/testaudit/assertions_test.go`, `TestAssertionFloor` — the **unit**
  tier, no Docker required, run as its own step by `make test-assert-floor`
  (named ahead of the tiers it judges in `make gates`, and its own CI step) as
  well as incidentally by `make test`. Files are parsed from disk rather than
  loaded through `go/packages`, so the build-tag-gated tiers are covered too;
  the tier is derived from the file's `//go:build` line, and an unrecognised tag
  fails the gate rather than silently widening the blind spot. The classifier's
  own two load-bearing verdicts — `err != nil` is not an assertion, a
  value-returning helper is not an assertion helper — are pinned by
  `TestClassifierVerdicts` against synthetic packages.

## H-20 — A rendered time is in the reader's zone, and `--since` counts back from now

Every wall-clock hactl prints is converted to the local zone before rendering;
a raw HA timestamp (UTC) never reaches the terminal. `--since` names how far
*back* to look — a positive duration (`24h`, `7d`), rejected when negative,
because an inverted window returns HA's empty answer, which the manual's
"stop at the first miss" rule turns into a confident wrong negative.

The timezone half shipped wrong twice: the first fix converted the renderers
it grepped for, and `analyze.shortTimestamp` — a fourth renderer that never
parses a time at all — kept rendering UTC, with a unit test pinning
UTC-in/UTC-out as correct (#94). The pole is therefore stated here once, and
the set of renderers is derived, not listed.

- Enforced by: `internal/surfaceaudit/surface_test.go`
  (`TestClockSurfaceIsClosed` — derives every clock layout in the source, so a
  new renderer must be dispositioned before it merges),
  `internal/cmd/since_test.go` (`TestParseSince_RejectsNegative`),
  `internal/cmd/ws_cmd_test.go` (`TestApplyLogSince`).

## H-21 — A listing decodes only the entities it lists

A command that renders one entity domain reads `/api/states`, which is every
entity in the instance. The set whose attributes it decodes into a
domain-specific schema must be a subset of the set it renders: split the
payload, read the identity, filter, then decode. An entity the command discards
can never determine whether the command succeeds.

The pole is the **ordering**, not schema tolerance. A command may keep a typed
attribute schema for the entities it actually renders — `automationAttributes`
is a correct description of an automation, and `Current int` is right by
construction, HA computes it as `Script.runs` → `return len(self._runs)`. What a
command may not do is apply that schema to entities it never describes.

**The collision ships in the box.** Attribute keys in HA are neither
domain-scoped nor type-stable across domains, and this needs no third-party
integration to demonstrate: in a single `/api/states` response from HA's own
integrations, `max` is an integer on `automation.*`/`script.*` (the `max_runs`
count) and a fraction on `number.*` (`100.0`); `current_temperature` spans
integral, fractional and null across `climate` and `water_heater`; seven further
keys behave the same way. `max` is the load-bearing one, because it is a key the
*listed* domains themselves carry — `automationAttributes` is exactly one
`Max int` field away from failing on a stock Home Assistant with any `number`
entity present. Nobody decided that; the field simply was never added. **The old
ordering held only by luck, and reading this defect as "a weird third-party
sensor" turns the rule into a hardening nicety it is not.**

What it cost while the ordering was wrong: `auto ls` and `script ls` both died
on a live instance (HA 2026.7.4) with `cannot unmarshal number -1.7525 into Go
struct field automationAttributes.attributes.current of type int` — an entity
neither command lists. Neither H-7 nor the H-14 sweep fires here, because both
govern decodes that silently yield *nothing*; this one fails loudly on data it
should never have read.

Two consequences carry the same weight as the ordering:

**The failure names its record.** Decoding per entity means the loop knows the
entity_id, which `encoding/json` cannot report for a slice element. The error
reads `parsing states: entity sensor.<id>: attributes.current: cannot unmarshal
number -1.7525 into Go value of type int`. The report above cost a source-reading
session only because the message named hactl's Go type instead of the instance's
entity, and the instance is a third party's that this project cannot reach. An
error that names the record means the next report arrives with its diagnosis
inside it.

**A fetch that failed is not an empty answer.** `resolveAutomation` used to
discard `fetchAutomations`' error and return "no match", so an unreadable
`/api/states` made every automation reference resolve as unknown with nothing
printed: `auto show` fell back to its `"automation." + id` guess and 404'd as if
the caller had typed a bad name, `auto delete` forwarded the raw object id to
the companion (H-17), `auto cat`/`diff`/`apply`/`rollback` skipped the config-id
path, and `trace show` passed the reference through unrewritten. That is H-7 at
five sites — an unavailable source rendering as a confident negative answer —
and the ordering fix does not retire it, because the fetch can still fail on
network, on auth, or on a genuinely degenerate payload.

- Enforced by: `internal/surfaceaudit/surface_test.go`
  (`TestDomainDecodeSurfaceIsClosed` — derives every place a domain-specific
  attribute schema can meet a states payload, so a new schema, a new
  whole-payload read, or a new join must be dispositioned before it merges),
  `internal/cmd/states_domain_decode_test.go`
  (`TestAutoLsIgnoresAttributesOfEntitiesItDiscards`,
  `TestScriptLsIgnoresAttributesOfEntitiesItDiscards` — a discarded entity
  carrying every key of the command's own attribute struct at a colliding type;
  `TestColliderCoversEachStructsOwnKeys`, which derives that key set from each
  struct rather than hand-copying one list for two different structs;
  `TestStatesDecodeErrorNamesTheEntityAndKey`;
  `TestStatesWithoutEntityIDStillPoisonsTheListing`, the H-14 half — filtering
  earlier must not narrow a payload that lost its identity into "no automations
  found"), `internal/cmd/auto_resolve_failure_test.go`
  (`TestAutomationReferenceCommandsReportAFailedStatesFetch` over every command
  that takes an automation reference, `TestResolveAutomationDistinguishesNoMatchFromNoAnswer`),
  `internal/integration/domaindecode_test.go`
  (`TestAutoLsIgnoresAForeignEntitysAttributeTypes`,
  `TestScriptLsIgnoresAForeignEntitysAttributeTypes` through the real CLI
  against a payload HA produced, with `TestEntListingsStillSeeTheColliderIsTheControl`
  as the negative control that the colliders were in that payload at all).
- Premises probed, not assumed: `internal/integration/domaindecode_oracle_test.go`
  (`TestOracleStatesCarriesOneKeyAtTwoJSONTypes` — the stock-HA collision above;
  `TestOracleAutomationScriptCurrentIsIntegral` — `current` read out of the
  running container's own source and observed above zero, which is why widening
  the field is not the fix; `TestOracleStatesSixKeyDomainCensus` — every key
  both structs decode is reachable at a colliding type, so the acceptance
  fixture covers all of them).
- Tier: `make test` for the ordering, the error naming and the resolver;
  `make test-int` for the real-wire half and the oracles.

The set this law quantifies over is derivable and **is** derived, by
`TestDomainDecodeSurfaceIsClosed` over `dev/surfaces/domaindecode.manifest`. The
rule is a conjunction — a domain-specific schema meets an unfiltered payload —
so the surface derives all three of its legs and none can appear silently: the
schemas (every struct declaring a `json:"attributes"` field whose type is not a
map), the payloads (every function that reads the whole `/api/states`
document), and the joins (every function that hands a pointer to a domain
attribute schema into a call, or that names one in its signature and unmarshals
wire bytes). A newly added domain-typed attribute struct is unclassified on the
day it appears.

The derivation immediately contradicted the count this law was written with.
`SPEC-states-domain-decode.md` §2 called the class "exactly two sites — not a
guess, a derived count"; it was neither. Sixteen sites carry the rule, and three
of them — `auto show`, `script show`, `script apply` — are domain-typed decodes
of a states payload that the ordering fix never touched. They are safe, but only
because each addresses `/api/states/<entity_id>` inside its own domain, so the
set decoded is the single entity rendered. That is a reason somebody had to
write down; before the surface existed, nobody had checked it.

## H-22 — An argument a command cannot act on ends the command

Every command in the tree declares what positional arguments it takes, and the
declaration refuses three things before the command body runs: an argument that
is empty or whitespace-only, an argument on a command that takes none, and — on
a command that holds subcommands — a subcommand it does not have. The pole is
**refuse**: an empty string is not a wildcard, an ignored argument is not a
filter, and a mistyped subcommand is not a help request.

The rule is about the *boundary*, not about any one resolver. Where a resolver
compares the caller's reference to a field, a record that legitimately carries
that field empty answers the empty string — and Home Assistant produces such
records in the ordinary course of being used. A restored automation ("ghost":
registry entry, no config) has an empty config id and an empty friendly_name; a
device the user renamed has an empty registry `name`, because the override lives
in `name_by_user`. So `hactl auto show ''` printed a real, unrelated automation,
`hactl auto delete ''` printed a plan to delete it, and `hactl device show ''`
answered with an arbitrary real device — each at exit 0, each one flag from a
write against an object nobody named.

The other two legs are the same defect where the argument is not an identifier:

- **A command that takes no positionals must say so.** `Args == nil` is cobra's
  ArbitraryArgs, so `hactl ent ls sensor` accepted `sensor`, discarded it, and
  printed the same listing as `hactl ent ls` — the most plausible mistake an LLM
  caller makes with that command, answered with a plausible wrong result. The
  refusal names the flag that does what the caller meant (`--domain sensor`),
  built from the command's own flag set rather than a table.
- **A family group must refuse an unknown subcommand like the root does.**
  Cobra's `legacyArgs` errors for the root and returns nil for every other
  group; a group with no `Run` then answers *anything* with its help text at
  exit 0. Twelve families were confirmed doing it. Setting `Args` on such a
  group changes nothing — `execute` returns `flag.ErrHelp` before
  `ValidateArgs` — so a group carries a `RunE` that prints its own help, which
  is what makes cobra validate its arguments at all.

- Enforced by: `internal/cmd/surface_positional_test.go`
  (`TestNoCommandAcceptsABlankPositional` — every command in the live tree,
  driven through the real entry point with one, two and three blank arguments,
  asserting the refusal comes from the contract rather than from the next error
  down; `TestEveryFamilyRefusesAnUnknownSubcommand` over every command that
  holds subcommands; `TestFamilyGroupsAreMarkedAsSuch`, which pins the
  annotation the MCP gate, the `--json` sweep and the manual guardrail read),
  `internal/cmd/args_test.go` (the message contracts: the blank refusal names
  the placeholder from the command's own `Use` line, the unexpected-positional
  refusal carries the flag alternative, the unknown-subcommand refusal keeps
  cobra's did-you-mean, and `TestValidUsageIsUnchanged` holds the other half —
  a bare family still prints help at exit 0, optional arguments stay optional;
  `TestResolveAutomationRefusesABlankReference` and
  `TestResolveDeviceRefusesABlankReference` at the two resolvers where the wrong
  match was actually made),
  `internal/integration/positional_test.go`
  (`TestBlankAutomationIdentifierResolvesNothing` against a ghost this test
  creates through HA's own config API, with the ghost's real identifiers as the
  control; `TestListingRefusesAPositionalFilter`;
  `TestUnknownSubcommandFailsAgainstALiveInstance`).
- Premise probed, not assumed:
  `TestOracleAutomationWithoutAnIDCarriesNoConfigID` (`make test-int`, the
  `idless` fixture) — HA reports `attributes.id` only for an automation that has
  an `id:`, so an automation without one is an entity whose config id is empty.
  That is the record the empty string matched, and it is stock YAML rather than
  the field instance's ghost: the same probe established that creating an
  automation through HA's config API and deleting it again removes the entity
  outright on HA 2026.x, so the rig cannot make a ghost that way and nothing
  here needs one.
- Quantified by: `internal/cmd` (`TestPositionalSurfaceIsClosed`,
  `make test-surface`) over `dev/surfaces/positional.manifest` — the set is the
  live cobra tree, and a command whose `Args` is not one of the five
  constructors in `internal/cmd/args.go` is a site. A new command inherits
  nothing silently: it is red until its contract is written.
