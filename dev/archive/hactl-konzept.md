# hactl – Konzept

Go CLI für Home Assistant Analyse & Entwicklung, optimiert für LLM tool_use via Claude Code.
Single-Binary, keine Runtime-Abhängigkeiten. Eine HA-Instanz = ein Verzeichnis.

## Ziel in einem Satz

LLM-freundliches Single-Binary-CLI, das verdichtete Sichten auf HA liefert (Funktion, Automationen, Scripts, Custom Components) und kontrollierten Write-Pfad für Templates/Automationen bietet – alles in wenigen hundert Tokens pro Aufruf.

## Leitlinien für Token-Effizienz

1. **Summary-first, drill-down on demand**
   Default = verdichtet (Counts, Top-N, Flags, Anomalien). Rohdaten nur mit `--full`, `--raw`, oder gezieltem Follow-up.

2. **Stable IDs überall**
   Jedes Item (Trace, Log-Event, Entity-Gap, Issue) bekommt kurze stabile ID für Folge-Calls. Beispiel: `trc:a7`, `iss:g3`, `ent:sensor.wp_vl`.

3. **Kompaktes Tabellenformat**
   Feste Spalten, ein Header, dann Zeilen. Keine Wiederholung von Labels pro Zeile. Bei vielen Items: ranked Top-N + `…+23 more`.

4. **Aggregate schlagen Raw**
   Log-Errors dedupliziert mit Count statt 100 gleiche Zeilen. Zeitreihen auto-resampled zum gewünschten Zeitfenster. Trace-Steps kollabiert, bis man reinzoomt.

5. **Diff statt Dump**
   State-Changes, Config-Änderungen, Automation-Verhalten: was hat sich verändert vs. letzter Baseline/Lauf.

6. **Scope-Defaults, die fast immer stimmen**
   `--since 24h`, `--top 10`, `--traces 5`. Explizit überschreibbar.

## Instanz-Modell

Eine HA-Instanz = ein Ordner. `hactl` sucht Instanz in folgender Reihenfolge:

1. `--dir <path>` Flag
2. `HACTL_DIR` env
3. Aktuelles Arbeitsverzeichnis (wenn `.env` + `cache/` vorhanden)
4. `~/.hactl/default/`

### Ordnerstruktur pro Instanz

```
./home/                     ← Instanz-Ordner (beliebig benannt)
  .env                      ← HA_URL, HA_TOKEN, optional SMB-Pfad, TZ
  hactl.yaml                ← optionale Zusatzconfig (Patterns, Aliases)
  cache/
    traces.db               ← SQLite
    timeseries.db           ← SQLite (oder parquet wenn groß)
    logs.jsonl              ← Ringbuffer
    ids.json                ← Stable-ID-Registry, persistent
    meta.json               ← Sync-Timestamps, HA-Version
  backups/                  ← YAML-Backups vor Writes
    2026-04-17T09-42_climate_schedule.yaml
```

### Mehrere Instanzen

```
~/ha/
  home/       .env  cache/  backups/
  fewo/       .env  cache/  backups/
  testbed/    .env  cache/  backups/
```

Nutzung:

```
cd ~/ha/home  && hactl health
cd ~/ha/fewo  && hactl auto ls --failing
hactl --dir ~/ha/testbed trace show trc:a7
```

Keine globale Config, keine Profile, kein Switching. Ordner = Instanz.

## Command-Oberfläche

Alle Commands defaulten auf **compact text**. Globale Flags: `--dir`, `--since`, `--top`, `--full`, `--json`.

### Health & Overview
```
hactl health                        # Gesamtbild: Boot, Recorder, Errors, Issues
hactl issues                        # Aktive HA-Issues + Repairs
hactl changes --since 24h           # Was wurde zuletzt geändert
```

### Automations & Scripts
```
hactl auto ls                       # id | state | last_run | errors | runs_24h
hactl auto ls --failing             # nur problematische
hactl auto show <id>                # Summary + letzte 5 traces (1-Zeile each)
hactl trace show <trace-id>         # condensed steps
hactl trace show <trace-id> --full  # alles, raw
hactl script ls                     # analog
```

### Entities & Zeitreihen
```
hactl ent ls --pattern sensor.wp_*  # kompakte Liste
hactl ent show <id>                 # Profile: range, gaps, last_change, linked
hactl ent hist <id> --since 7d      # Auto-resampled (~50 Punkte default)
hactl ent hist <id> --resample 5m
hactl ent anomalies <id>            # Gaps, Stuck-Values, Spikes
```

### Logs
```
hactl log --errors --unique         # dedupliziert, count + last_seen
hactl log --component zha
hactl log show <log-id>
```

### Custom Components
```
hactl cc ls                         # installierte CC mit version + health
hactl cc show <name>                # Issues, Warnings, linked entities
hactl cc logs <name> --unique
```

### Templates / Jinja
```
hactl tpl eval '{{ states("sensor.foo") | float * 2 }}'
hactl tpl eval -f my_template.j2 --at 2026-04-15T18:00
hactl tpl lint automations/heizung.yaml
```

### Dev-Loop
```
hactl auto dryrun -f new_auto.yaml --events "e1,e2"
hactl auto diff <id> -f new.yaml
hactl auto apply -f new.yaml                    # dry-run + diff preview
hactl auto apply -f new.yaml --confirm          # schreibt + reloaded
hactl rollback                                   # letztes Write zurück
```

### Cache
```
hactl cache status                  # age, size pro Kategorie
hactl cache refresh [traces|hist|logs]
hactl cache clear
```

## Output-Konventionen

**Tabellenzeile = eine Zeile.**

```
id                       state  runs_24h  errors  last_err
climate_schedule         on     12        3       09:42 cond_false
alarm_morning            off    0         1       03:11 tmpl_err
vent_boost               on     47        1       11:05 srv_timeout
```

**Trace condensed** (Default):

```
trc:a7  automation.climate_schedule  2026-04-16 09:42  FAIL
 1 trigger  time         09:42:00
 2 cond     state==home  true
 3 cond     tmpl         FAIL  → 'unknown' not float
 X action   service_call skipped
```

**Stable IDs** sind persistent in `cache/ids.json` und bleiben zwischen Aufrufen gültig, bis Cache refreshed wird.

**Zeitstempel**: kurz (`09:42`, `04-16 09:42`), ISO nur bei `--full`.

**Keine Emojis, keine Farben** im LLM-Output. `--color` für Human-Mode.

## Write-Pfad (Safety)

1. `apply` ohne `--confirm` = Dry-Run mit Diff-Preview + Validate-Check.
2. `--confirm` schreibt + reloaded.
3. Automatisches Backup nach `backups/` vor jedem Write.
4. `hactl rollback` kehrt letztes Write zurück.

Validate-Check nutzt HA `check_config` Service vor Reload.

## Architektur (Go-Module)

```
cmd/hactl/              ← main, cobra root
internal/
  config/               ← .env, hactl.yaml laden, Instanz-Discovery
  haapi/                ← REST + WebSocket Client, Auth, Retry
  cache/                ← SQLite-Zugriff, TTL, Refresh-Logik
  analyze/              ← Anomalien, Trace-Condenser, Log-Deduper
  format/               ← Tabellen-Renderer, ID-Registry, Compact/Full/JSON
  writer/               ← YAML-Diff, Backup, Validate, Reload
  cmd/                  ← Subcommand-Implementierungen (health, auto, ent, ...)
pkg/
  ids/                  ← Stable-ID-Generator (kurz, stabil, lesbar)
```

### Warum Go

- Single-Binary, kein Python-Env, keine venv-Konflikte
- Schneller Start (< 50ms) – wichtig wenn LLM viele Calls macht
- Statisches Linking → einfaches Distrib (brew tap, goreleaser, ghcr)
- Gute CLI-Libs: `cobra` + `viper`, `charmbracelet/lipgloss` optional für Human-Mode

### Dependencies (geplant)

- `spf13/cobra` – Commands
- `spf13/viper` oder plain `os` – Config/Env
- `modernc.org/sqlite` – CGO-frei → echtes static binary
- `gopkg.in/yaml.v3` – YAML read/write
- Jinja-Eval: **kein** Go-Port, geht über HA `/api/template` (semantisch immer korrekt)

## Bezug zum bestehenden Python-Framework

Das existierende Framework bleibt als Referenz-Implementierung für die Analyselogik (Trace-Condenser, Anomaly-Detector, Log-Deduper). Beim Port:

- **Datenmodelle & SQL-Schema** → 1:1 übernehmen, schon bewährt
- **Analyse-Algorithmen** → nach Go portieren
- **Python-Framework** läuft parallel für Notebooks/Experimente, hactl für Produktion

## Typische LLM-Sessions (Token-Budget)

| Aufgabe | Commands | grobe Tokens |
|---|---|---|
| „Geht HA grundsätzlich gut?" | `hactl health` | ~200 |
| „Was ist mit Heizungs-Automation?" | `hactl auto show climate_schedule` → `hactl trace show trc:a7` | ~400–600 |
| „Sensor WP-Vorlauf auffällig?" | `hactl ent anomalies` → `hactl ent hist` | ~300 |
| „Neues Template + Test" | `hactl tpl eval` x3 → `hactl auto dryrun` | ~500–1000 |

## Testkonzept

Echtes HA gegen Mocks vorgezogen: Template-Engine, Trace-Format, WebSocket-Quirks und Config-Validation sind zu HA-spezifisch für sinnvolle Mocks.

### Drei Ebenen

Ebene 0 ist golangci-lint. kein test ohne vorher lint sauber

**1. Unit-Tests (Go `testing`)**
Pure Funktionen ohne HA:
- Trace-Condenser (Input: raw Trace JSON → Output: condensed Steps)
- Log-Deduper (Hash über Template-Message ohne Timestamps)
- Anomaly-Detector (Gap/Stuck/Spike aus Zeitreihe)
- ID-Generator + Registry-Persistenz
- Tabellen-Formatter
- YAML-Diff-Logik

**2. Integration-Tests gegen echten HA (testcontainers-go)**
Container wird per Test-Code hochgezogen, definierte Fixture-Config gemountet, Tests reden über REST/WebSocket dagegen.

```go
func TestAutoShow(t *testing.T) {
    ha := hatest.Start(t, hatest.WithFixture("basic"))
    defer ha.Stop()
    
    out := hactl.Run(t, "auto", "show", "climate_schedule", "--dir", ha.Dir())
    assert.Contains(t, out, "FAIL")
}
```

**3. Contract-Tests**
Dokumentieren, welche HA-Endpoints/Formate wir nutzen. Bei HA-Update wird Bruch erkennbar:
- `/api/states` Schema
- `/api/template` Verhalten  
- WebSocket `trace/list` + `trace/get` Payload
- SQLite `traces`-Tabelle Layout

### Fixtures

Im Repo unter `testdata/fixtures/`:

```
testdata/fixtures/
  basic/              ← Minimal-Setup
    configuration.yaml
    automations.yaml
    scripts.yaml
  faulty/             ← definierte Fehlerfälle
    automations.yaml  ← mit Template-Error, ungültiger Condition
  heavy/              ← viele Entities, lange Historie
    ...
```

Deterministisch → Tests sind reproduzierbar.

### HA-Versions-Matrix

CI testet parallel gegen:
- **stable (latest)** – Baseline
- **stable-1** – vorige Minor, Kompatibilität
- **beta** – Frühwarnung vor Breaking Changes

Matrix als GitHub Actions Job, Versions-Tags aus `ghcr.io/home-assistant/home-assistant`.

Bricht beta → Issue automatisch erstellt. Bricht stable → Merge blockiert.

### Lokale Entwicklung

```bash
make test           # Unit-Tests (schnell, kein Docker)
make test-int       # Integration gegen aktuelle HA-Version
make test-matrix    # alle drei Versionen (langsam, meist nur in CI)
```

Unit-Tests laufen < 1s, Integration pro Version ~30-60s (HA-Boot).

### Was später dazukommt

- ~~**Golden-File-Tests für LLM-Output-Formate**~~ ✓ implementiert (Phase 6) — `assertGolden()` + `sanitizeGolden()`, `HACTL_UPDATE_GOLDEN=1` zum Regenerieren
- **Regression-Korpus** – reale anonymisierte Traces/Logs aus produktiven Instanzen als Test-Input
- **heavy-Fixture** – viele Entities + lange Historie für Performance/Scale-Tests

## HA-Zugriff: nur über API

Bewusste Entscheidung: hactl spricht **ausschließlich über URL+Token** aus der `.env` mit HA. Kein SMB-Mount, kein direkter YAML-Filesystem-Zugriff, keine SQLite-DB-Reads aus `/config`.

**Gründe:**
- Selbstschutz: Bugs in hactl können die HA-Instanz nicht File-System-seitig zerschießen
- HA validiert jeden Write (`check_config`) bevor Reload → invalide Configs landen nie aktiv
- Gleiche Sicherheitsmodelle wie HA-eigenes Frontend
- Deploy-neutral: egal ob Docker, HAOS, VM – nur URL+Token zählt

**Wie dann YAML-Writes?**
HA hat Config-APIs für Automationen, Scripts, Scenes (die der Automation-Editor auch nutzt):
- `/api/config/automation/config/<id>` (GET/POST/DELETE)
- `/api/config/script/config/<id>`
- `/api/config/scene/config/<id>`

Für andere YAMLs (configuration.yaml, packages, templates-sensors) → aktuell aus dem Scope raus. Falls später nötig: explizit opt-in mit `HACTL_CONFIG_PATH` env, mit großem Warnhinweis.

**Traces lesen:** WebSocket `trace/list` + `trace/get`, nicht DB-Direktzugriff.

## Logs: REST polling

`/api/error_log` pollen statt WebSocket-Subscribe. Gründe:
- hactl ist **on-demand CLI, kein Daemon** – es läuft nicht kontinuierlich, subscribe wäre sinnlos
- REST-Call ist zustandslos, kein Reconnect-Handling nötig
- Immer der volle Log-Text, Deduplizierung macht hactl selbst
- Robust > Convenience

Wenn später ein Hintergrund-Daemon kommt (z.B. für Live-Monitoring), kann der WebSocket nutzen. Nicht jetzt.

## Distribution

**Phase 1 (jetzt):**
```
git clone https://github.com/.../hactl
cd hactl && go build -o hactl ./cmd/hactl
```

Oder: `go install github.com/.../hactl/cmd/hactl@latest`

**Phase 2 (später):** goreleaser für Multi-Platform-Binaries, optional Homebrew-Tap. Wenn jemand außer dir es braucht.

## Offene Punkte

- [ ] Erster Meilenstein: `health`, `auto show`, `trace show` als Proof-of-Concept
- [ ] Erste Fixture `basic` definieren
- [ ] Repo-Name / GitHub-URL festlegen
