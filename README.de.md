# 🐼 OpenPanda

**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> Jedes Gerät, jede Rechenleistung, ein Befehl.
> Ein persönlicher Task-Orchestrierungs-Assistent, der als Peer-to-Peer-Netzwerk
> aus Nodes über deine heterogenen Geräte hinweg läuft.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Status](https://img.shields.io/badge/status-pre--release-yellow)

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

OpenPanda ist **kein weiteres Agent-CLI** — es ist die Schicht *darüber*: der
Haushälter für jedes deiner Geräte und jedes deiner Werkzeuge.

Claude Code, Codex, OpenCode, OpenClaw … jeder ist ein starker Agent auf *einem*
Rechner. OpenPanda konkurriert nicht mit ihnen — es **stellt sie ein**. Von
welchem Gerät aus du auch sprichst, dieses Gerät wird zum Kommandanten: es
antwortet direkt, wenn es kann, und wenn nicht, routet es den Task über dein
Netzwerk an den Node, der ihn wirklich ausführen kann — an dessen eigene Agenten
(Claude Code, Codex, …) oder direkt an die Hardware, wenn eine reine
Geräteaktion genügt (ein Servo braucht kein LLM).

```
Sub-Agenten (eine Maschine)   Agent-Orchestrierung (eine Maschine)   OpenPanda (viele Maschinen)
┌──────────────┐              ┌──────────────┐              ┌──────────────────────┐
│ Claude Code  │              │ Multi-Agent- │              │ heterogene Geräteflotte│
│ Codex …      │              │ Orchestrierung│              │ + deren Agenten      │
│              │              │              │              │ + nackte Hardware    │
└──────────────┘              └──────────────┘              └──────────────────────┘
                stromauf von allen: OpenPanda delegiert, sie führen aus
```

In der Praxis: Du stellst einmal eine Anfrage, von welchem Gerät auch immer, und
OpenPanda delegiert den Task an den Node, der ihn am besten ausführen kann,
liefert das Ergebnis zurück und merkt sich für das nächste Mal, was es gelernt
hat — wobei Projektarbeit strikt von der persönlichen Erinnerung getrennt
bleibt, damit deine Codebasis nie abdriftet, weil „der Assistent weiß, dass du
dunkle Themes bevorzugst“.

Es ist von Grund auf als **persönliches** System gebaut: keine Cloud-Abhängigkeit,
dein Speicher bleibt auf deinen Geräten, und jeder Node spricht mit seinen Peers
über direkte WebSocket-Verbindungen, die du kontrollierst.

## Hauptfunktionen

- **Heterogenes Node-Netzwerk** — jeder Node bewirbt seine tatsächlichen
  Fähigkeiten (CPU-Klasse, Shell, Agent-Adapter) über eine Capability Card; das
  Netzwerk routet jeden Task an den Node, der ihn wirklich ausführen kann.
  Entwickelt für Laptops, SBCs, Desktops und alle Plattformklassen dazwischen.
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
- **Alltagsassistent-Werkzeuge** — der Agent kann die Systemuhr lesen, Live-Wetter
  abrufen und **geplante Erinnerungen** setzen (`reminder.set`): in SQLite
  gespeichert, von einem Scanner ausgelöst, zugestellt als Web-Push-Benachrichtigungen
  und Live-SSE-Updates an jede offene Konsole. `panda reminder list/add/rm`
  verwaltet sie über die CLI.
- **MCP-Integration** — ein stdio-MCP-Server, konfigurierbar in config.yaml
  (`mcp.command`) oder auf der Einstellungsseite der Konsole; seine Werkzeuge
  werden **zur Laufzeit** in das Werkzeugset des Agenten geladen, ohne
  Daemon-Neustart.
- **Zwei-Ebenen-Gedächtnis** — pro Benutzer und pro Projekt getrennte Erinnerungen
  (`USER.md` / `MEMORY.md`-Stil) hinter einer Isolationswand, plus eine
  Hintergrund-Engine **Dreaming**, die tägliche Logs bei Leerlauf des Nodes in
  Langzeitgedächtnis konsolidiert.
- **Spracheingabe** — optionale Sidecar-Pipeline (Wake-Word → STT → LLM → TTS),
  hardware-gated und bereit für eingebettete Mikrofone.
- **Interaktives REPL + eingebettete Web-Konsole** — `panda repl` ist der
  Arbeitsplatz: freie Eingabe geht an die Ask-Engine, Slash-Befehle
  (`/tasks`, `/approve`, `/projects`, `/nodes`, `/lang` …) steuern das Panel,
  und `/web` startet die eingebettete Konsole per Klick. Die Task-Warteschlange
  ist ein **Kanban-Board** (offen / in Arbeit / in Prüfung / fertig) mit
  Inline-Freigaben, dazu Chat, Erinnerungen, eine editierbare Speicher-Seite
  (USER / MEMORY / DREAMS) und eine Einstellungsseite (Modell-Endpoint —
  Anthropic- oder OpenAI-kompatibel — und MCP-Server). `panda web` ist der
  Ein-Befehl-Weg: standardmäßig Loopback-Bind + temporäres Token, der Browser
  öffnet sich bereits angemeldet. Fünf UI-Sprachen.
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
                        │   Du: CLI / Web / Voice   │
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
          │   z. B. Laptop (Standard)│     │   z. B. SBC (Micro)     │
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
| Go | 1.26.5+ |
| Python | 3.10+ (Agent-Adapter / Voice-Sidecar) |
| make | beliebige neuere Version |

### Build

```bash
make build          # natives Binary → bin/panda (release, stripped)
make web            # eingebettete Web-Konsole ins Binary (benötigt node/npm; ohne = Hinweisseite)
make test           # komplette Test-Suite
make vet            # statische Analyse
```

Cross-Compile für die Geräte, die du wirklich betreibst:

```bash
make build-linux-arm64   # → bin/panda-linux-arm64  (SBCs, eingebettete Boards)
make build-linux-amd64   # → bin/panda-linux-amd64
make build-darwin-arm64  # → bin/panda-darwin-arm64
make build-windows-amd64 # → bin/panda-windows-amd64.exe
```

### Konfiguration

Interaktiv einrichten — Modell-Endpoint, Node-Name und Capability-Karte
entstehen in einem einzigen Dialog:

```bash
./bin/panda init
```

Oder die Beispiel-Config kopieren und pro Node anpassen:

```bash
cp config.example.yaml /etc/openpanda/config.yaml   # oder lokal behalten und per --config setzen
```

Die Config ist klein und selbsterklärend. Das Wichtigste:

```yaml
network:
  listen_addr: ":7836"        # WebSocket-Listener
  shared_secret: "..."        # HMAC-Authentifizierung zwischen Nodes — alle teilen denselben Wert
  peers:                      # weitere Nodes im Netzwerk
    - "worker-1.your-tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # beliebiger /v1/messages-kompatibler Endpoint
  model: "deepseek-chat"
  # api_key: ""               # bevorzugt über die Env-Variable OPENPANDA_MODEL_API_KEY
```

Geheimnisse (Modell-API-Keys) werden möglichst über `OPENPANDA_MODEL_API_KEY` gelesen,
nicht aus der Config-Datei.

### Starten

Der schnellste Weg, das ganze System zu sehen, ist die Ein-Befehl-Web-Konsole:
Loopback-Bind, temporäres Token, der Browser öffnet sich bereits angemeldet —
kein Config-Editieren, kein Token-Kopieren:

```bash
./bin/panda web
```

Ist noch kein Modell-Endpoint konfiguriert, verwaltet ihn die Einstellungsseite
der Konsole direkt (Anthropic- oder OpenAI-kompatibel).

Für ein residentes Multi-Node-Setup starte den Daemon selbst:

```bash
./bin/panda daemon --config config.yaml --card config/capabilities.example-desktop.yaml
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
| `panda` (ohne Argumente) | Öffnet die interaktive REPL (wie `panda repl`); der Daemon läuft jetzt über den Subcommand `panda daemon` |
| `panda daemon [--config PATH] [--card PATH]` | Daemon starten: Node registrieren, Heartbeat, WS-Server, Peer-Reconnect |
| `panda ask [--config PATH] [--card PATH] [--authorize] "<Frage>"` | Einheitliches Eingabemodell: klassifiziert in answer / tool_call / task und führt aus |
| `panda repl [--config PATH] [--card PATH]` | Interaktive Shell: Slash-Befehle (tasks/approve/projects/nodes/lang), freie Eingabe geht an die ask-Engine, `/web` startet die eingebettete Konsole |
| `panda web [--config PATH] [--card PATH] [--no-browser]` | Web-Konsole mit einem Befehl: standardmäßig Loopback + flüchtiges Token, der Browser öffnet sich bereits angemeldet |
| `panda init` | Interaktive Ersteinrichtung: erzeugt `config.yaml` + `capabilities.yaml` (Modell-Endpoint, Node-Name, Hardware-Scan-Standardwerte) |
| `panda install [--dir PATH] [--no-path]` | Registriert `panda` als globales Kommando im PATH (überlebt Neustarts) und verifiziert die installierte Kopie automatisch |
| `panda uninstall [--config PATH] [--yes] [--no-backup] [--dry-run]` | Sichere Deinstallation: erst der volle Plan, dann zwingend `confirm`, Löschung nur nach Whitelist, Nutzerdaten (projects/memory/skills) bleiben immer erhalten, Zip-Backup und Bericht |
| `panda doctor [--config PATH]` | Selbstcheck: installierte Kopie läuft, PATH löst auf, Persistenz überlebt den Neustart, Konfiguration/Datenbank nutzbar |
| `panda status` | Node- & Task-Status |
| `panda queue` | Task-Warteschlange anzeigen |
| `panda task [--config PATH] <task-id>` | Task-Details |
| `panda cancel [--config PATH] <task-id>` | Task abbrechen (kaskadiert an den ausführenden Node) |
| `panda approve [--config PATH] <task-id>` | Review-Task freigeben (review → done) |
| `panda reject [--config PATH] [--reason s] <task-id>` | Review-Task ablehnen |
| `panda logs [--config PATH] <task-id>` | Task-Ausführungslogs |
| `panda skill` | Skill-Store-Verwaltung |
| `panda reminder list \| add \| rm` | Erinnerungen: auflisten / anlegen (`--after 10m` oder `--at "2006-01-02 15:04"`) / löschen |
| `panda detect [-o PATH]` | Scannt die Hardware dieser Maschine (CPU/RAM/GPU/Agent-CLIs) in einen capabilities.yaml-Entwurf |
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
| `network` | `panel_addr` | HTTP-Adresse der Web-Konsole (`panda web` / `/web`); Standard `127.0.0.1:7840` |
| `network` | `panel_token` | Bearer-Token für `/api/*` der Konsole (Loopback erzeugt ein temporäres; bevorzugt `OPENPANDA_PANEL_TOKEN`) |
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
| `model` | `api_type` | `anthropic` \| `openai` (Standard `anthropic`) |
| `model` | `max_tokens` | Completion-Token-Limit (Standard 4096) |
| `mcp` | `command` | Kommandozeile des stdio-MCP-Servers (leer = deaktiviert); Werkzeuge werden heiß in das Agenten-Set geladen |
| `push` | `enabled` | `/api/push/*` bereitstellen und Web Push senden (eingebettete Konsole + webui-Sidecar) |
| `push` | `vapid_subject` | VAPID-Subject (z. B. eine `mailto:`-Adresse) |
| `push` | `vapid_key_path` | Pfad des VAPID-Schlüssels (wird beim ersten Start automatisch erzeugt) |

Ladereihenfolge der Config: `--config`-Flag > Umgebungsvariable > Standard `/etc/openpanda/config.yaml`.

## Dokumentation

Die vollständige Dokumentation liegt im [`docs/`](docs/)-Verzeichnis:

- [Dokumentationsindex](docs/README.md) — Einstiegspunkt für die öffentlichen Dokumente.
- [Beitragsleitfaden](CONTRIBUTING.md) — Toolchain, Engineering-Gates,
  Code-Konventionen und die PR-Checkliste
  (Übersetzungen: `CONTRIBUTING.de.md` / `CONTRIBUTING.zh-CN.md` / `CONTRIBUTING.ja.md` / `CONTRIBUTING.es.md`).
- [Desktop- & Paketierungs-Roadmap](docs/plans/roadmap-desktop-and-packaging.md) —
  der gestufte Plan zu nativem Desktop-Client, signierten Installern,
  Notarisierung und Auto-Updates.

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
  ./bin/panda daemon --config testdata/node-a.yaml >/dev/null 2>&1 &
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
| Frontend | Web-Konsole (Vite + Preact, `go:embed` im einzelnen Binary) |
| LLM-Zugriff | Anthropic-kompatibler `/v1/messages`- oder OpenAI-kompatibler Endpoint (z. B. DeepSeek) |

## Roadmap

Die Phasen 0–3 (Eingangsmodell · P2P-Delegation · Speicher/Sprache/Ausführungshärtung · Kernel/Konsole/REPL-Neuaufbau plus Live-Verifikation zweier Knoten) sind abgeschlossen. Phase 4 (Desktop-Client + Pipeline signierter Installer + Auto-Update-Mechanismus + Release-Kanäle) wird detailliert im [Desktop- & Paketierungs-Roadmap](docs/plans/roadmap-desktop-and-packaging.md) geplant.

## Mitwirken

Beiträge sind willkommen. Damit die Codebasis konsistent bleibt, bitte vor einem
Pull Request die Engineering-Gates beachten:

- `make vet && make test` müssen bestehen.
- `gofmt -l internal/ cmd/ adapters/` muss leer sein.
- Testabdeckung der Kern-Module möglichst über ~60 % halten.

Die vollständigen Konventionen stehen im
[Beitragsleitfaden](CONTRIBUTING.md): Error-Wrapping
(`%w` / `errors.Is`), Komplexitäts-Limits, kein toter Code, Concurrency-Regeln,
i18n-Regeln und Commit-Stil. Übersetzungen des Leitfadens:
[`CONTRIBUTING.de.md`](CONTRIBUTING.de.md)、[`CONTRIBUTING.zh-CN.md`](CONTRIBUTING.zh-CN.md)、[`CONTRIBUTING.ja.md`](CONTRIBUTING.ja.md)、[`CONTRIBUTING.es.md`](CONTRIBUTING.es.md)。

## Lizenz

Veröffentlicht unter der [MIT-Lizenz](LICENSE).

## Danksagung

Inspiriert von der Theorie des verteilten Multi-Agent-Schedulings (ATC-MARL) und
von den Gedächtnismustern von Hermes und OpenClaw. Gebaut von Xenith.
