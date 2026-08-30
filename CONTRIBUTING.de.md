# An OpenPanda mitwirken

Danke für dein Interesse, OpenPanda zu verbessern. Dieses Dokument beschreibt
die Werkzeugkette, die Engineering-Gates, die jede Änderung durchlaufen muss,
und die Konventionen, die den Code auch bei wachsendem Umfang lesbar halten.

## Voraussetzungen

| Werkzeug | Version | Benötigt für                               |
| -------- | ------- | ------------------------------------------ |
| Go       | ≥ 1.26.5 | Kernel, CLI, Panel, Tests                  |
| Node.js  | ≥ 18    | Web-Konsole (`webui/app`)                  |
| Python   | ≥ 3.10  | Voice-Sidecar, Agent-Adapter               |

## Erste Schritte

```bash
git clone https://github.com/Xustalis/OpenPanda
cd openpanda
make run            # Daemon mit der Beispielkonfiguration starten
make web            # Konsole nach webui/panel/dist bauen (go:embed)
make build-webui    # Eigenständiges Panel-Sidecar mit eingebetteter Konsole bauen
```

Der Daemon bindet die Web-Konsole zur Build-Zeit ein. Ohne `make web` wird
eine Platzhalter-Seite eingebettet, sodass `go build` auch ohne Node läuft —
**bevor du UI-Änderungen auslieferst, immer `make web` ausführen**.

Kurzbesuch der interaktiven Oberfläche:

```bash
panda repl          # Slash-Kommandos: /tasks /approve /projects /nodes /web …
```

## Engineering-Gates

Ein Pull Request wird nur gemergt, wenn **alle** Gates grün sind. Führe sie
lokal vor dem Push aus:

```bash
make gate           # build + vet + test + race (das Merge-Gate)
gofmt -l internal/ cmd/ adapters/ webui/panel/   # darf nichts ausgeben
cd webui/app && npm run typecheck                # bei Web-Konsolen-Änderungen
```

CI führt dieselben Prüfungen als parallele Jobs aus — `lint` (gofmt + vet), `test`, `race` (über `make race-focused` auf die nebenläufigen Hotspot-Pakete beschränkt), `web` (Typecheck + Node-Tests + der Build der eingebetteten Konsole) und die Cross-Plattform-Matrix — zusätzlich bleibt dieses vollständige `make gate` der lokale Maßstab; halte beide grün.

- **`go test -race ./...` muss durchlaufen** — der Kernel ist ein nebenläufiges
  System (Peer-Registry, Task-Store, SSE-Hub); Befunde des Race-Detektors sind
  Release-Blocker, keine Warnungen.
- Kernmodule (`internal/core`, `internal/scheduler`, `internal/storage`)
  sollten nach Möglichkeit oberhalb von **~60 % Testabdeckung** bleiben.
  Bugfixes kommen mit einem Regressionstest, der vor dem Fix fehlschlägt.
- Neues Wire-Protokoll oder Delegationsverhalten bekommt einen Loopback-Test —
  sieh dir das Muster in `internal/core/dedup_test.go` und
  `scripts/smoke-delegate` an.

## Code-Konventionen

- **Fehler**: mit `%w` wrappen, mit `errors.Is/As` prüfen. Niemals einen Fehler
  stillschweigend verwerfen; niemals in derselben Funktion loggen **und**
  zurückgeben (eines von beiden).
- **Kommentare erklären das Warum, nicht das Was**. Ein Leser in sechs Monaten
  braucht die Invariante, den Trade-off oder den Incident-Referenz — keine
  Wiederholung des Codes. Nicht offensichtliche Nebenläufigkeitsentscheidungen
  (Lock-Reihenfolge, warum ein Close außerhalb des Mutex stattfindet) müssen
  an Ort und Stelle dokumentiert werden.
- **Kein toter Code, keine spekulativen Abstraktionen.** Drei ähnliche Zeilen
  schlagen ein verfrühtes Interface. Unbenutzten Code löschen, nicht
  auskommentieren.
- **Nebenläufigkeit**: jeder Mutex dokumentiert, was er bewacht. Kein Lock
  wird während I/O oder eines Channel-Sends gehalten. Sender schließen; der
  Besitzer einer Goroutine ist von deren Spawn-Stelle aus identifizierbar.
- **Sicherheit**: fail closed. Alles, was klassifiziert, autorisiert oder
  schwärzt, muss standardmäßig den restriktiven Zweig nehmen (siehe das
  Tier-Modell in `internal/defense`). Neue Config-Schlüssel mit Geheimnissen
  bekommen die 0600-chmod+Warn-Behandlung; niemals Geheimnisse loggen.

## Commit-Stil

Conventional Commits, passend zur Historie:

```
feat(cli): interaktives REPL — Slash-Kommandos, /web-Konsole, fünfssprachige i18n
fix(core): deterministisches Mutual-Dial-Dedup — 1s Connect/Disconnect-Flap beenden
feat(web): volle Konsole — Queue/Detail/Ask/Projects/Nodes + go:embed Einzelbinärdatei
```

`feat` / `fix` / `docs` / `refactor` / `chore` / `test` als Typ, Scope aus
dem Top-Level-Layout (`core`, `cli`, `web`, `scheduler`, `defense`, …). Die
Betreffzeile steht im Imperativ und ist spezifisch genug, um in
`git log --oneline` verständlich zu bleiben.

## Web-Konsole (webui/app)

- Stack: Vite + Preact + TypeScript, keine Runtime-Abhängigkeiten außer Preact.
- **Alle sichtbaren Strings laufen durch i18n.** Füge den Key zu jedem Locale
  in `webui/app/src/i18n/` hinzu (Englisch ist das Fallback; fehlende Keys
  fallen darauf zurück — also niemals einen englisch-only Key ausliefern und
  "später reparieren").
- Gleiches gilt für die CLI: `internal/i18n/messages.go`.
- Sprache hinzufügen: Englische Map kopieren, übersetzen, Locale sowohl in
  `internal/i18n/i18n.go` als auch `webui/app/src/i18n/index.ts` registrieren,
  README-Link ergänzen. Keys greifbar halten — der Key ist der Identifier.

## Pull Requests

1. Fork, Zweig von `main` (`feat/…`, `fix/…`).
2. PRs klein und einzeln halten; ein Feature und sein Refactor sind zwei PRs.
3. `CHANGELOG.md` unter `[Unreleased]` pflegen — Added / Changed / Fixed.
4. README-Aktualisierungen in fünf Sprachen sind nur Pflicht, wenn du
   sichtbares CLI-Verhalten änderst oder einen Eintrag zur Featureliste
   hinzufügst; deinen Absatz in die anderen vier README-Sprachen zu
   übersetzen, ist gerne gesehen, blockt den Merge aber nicht —
   Maintainer synchronisieren Übersetzungen.
5. `make gate` grün → PR → Review.

## Sicherheitsprobleme melden

Öffne für Schwachstellen in Transport-Auth, Tier-Modell oder Schwärzungsschicht
**kein öffentliches Issue**. Melde sie privat per Sicherheitskontakt in den
Repo-Einstellungen mit Reproduktionsschritten. Die Audit-Kette
(`panda audit verify`) ist genau dafür da, dass Fixes gegen Manipulation
verifiziert werden können.

## Lizenz

Mit deinem Beitrag stimmst du zu, dass dein Werk unter der [MIT-Lizenz](LICENSE)
des Projekts veröffentlicht wird.
