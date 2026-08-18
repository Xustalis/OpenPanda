# 🐼 OpenPanda

**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> Jedes Gerät, jede Rechenleistung, ein Befehl.
> Ein persönlicher Task-Orchestrierungs-Assistent, der als Peer-to-Peer-Netzwerk
> aus Nodes über deine heterogenen Geräte hinweg läuft.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.22-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

---

## Inhaltsverzeichnis

- [Was ist OpenPanda?](#was-ist-panda)
- [Hauptfunktionen](#hauptfunktionen)
- [Architektur](#architektur)
- [Schnellstart](#schnellstart)
- [Verwendung](#verwendung)
- [CLI-Referenz](#cli-referenz)
- [Konfiguration](#konfiguration)
- [Dokumentation](#dokumentation)
- [Tests](#tests)
- [Deployment](#deployment)
- [Tech-Stack](#tech-stack)
- [Roadmap](#roadmap)
- [Mitwirken](#mitwirken)
- [Lizenz](#lizenz)
- [Danksagung](#danksagung)

## Was ist OpenPanda?

OpenPanda macht aus jedem Gerät, das dir gehört — Laptop, Einplatinencomputer, Desktop —
einen *Node* in deinem persönlichen Task-Netzwerk. Du stellst einmal eine Anfrage,
von welchem Gerät auch immer, und OpenPanda delegiert den Task an den Node, der ihn am
besten ausführen kann, liefert das Ergebnis zurück und merkt sich für das nächste
Mal, was es gelernt hat.

Es ist von Grund auf als **persönliches** System gebaut: keine Cloud-Abhängigkeit,
dein Speicher bleibt auf deinen Geräten, und jeder Node spricht mit seinen Peers
über direkte WebSocket-Verbindungen, die du kontrollierst.

## Hauptfunktionen

- **Heterogenes Node-Netzwerk** — jeder Node bewirbt seine tatsächlichen
  Fähigkeiten (CPU-Klasse, Shell, Agent-Adapter) über eine Capability Card; das
  Netzwerk routet jeden Task an den Node, der ihn wirklich ausführen kann.
  Entwickelt für MacBook ↔ Orange Pi 3B und alles dazwischen.
- **Einheitliches Eingabemodell** — eine Anfrage rein, drei Intents raus:
  `answer` (reine LLM-Antwort), `tool_call` (deine Tools), `task` (an einen Node
  delegiert). Automatische Intent-Klassifikation mit sanftem Fallback.
- **Drei-Ebenen-Ausführung** — `native` (direkte Shell-Ausführung),
  `agent` (Adapter-basierter Agent, z. B. Claude Code über einen
  Anthropic-kompatiblen Endpoint) und `manual` (in der Warteschlange, bis du
  freigibst oder manuell ausführst).
- **P2P-Delegationsprotokoll** — idempotente `task_id`-Schlüssel und
  versuchs-eindeutige `attempt_id`s über WebSocket + JSON, damit abgestürzte
  Wiederholungen nie doppelt ausgeführt werden.
- **Selbst-evolvierende Skills** — prozedurales Gedächtnis in `SKILL.md`-Dateien:
  ein Skill deklariert, wann er greift und wie er läuft, und kann nach jeder
  Nutzung verfeinert werden.
- **Zwei-Ebenen-Gedächtnis** — pro Benutzer und pro Projekt getrennte Erinnerungen
  (`USER.md` / `MEMORY.md`-Stil) hinter einer Isolationswand, plus eine
  Hintergrund-Engine **Dreaming**, die tägliche Logs bei Leerlauf des Nodes in
  Langzeitgedächtnis konsolidiert.
- **Spracheingabe** — optionale Sidecar-Pipeline (Wake-Word → STT → LLM → TTS),
  hardware-gated und bereit für eingebettete Mikrofone.
- **PWA-Steuerpanel** — eine Web-Konsole für Task-Warteschlange, Task-Details und
  Human-in-the-Loop-Freigaben; als Progressive Web App installierbar.
- **Defense- und Sicherheits-Schichten** — Berechtigungs-Tiers, ein
  Circuit Breaker, Scope-Drift- und Endlosschleifen-Erkennung, plus
  Ausführungshärtung: Sandboxing, Netzwerk-Allowlists, Secret-Redaction und
  Audit-Logging.
- **Schlank gebaut** — steady-state RSS ≈ **13–20 MB**, entwickelt für
  ressourcenbeschränkte Einplatinencomputer.
- **Sauberes Cross-Compiling** — ein statisches Binary pro Plattform, kein CGO
  nötig (reines-Go-SQLite über `modernc.org/sqlite`).

## Architektur

```
                        ┌───────────────────────────┐
                        │   Du: CLI / PWA / Voice   │
                        └─────────────┬─────────────┘
                                      │
                 ┌────────────────────▼────────────────────┐
                 │            entry · panda ask             │
                 │   klassifiziere: answer | tool_call | task│
                 └────────────────────┬────────────────────┘
                                      │  Delegation über WebSocket + JSON
                       ┌──────────────┴───────────────┐
                       │                              │
          ┌────────────▼────────────┐     ┌────────────▼────────────┐
          │       Worker-Node       │     │       Worker-Node       │
          │   z. B. MacBook (Full)  │     │   z. B. Orange Pi (Micro)│
          └─────────────────────────┘     └─────────────────────────┘
```

Im Inneren jedes Nodes:

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      Daemon + CLI (ask / status / queue / task …)  │
├─────────────────────────────────────────────────────────────┤
│ entry          einheitliches Eingabemodell (answer·tool_call·task)│
│ scheduler      Delegations- & Routing-Entscheidungen         │
│ commander      3-Ebenen-Ausführung: native · agent · manual  │
│ defense        Berechtigungs-Tiers · Circuit · Drift · Loops │
│ security       Sandbox · Allowlists · Redaction · Audit      │
│ memory         USER/MEMORY-Stores + Dreaming-Engine          │
│ skills         SKILL.md prozedurales Gedächtnis              │
├─────────────────────────────────────────────────────────────┤
│ bus            WebSocket-Transport + Message-Envelope        │
│ ledger         Fähigkeitsverzeichnis (Cards, Heartbeat)      │
│ storage        SQLite (WAL) + Migrationen                    │
│ log / util     strukturierte JSON-Logs, UUIDv7              │
└─────────────────────────────────────────────────────────────┘
```

## Schnellstart

### Voraussetzungen

| Werkzeug | Version |
|---|---|
| Go | 1.22+ (Modul zielt auf 1.26.5) |
| Python | 3.10+ (Agent-Adapter / Voice-Sidecar) |
| make | beliebige neuere Version |

### Build

```bash
make build          # natives Binary → bin/panda (release, stripped)
make test           # komplette Test-Suite
make vet            # statische Analyse
```

Cross-Compile für die Geräte, die du wirklich betreibst:

```bash
make build-linux-arm64   # → bin/panda-linux-arm64  (z. B. Orange Pi)
make build-linux-amd64   # → bin/panda-linux-amd64
make build-darwin-arm64  # → bin/panda-darwin-arm64
make build-windows-amd64 # → bin/panda-windows-amd64.exe
```

### Konfiguration

Beispiel-Config kopieren und pro Node anpassen:

```bash
cp config.example.yaml /etc/openpanda/config.yaml   # oder lokal behalten und per --config setzen
```

Die Config ist klein und selbsterklärend. Das Wichtigste:

```yaml
network:
  listen_addr: ":7836"        # WebSocket-Listener
  shared_secret: "..."        # HMAC-Authentifizierung zwischen Nodes — alle teilen denselben Wert
  peers:                      # weitere Nodes im Netzwerk
    - "orangepi3b.tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # beliebiger /v1/messages-kompatibler Endpoint
  model: "deepseek-chat"
  # api_key: ""               # bevorzugt über die Env-Variable OPENPANDA_MODEL_API_KEY
```

Geheimnisse (Modell-API-Keys) werden möglichst über `OPENPANDA_MODEL_API_KEY` gelesen,
nicht aus der Config-Datei.

### Daemon starten

```bash
./bin/panda --config config.yaml --card config/capabilities.macbook.yaml
```

Jeder Node, der Arbeit *ausführen* kann, sollte mit seiner Capability Card starten.
Ein Node ohne Card nimmt weiterhin am Heartbeat teil, bekommt aber keine Tasks zugewiesen.

## Verwendung

Beliebige Frage — das Eingabemodell entscheidet: antworten, Tool oder delegieren:

```bash
./bin/panda ask "fasse den git log der letzten Woche zusammen"
```

Netzwerk und Warteschlange prüfen:

```bash
./bin/panda status
./bin/panda queue
```

Task im Detail ansehen / abbrechen:

```bash
./bin/panda task <task-id>
./bin/panda cancel <task-id>
./bin/panda logs <task-id>
```

Skills verwalten:

```bash
./bin/panda skill
```

## CLI-Referenz

| Befehl | Beschreibung |
|---|---|
| `panda` (ohne Argumente) | Daemon starten: Node registrieren, Heartbeat, WS-Server, Peer-Reconnect |
| `panda ask [--config PATH] [--card PATH] [--authorize] "<Frage>"` | Einheitliches Eingabemodell: klassifiziert in answer / tool_call / task und führt aus |
| `panda status` | Node- & Task-Status |
| `panda queue` | Task-Warteschlange anzeigen |
| `panda task [--config PATH] <task-id>` | Task-Details |
| `panda cancel [--config PATH] <task-id>` | Task abbrechen (kaskadiert an den ausführenden Node) |
| `panda approve [--config PATH] <task-id>` | Review-Task freigeben (review → done) |
| `panda reject [--config PATH] [--reason s] <task-id>` | Review-Task ablehnen |
| `panda logs [--config PATH] <task-id>` | Task-Ausführungslogs |
| `panda skill` | Skill-Store-Verwaltung |
| `panda metrics [--csv]` | Delegations-Metriken exportieren |
| `panda audit [--task <id>]` | `prev_hash`-Kette des Audit-Logs oder der Events eines Tasks verifizieren |
| `panda version` | Version anzeigen |

## Konfiguration

| Sektion | Schlüssel | Bedeutung |
|---|---|---|
| `node` | `name` | Eindeutige Node-ID (netzwerkweit) |
| `node` | `resource_class` | `Micro` \| `Standard` \| `Full` → Scheduler-Tier |
| `network` | `listen_addr` | WebSocket-Listener-Adresse |
| `network` | `shared_secret` | HMAC-Geheimnis zur Node-Authentifizierung; der WS-Listener startet ohne es nicht (alle Nodes teilen einen Wert) |
| `network` | `max_connections` | Globales Limit gleichzeitiger WS-Verbindungen (0 = unbegrenzt) |
| `network` | `max_connections_per_ip` | Limit gleichzeitiger WS-Verbindungen pro Remote-IP (0 = unbegrenzt) |
| `network` | `panel_addr` | HTTP-Adresse des PWA-Panels (leer = deaktiviert) |
| `network` | `panel_token` | Bearer-Token für `/api/*` des Sidecars (bevorzugt `OPENPANDA_PANEL_TOKEN`) |
| `network` | `peers` | Manuelle Peer-Adressen zum Anwählen |
| `storage` | `db_path` | SQLite-Datenbankpfad |
| `storage` | `context_path` | Context-Snapshot-Speicher |
| `storage` | `memory_path` | Persönliches Gedächtnis |
| `storage` | `projects_path` | Pro-Projekt-Gedächtnis |
| `storage` | `skills_path` | Prozedurales Gedächtnis |
| `storage` | `work_path` | Ausführungsverzeichnis der Agents; Scope-Drift wird hier gemessen |
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` |
| `model` | `base_url` | Anthropic-kompatible Messages-API-Basis-URL |
| `model` | `model` | Modell-ID (z. B. `deepseek-chat`, `deepseek-reasoner`) |
| `model` | `api_key` | Geheim — bevorzugt `OPENPANDA_MODEL_API_KEY` |
| `model` | `max_tokens` | Completion-Token-Limit (Standard 4096) |
| `push` | `enabled` | `/api/push/*` bereitstellen und Web Push senden (nur webui-Sidecar) |
| `push` | `vapid_subject` | VAPID-Subject (z. B. eine `mailto:`-Adresse) |
| `push` | `vapid_key_path` | Pfad des VAPID-Schlüssels (wird beim ersten Start automatisch erzeugt) |

Ladereihenfolge der Config: `--config`-Flag > Umgebungsvariable > Standard `/etc/openpanda/config.yaml`.

## Dokumentation

Die vollständige Dokumentation liegt im [`docs/`](docs/)-Verzeichnis, aufgeteilt
in öffentliche und interne Teile:

- [Dokumentationsindex](docs/README.md) — Einstiegspunkt für alle Dokumente.
- [Beitragsleitfaden](CONTRIBUTING.md) — Toolchain, Engineering-Gates,
  Code-Konventionen und die PR-Checkliste.
- [Desktop- & Paketierungs-Roadmap](docs/plans/roadmap-desktop-and-packaging.md) —
  der gestufte Plan Richtung Desktop-Client.
- [Phasen-Berichte](docs/reports/) — Fortschrittsberichte für jede Phase und
  jedes Sprint.

Interne Planungs-, Design- und Audit-Dokumente bleiben aus dem öffentlichen
Repository ausgeschlossen.

## Tests

```bash
make test        # komplette Suite
make vet         # go vet
```

Die zentralen Protokoll-Invarianten sind durch echte Zwei-Node-WebSocket-Tests
abgedeckt (kein Tailscale nötig):

```bash
go test ./internal/core/ -run 'TestTwoNodeProtocol|TestDelegateIdempotent|TestCancelPropagates' -v
```

## Deployment

OpenPanda zielt auf stromsparende Geräte. Vor dem Einsatz auf Hardware sollte der
steady-state Speicherverbrauch verifiziert werden — eine einzelne `ps`-Messung
ist wegen GC-Rauschen unzuverlässig; besser mehrfach messen:

```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda --config testdata/mac-config.yaml >/dev/null 2>&1 &
  PID=$!; sleep 3
  ps -o rss= -p $PID | awk '{printf "%d MB\n", $1/1024}'
  kill -TERM $PID; wait $PID 2>/dev/null
done
```

## Tech-Stack

| Ebene | Wahl |
|---|---|
| Kern-Daemon | Go (modernc.org/sqlite — reines Go, kein CGO) |
| Kleber / Adapter | Python 3.10+ |
| Transport | WebSocket + JSON-Envelopes |
| Zustand | SQLite im WAL-Modus |
| Frontend | PWA (native Web-App + Service Worker) |
| LLM-Zugriff | Anthropic-kompatibler `/v1/messages`-Endpoint (z. B. DeepSeek) |

## Roadmap

Phase 3 (Gedächtnis + Sprache + Sicherheit) ist abgeschlossen. Die Gedächtnis-Schicht, die
Dreaming-Engine, das Skill-System und die Ausführungshärtung sind
implementiert; die Spracheingabe ist code-komplett und wartet auf die Validierung
mit Mikrofon-Hardware. Die Web-Konsole wurde mit Vite + Preact neu gebaut und
in die Binärdatei eingebettet, das interaktive REPL ist mit ihr gelandet — die
Zwei-Knoten-Delegation ist live verifiziert
([Bericht](docs/reports/delegation-loopback-2026-08-18.md)). Phase 4 (u. a.
Desktop-Client) ist in der
[Desktop- & Paketierungs-Roadmap](docs/plans/roadmap-desktop-and-packaging.md) geplant.

## Mitwirken

Beiträge sind willkommen. Damit die Codebasis konsistent bleibt, bitte vor einem
Pull Request die Engineering-Gates beachten:

- `make vet && make test` müssen bestehen.
- `gofmt -l internal/ cmd/ adapters/` muss leer sein.
- Testabdeckung der Kern-Module möglichst über ~60 % halten.

Die vollständigen Konventionen stehen im
[Beitragsleitfaden](CONTRIBUTING.md): Error-Wrapping
(`%w` / `errors.Is`), Komplexitäts-Limits, kein toter Code, Concurrency-Regeln,
i18n-Regeln und Commit-Stil.

## Lizenz

Veröffentlicht unter der [MIT-Lizenz](LICENSE).

## Danksagung

Inspiriert von der Theorie des verteilten Multi-Agent-Schedulings (ATC-MARL) und
von den Gedächtnismustern von Hermes und OpenClaw. Gebaut von Xenith.
