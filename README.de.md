# 🐼 PANDA

**P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> Jedes Gerät, jede Rechenleistung, ein Befehl.
> Ein persönlicher Task-Orchestrierungs-Assistent, der als Peer-to-Peer-Netzwerk
> aus Nodes über deine heterogenen Geräte hinweg läuft.

[English](README.md) · [简体中文](README.zh-CN.md) · [Deutsch](README.de.md)

---

## Was ist PANDA?

PANDA macht aus jedem Gerät, das dir gehört — Laptop, Einplatinencomputer, Desktop —
einen *Node* in deinem persönlichen Task-Netzwerk. Du stellst einmal eine Anfrage,
von welchem Gerät auch immer, und PANDA delegiert den Task an den Node, der ihn am
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
| Go | 1.22+ (getestet auf 1.26.5) |
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
cp config.example.yaml /etc/panda/config.yaml   # oder lokal behalten und per --config setzen
```

Die Config ist klein und selbsterklärend. Das Wichtigste:

```yaml
network:
  listen_addr: ":7836"        # WebSocket-Listener
  peers:                      # weitere Nodes im Netzwerk
    - "orangepi3b.tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # beliebiger /v1/messages-kompatibler Endpoint
  model: "deepseek-chat"
  # api_key: ""               # bevorzugt über die Env-Variable PANDA_MODEL_API_KEY
```

Geheimnisse (Modell-API-Keys) werden möglichst über `PANDA_MODEL_API_KEY` gelesen,
nicht aus der Config-Datei.

### Daemon starten

```bash
./bin/panda --config config.yaml --card config/capabilities.macbook.yaml
```

Jeder Node, der Arbeit *ausführen* kann, sollte mit seiner Capability Card starten.
Ein Node ohne Card nimmt weiterhin am Heartbeat teil, bekommt aber keine Tasks zugewiesen.

### Kurztour

```bash
# Beliebige Frage — das Eingabemodell entscheidet: antworten, Tool oder delegieren
./bin/panda ask "fasse den git log der letzten Woche zusammen"

# Netzwerk und Warteschlange prüfen
./bin/panda status
./bin/panda queue

# Task im Detail ansehen / abbrechen
./bin/panda task <task-id>
./bin/panda cancel <task-id>
./bin/panda logs <task-id>

# Skills verwalten
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
| `panda logs [--config PATH] <task-id>` | Task-Ausführungslogs |
| `panda skill` | Skill-Store-Verwaltung |
| `panda version` | Version anzeigen |

## Konfigurations-Referenz

| Sektion | Schlüssel | Bedeutung |
|---|---|---|
| `node` | `name` | Eindeutige Node-ID (netzwerkweit) |
| `node` | `resource_class` | `Micro` \| `Standard` \| `Full` → Scheduler-Tier |
| `network` | `listen_addr` | WebSocket-Listener-Adresse |
| `network` | `panel_addr` | HTTP-Adresse des PWA-Panels (leer = deaktiviert) |
| `network` | `peers` | Manuelle Peer-Adressen zum Anwählen |
| `storage` | `db_path` | SQLite-Datenbankpfad |
| `storage` | `context_path` | Context-Snapshot-Speicher |
| `storage` | `memory_path` | Persönliches Gedächtnis (Phase 3) |
| `storage` | `projects_path` | Pro-Projekt-Gedächtnis (Phase 3) |
| `storage` | `skills_path` | Prozedurales Gedächtnis (Phase 3) |
| `storage` | `work_path` | Ausführungsverzeichnis der Agents; Scope-Drift wird hier gemessen |
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` |
| `model` | `base_url` | Anthropic-kompatible Messages-API-Basis-URL |
| `model` | `model` | Modell-ID (z. B. `deepseek-chat`, `deepseek-reasoner`) |
| `model` | `api_key` | Geheim — bevorzugt `PANDA_MODEL_API_KEY` |

Ladereihenfolge der Config: `--config`-Flag > Umgebungsvariable > Standard `/etc/panda/config.yaml`.

## Verzeichnisstruktur

```
cmd/panda/            Daemon-Einstieg + CLI-Subkommandos
internal/
  core/               Node-Lebenszyklus, State Machine, Message-Routing, lokale Ausführung
  entry/              einheitliches Eingabemodell (klassifizieren · validieren · fallback)
  bus/                WebSocket-Transport + Message-Envelope
  commander/          3-Ebenen-Ausführung (native / agent / manual)
  scheduler/          Delegations- & Routing-Entscheidungen
  defense/            Berechtigungs-Tiers, Circuit Breaker, Drift- & Loop-Erkennung
  security/           Ausführungshärtung (Sandbox, Allowlists, Audit)
  panel/              HTTP-API des PWA-Panels
  ledger/             Fähigkeitsverzeichnis (Cards, Heartbeat, Employee-Cache)
  ctxstore/           Context-Snapshot-LRU
  memory/             Zwei-Ebenen-Gedächtnis + Dreaming-Engine
  skills/             prozedurales Gedächtnis (SKILL.md-Selbst-Evolution)
  config/             YAML-Config-Laden
  storage/            SQLite (WAL)-Wrapper + Migrationen
  log/                strukturierte JSON-Logs (slog)
  util/               UUIDv7
adapters/             Agent-Adapter (claude_code.py, opencode.py)
extensions/voice/     Voice-Sidecar (Wake / STT / TTS / VAD)
web/pwa/              PWA-Frontend (Manifest + Service Worker + Panel-Views)
config/               Beispiel-Capability-Cards (macbook, orangepi3b)
testdata/             Multi-Node-Loopback-Testkonfigurationen
```

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

PANDA zielt auf stromsparende Geräte. Vor dem Einsatz auf Hardware sollte der
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

## Status

Phase 3 (Gedächtnis + Sprache + Sicherheit) läuft. Die Gedächtnis-Schicht, die
Dreaming-Engine, das Skill-System, das PWA-Panel und die Ausführungshärtung sind
implementiert; die Spracheingabe ist code-komplett und wartet auf die Validierung
mit Mikrofon-Hardware.

## Danksagung

Inspiriert von der Theorie des verteilten Multi-Agent-Schedulings (ATC-MARL) und
von den Gedächtnismustern von Hermes und OpenClaw. Gebaut von Xenith.
