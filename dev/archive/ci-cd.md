# Claude Code Prompt: Go CLI Release- & Security-Setup

## Kontext
Ich habe ein CLI-Tool in Go entwickelt, das auf GitHub liegt. Richte die komplette Release- und Security-Pipeline für Linux, macOS und Windows ein. Nutze dazu den GitHub-Token aus der Umgebungsvariable `GITHUB_TOKEN` (oder `GH_TOKEN`) für alle `gh`-CLI-Aufrufe.

## Ziele
1. Multi-Platform-Builds (linux/darwin/windows × amd64/arm64) via **GoReleaser**
2. Automatisches Security-Scanning mit **govulncheck**, **gosec**, **Dependabot** und **CodeQL**
3. Release-Integrität durch **Checksums**, **Cosign-Signaturen (keyless)** und **SBOM** (Syft)
4. Gehärteter GitHub-Actions-Workflow

## Vorgehen

### Phase 1: Analyse (erst bewerten, dann handeln)
- Lies `go.mod`, vorhandene Workflows in `.github/workflows/`, und bestehende Config-Dateien
- Prüfe ob GoReleaser, Dependabot, SECURITY.md schon existieren
- **Zeige mir deinen Plan und frage nach Bestätigung, bevor du Dateien erstellst**
- Frage nach: Binary-Name, Homepage/Repo-URL, License (falls nicht in LICENSE-Datei erkennbar)

### Phase 2: Dateien erstellen
Erstelle/aktualisiere folgende Dateien (nur was noch fehlt — existierende nicht überschreiben ohne Rückfrage):

1. **`.goreleaser.yaml`**
   - Builds für linux/darwin/windows × amd64/arm64
   - Archive (tar.gz für unix, zip für windows)
   - Checksums (SHA256)
   - Changelog aus Commits
   - SBOM via Syft
   - Cosign-Signaturen (keyless, OIDC)

2. **`.github/workflows/release.yml`**
   - Trigger: Push von Tag `v*`
   - Go + Syft + Cosign installieren
   - GoReleaser ausführen
   - Minimale `permissions:` (nur `contents: write`, `id-token: write`, `packages: write`)
   - Actions per SHA pinnen (aktuelle SHAs selbst nachschlagen, nicht raten)

3. **`.github/workflows/security.yml`**
   - Trigger: PR, Push auf main, wöchentlicher Cron
   - `govulncheck ./...`
   - `gosec ./...`
   - CodeQL-Analyse für Go

4. **`.github/dependabot.yml`**
   - Ecosystem: `gomod` (wöchentlich)
   - Ecosystem: `github-actions` (wöchentlich)

5. **`SECURITY.md`**
   - Schlicht gehalten: wie Schwachstellen gemeldet werden (Private Vulnerability Reporting via GitHub)
   - Unterstützte Versionen

### Phase 3: GitHub-Konfiguration via `gh` CLI
Nutze den Token aus `GITHUB_TOKEN`/`GH_TOKEN` und führe aus:

- Dependabot Security Updates aktivieren: `gh api -X PUT /repos/{owner}/{repo}/automated-security-fixes`
- Vulnerability Alerts aktivieren: `gh api -X PUT /repos/{owner}/{repo}/vulnerability-alerts`
- Private Vulnerability Reporting aktivieren: `gh api -X PUT /repos/{owner}/{repo}/private-vulnerability-reporting`
- Branch Protection für `main` (wenn sinnvoll und noch nicht vorhanden — **vorher fragen**)

Repo-Name/Owner aus `git remote get-url origin` ableiten.

### Phase 4: Validierung
- `goreleaser check` lokal ausführen
- `goreleaser release --snapshot --clean` als Dry-Run
- Workflow-YAMLs mit `actionlint` prüfen, falls verfügbar
- Ergebnis-Report: was wurde erstellt, was ist noch manuell zu tun

## Randbedingungen
- **Keine Secrets/Keys** committen — Cosign läuft keyless über OIDC
- **Keine Aktionen per Tag pinnen**, immer per SHA
- Wenn etwas unklar ist: **fragen, nicht raten**
- Zeige vor jedem `gh api`-Write-Call was gemacht wird
- Keine zusätzlichen Features hinzufügen, die ich nicht angefragt habe

## Nicht tun
- Kein erstes Release-Tag pushen — das mache ich manuell nach Review
- Keine bestehenden Workflows überschreiben ohne Rückfrage
- Keine Homebrew-Tap / Scoop / AUR / Docker-Images aufsetzen (kann später folgen)