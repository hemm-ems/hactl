# Decisions

Choices a test cannot express: canonical identifiers, defaults, naming. One
row each, decided **before** code builds on them (see workspace `AGENTS.md`,
"Spec before code"). A decided row records the decision and the law/test it
materialized into; nothing here carries a count or a status narrative.

| # | Question | Decision | Materialized as |
|---|---|---|---|
| D-1 | Canonical automation identifier — config `id`, alias, or `entity_id`? `--pattern` rejects the config id `auto show` prints (R2/T10). | DECIDED 2026-07-27: accept all three everywhere (H-17's pole); config `id` is the canonical printed form | `TestAutomationRefSurfaceIsClosed` (autoref surface — every automation-target entrypoint reaches `resolveAutomation`), `TestPatternAcceptsEveryIdentifierHactlPrints` (`make test-int`), the `…AcceptsTheAliasHactlPrints` pair, `TestAutoRollbackAcceptsTheIdentifierAutoLsPrints`, `TestAutoDeleteSendsTheCompanionAnIdItAccepts`; law: H-17 |
| D-2 | Filter case pole — all four `--pattern` flags are case-sensitive while every `--name`/`--area`/`--label` sibling is not. | DECIDED 2026-07-27: all filters case-insensitive | `TestFilterFlagsAgreeOnCase` (internal/cmd/surface_filter_test.go) — asserts the case-insensitive pole over every filter probe, not parity |
| D-3 | `dash show` default view (R17). | OPEN | — |
| D-4 | Logbook actor field name — `changed_by` vs `who` (R20). | OPEN | — |
| D-5 | `anom:` identifiers — give them a consumer or delete them (R19; deadcode gate holds the allowlist). | OPEN | — |
| D-6 | `ref replace` and the default dashboard — skip silently (today) or refuse loudly (D71). | OPEN — proposal: refuse unless `--allow-partial`, mirroring `ref validate` | — |
