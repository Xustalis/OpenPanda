# 🐼 OpenPanda

**Open-Source, Local-First Betriebssystem für persönliche KI-Agenten und geräteübergreifender Orchestrator**

> Verbinde alle deine Geräte zu einem privaten Peer-to-Peer-Mesh und „heuere“ Terminal-KI-Coding-Agenten (Claude Code, Codex, Grok etc.) an, um als einheitliches Team maschinenübergreifend zusammenzuarbeiten.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release](https://img.shields.io/github/v/release/Xustalis/OpenPanda?label=release&color=blue)](https://github.com/Xustalis/OpenPanda/releases)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-00ADD8)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-3776AB)
![Platforms](https://img.shields.io/badge/Plattformen-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Memory](https://img.shields.io/badge/Speicherbedarf-~20MB%20RSS-brightgreen)
![Local First](https://img.shields.io/badge/Cloud--Abh%C3%A4ngigkeit-Keine%20(Local--First)-success)

---

## ⚡ Warum OpenPanda?

Heutige KI-Coding-Assistenten (**Claude Code, OpenAI Codex, Grok Build, OpenCode**) sind enorm leistungsfähig, jedoch **in einem einzelnen Terminal auf einer einzigen Maschine gefangen**.

In der Praxis besteht dein täglicher Arbeitsablauf jedoch aus heterogenen Geräten:
- Ein leichtes **Laptop** zum Entwerfen von Ideen und Prüfen von Code.
- Eine **Workstation oder ein Linux-Server** mit leistungsstarken CPUs/GPUs für schwere Builds, Docker und KI-Training.
- Ein **Raspberry Pi oder SBC** für 24/7-Hintergrunddienste, Sensoren und IoT.

**OpenPanda schließt diese Lücke.** Es ersetzt bestehende CLI-Agenten nicht, sondern **stellt sie an**:

```
┌─────────────────────────────────────────────────────────────┐
│                      Du: Ein einziger Befehl                │
│             (Terminal-TUI / Web-Konsole / Sprache)          │
└──────────────────────────────┬──────────────────────────────┘
                               │
                  ┌────────────▼────────────┐
                  │     🐼 OpenPanda OS     │
                  │   Routen, Orchestrieren,│
                  │    Prüfen & Absichern   │
                  └────────────┬────────────┘
                               │ Direkte P2P-Links (WS + versiegeltes UDP, keine Cloud)
     ┌─────────────────────────┼─────────────────────────┐
     │                         │                         │
┌────▼──────────────┐   ┌──────▼────────────┐   ┌────────▼────────────┐
│  MacBook (Worker) │   │  Linux-Build-Box  │   │  Raspberry Pi / SBC │
│  - Schnelle Tests │   │  - Schwere Builds │   │  - GPIO / Sensoren  │
│  - Claude Code    │   │  - Codex / Docker │   │  - 24/7-Daemons     │
└───────────────────┘   └───────────────────┘   └─────────────────────┘
```

Du gibst eine Anweisung von **irgendeinem** Gerät aus. OpenPanda analysiert die Aufgabe, delegiert sie an das Gerät mit den passenden Werkzeugen und Rechenressourcen, überwacht den ausführenden Agenten, prüft das Ergebnis und streamt die fertige Ausgabe zurück.

---

## 🌟 Was kann OpenPanda?

### 1. 🌐 Heterogene P2P-Gerätezusammenarbeit
- **LAN-Discovery + Bluetooth-artige Kopplung**: Knoten finden sich im LAN mit fingerprint-bestätigter Aufnahme; `panda pair` fügt ein Gerät über einen auf beiden Seiten bestätigten Kurzcode hinzu, und `panda nodes verify` pinnt den Ed25519-Schlüssel jedes Peers.
- **Dynamische Fähigkeitskarten**: Jeder Knoten erfasst automatisch sein Hardwareprofil (CPU, RAM, OS) und verfügbare Agenten.
- **Intelligente Aufgabenverteilung**: Routet nach gemessener Kapazität — schwere Kompiliervorgänge an leistungsstarke Server, Sensoraufgaben an Edge-Knoten; `panda nodes drain` entfernt einen Knoten für Wartung.
- **Worktree-reisende Delegation**: Eine an einen anderen Knoten delegierte Datei-Aufgabe trägt ihren Checkout hin und bringt das Ergebnis zurück.
- **Multi-Hop-Mesh**: Harte Pins (`--preferred`, Stufen-`node:`) und die Kontext-/Artefakt-Datenebene routen über mehrere Relays in einem Teilmesh: Ein Knoten zwei Hops entfernt wird über den gegossipelten Link-State adressiert, und ein Executor ohne Origin-Erreichbarkeit holt Snapshot und Inputs über den Hop, der ihm die Aufgabe übergab.
- **Aktor-Dispatch**: Aufgaben betätigen physische Aktuatoren auf dem Knoten mit der Hardware — `drivers/` liefert sieben Referenztreiber mit (Servo, Mikrofon, Kamera, Benachrichtigung, serieller MCU, Screen-Capture, GUI-Eingabe), mit `.ps1`-Varianten für das Desktop-Trio, sodass Standard-Windows-Knoten kein git-bash brauchen.
- **Computer-use-Paar**: Ein Knoten mit Desktop-Session bewirbt `hardware:screen_capture` (Screenshot des Bildschirms) und `hardware:gui_input` (Klick, Tippen, Taste, Scrollen — Tier 2, weil ein Klick nicht ungeschehen gemacht werden kann), sodass der Agent auf dieser Maschine seine GUI sehen und bedienen kann; headless Daemons behaupten nie einen Bildschirm, den sie nicht sehen können. Bildschirminhalte fließen nur zwischen mesh-vertrauten Knoten.
- **Abrufbare Artefakte**: Was eine Aufgabe produziert (ein Screenshot, eine Reportdatei) wird gepackt und zurückgeholt: `panda task artifact <id>` listet und entpackt es, die Konsole zeigt Bilder und Text inline.
- **Privates P2P-Netzwerk**: Direkte Kommunikation über authentifizierte WebSockets (pro Verbindung ausgehandelte AEAD-Verschlüsselung im signierten Hello) und eine verschlüsselte UDP-Datenebene mit NAT-Traversal. Dein Code und Kontext verlassen niemals deine Geräte.
- **Trust-Zonen**: Jeder Peer trägt eine vom Betreiber gesetzte Stufe (`panda nodes trust <id> mesh|guarded|open`), die begrenzt, was eine delegierte Aufgabe ihm offenlegen darf und wie eingehende Arbeit von ihm zugelassen wird; Bildschirminhalte sind durchgehend mesh-exklusiv — eine Abstufung des Vertrauens kappt eine laufende Screen-Übertragung mitten im Flug.

### 2. 🤖 Universelle Agenten-Orchestrierung & Ausfallsicherung
- **Vorkonfigurierte Adapter**: Zehn mitgelieferte Adapter (Claude Code, OpenAI Codex, OpenCode, Grok Build, DeepSeek Harness, Antigravity, Hermes, Pi, ZCode, OpenClaw), eine generic-Brücke für alles Übrige und Shell-Befehle.
- **Multi-Modell-Registry & transparentes Failover**: `panda model add|switch|test` verwaltet mehrere Provider pro Knoten hinter einem Health-Breaker; bei aufgebrauchten Kontingenten oder ungültigen Schlüsseln (401/403) wird automatisch ein Ausweichmodell injiziert und die Aufgabe läuft weiter.
- **Multi-Harness-Läufe**: `panda ask --agents a,b --mode parallel` verteilt eine Anweisung als Plan an mehrere Agenten und sammelt die Urteile ein.
- **Vollständige Transparenz**: Verfolge Bash-Befehle, Dateiänderungen und Werkzeugaufrufe live im Terminal oder Browser.
- **Geschwister-Nachrichtenbrett** (experimentell): Aufgaben unter demselben Plan oder Elternteil tauschen kurze Notizen (eine Statuszeile, ein Befund, ein Verweis auf erzeugte Dateien) über ein begrenztes Slot-Board aus; jeder Agent liest beim nächsten Durchlauf, was seine Peers posteten, statt auf deren Ergebnisse zu blockieren — ein Budget pro Episode verhindert, dass der Kanal zum Geplauder wird.

### 3. 🛡️ Autonome Sicherheit & menschliche Freigabe (Human-in-the-Loop)
- **Gestufte Risikobewertung**: Sichere, umkehrbare Aktionen (Code lesen, kompilieren, Tests ausführen) laufen autonom durch.
- **Interaktive Freigabepforten**: Irreversible Aktionen (`git push`, Datenbankänderungen, Dateilöschungen) pausieren sicher für deine Bestätigung.
- **Schutz vor Endlosschleifen**: Aktive Sicherungen verhindern unkontrollierten Token-Verbrauch bei Fehlversuchen.

### 4. 🧠 Zweischichtiger Speicher & lernende Fähigkeiten
- **Strikte Speichertrennung**: Persönliche Präferenzen (`USER.md`) sind strikt vom Projektkontext (`MEMORY.md`) isoliert.
- **Selbstverbessernde Fähigkeiten**: Erfolgreiche Abläufe werden in `SKILL.md`-Leitfäden gespeichert und mit jeder Nutzung präziser.
- **Skills Hub & autonome Erkennung**: Ein kuratierter Offline-Katalog (`panda skill hub`), Import aus Pfad, URL oder Archiv (`panda skill import`), und ein Assistent, der den Skill, den eine Aufgabe braucht, während des Laufs selbst findet und installiert.
- **Projektkontext-Roaming**: Bei der Aufgabenübergabe reisen Projektspeicher und Arbeitsbaum-Zusammenfassungen automatisch mit.

### 5. 🖥️ Einheitliche Schnittstellen & Einbettung
- **Interaktive Terminal-TUI**: Bubble Tea mit Vollbild-Alternate-Screen-Modus, Tastaturnavigation, Erststarts-Assistent, Live-Fortschritt und Richtungswechsel während der Ausführung; `PANDA_CLASSIC_REPL=1` hält die klassische Zeilen-REPL bereit.
- **Integrierte Web-Konsole**: Kanban-Board, Echtzeit-SSE-Streaming, Skills-Verwaltung, Sitzungsabbruch und automatische Anmeldung; `panda web --lan` teilt sie im LAN hinter einem temporären Token, inklusive Web-Push-Benachrichtigungen.
- **Spracheingabe** (experimentell): `panda voice` führt die Schleife Wake → Transkribieren → Dispatchen → Vorlesen über die mitgelieferten `extensions/voice`-Sidecar-Skripte aus (braucht eigene Python-Abhängigkeiten).
- **Skriptfähige CLI**: Schnelle Befehle wie `panda ask` zur nahtlosen Einbindung in eigene Skripte.
- **Selbstaktualisierung**: `panda update check|apply` (oder `panda upgrade`) prüft ein bereitgestelltes Binary und tauscht es vor Ort aus.
- **Sitzungsbäume & Forking**: `panda session fork <id> --at N` gabelt die Konversation an beliebigem Turn; in einem Repo wird der Kind-Worktree vom Eltern-Branch abgezweigt und erbt erzeugten Code. `panda session tree` zeigt die Familie, `/fork` tut dasselbe in REPL/TUI.
- **Auto-Kompaktierung**: Überlaufende Historie wird zu einem modellgeschriebenen Rollsummary gefaltet statt verworfen — der gespeicherte Verlauf bleibt vollständig.
- **`panda mcp`**: Stellt die eigenen Werkzeuge des Knotens (Aufgaben, Speicher, Skills …) als MCP-stdio-Server bereit, sodass jeder MCP-fähige Agent den Knoten über das Standardprotokoll steuern kann.
- **`panda rpc`** (experimentell): NDJSON-over-stdio-Einbettungsoberfläche (`status`, Streaming-`ask`, `session.*`); `scripts/panda_rpc.py` ist ein stdlib-Referenzclient.
- **Abo-OAuth** (experimentell): `panda auth login anthropic` meldet Claude Pro/Max per PKCE an und erneuert Token transparent.

### 6. 🪶 Extrem leichtgewichtig (~20MB Speicher)
- Einzelne statische Go-Binärdatei ohne externe Laufzeitabhängigkeiten (reines Go SQLite im WAL-Modus).
- Läuft mühelos auf 20\$-Einplatinencomputern bis hin zu großen Clustern.

---

## 🚀 Schnellstart (In 3 Minuten)

### Schritt 1: Installation

**macOS / Linux:**
```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**macOS (Homebrew):**
```bash
brew install Xustalis/openpanda/openpanda
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

**Aus dem Quellcode** (Go ≥ 1.26, ohne CGO — sauberes Cross-Compiling):
```bash
git clone https://github.com/Xustalis/OpenPanda.git && cd OpenPanda
make build && ./bin/panda init
```

Bereits installiert? `panda update` aktualisiert vor Ort; deine Daten bleiben erhalten.

### Schritt 2: Knoten initialisieren

```bash
panda init
```
*Der Einrichtungsassistent konfiguriert den Knotennamen, Modellanbieter (DeepSeek, Claude, OpenAI, Ollama etc.) und erstellt die Fähigkeitskarte.*

### Schritt 3: OpenPanda starten

- **Interaktive TUI starten:**
  ```bash
  panda
  ```
- **Web-Konsole starten (öffnet automatisch im Browser):**
  ```bash
  panda web
  ```
- **Oder direkt eine Frage stellen:**
  ```bash
  panda ask "Systemstatus prüfen und offene Aufgaben zusammenfassen"
  ```

### Zweites Gerät in 30 Sekunden verbinden

1. Auf Gerät A: `panda pair <Adresse-von-Gerät-B>` ausführen (Kopplungscode wird angezeigt).
2. Auf Gerät B: `panda pair confirm` ausführen, um denselben Code zu bestätigen und beizutreten.
*Beide Geräte sind jetzt in deinem privaten Agenten-Mesh verbunden!*

---

## 🛠️ Befehlsübersicht

| Befehl | Beschreibung |
|---|---|
| `panda` | Vollständige interaktive Bubble Tea TUI starten |
| `panda ask "<anfrage>"` | Direkt ausführen: antworten, Werkzeug nutzen oder delegieren (`--agents a,b --mode parallel` für einen Multi-Agenten-Lauf) |
| `panda web` | Eingebettete Web-Konsole starten und Browser öffnen (`--lan` zum Teilen) |
| `panda voice` | Dem Knoten etwas sagen statt tippen (experimentell) |
| `panda model` | Multi-Modell-Registry: `status`, `add`, `switch`, `test` |
| `panda nodes` | Verbundene Geräte und Fähigkeiten anzeigen; `nodes trust <id> mesh\|guarded\|open` pinnt die Peer-Stufe |
| `panda pair` | Neues Gerät per LAN koppeln (Kurzcode auf beiden Seiten bestätigt) |
| `panda queue` | Wartende, laufende und zu genehmigende Aufgaben anzeigen (`--watch` für Live-Updates) |
| `panda approve <id>` | Ausstehende irreversible Stufe-2-Aktion freigeben |
| `panda plan run <datei.yaml>` | Mehrstufige, geräteübergreifende Pipelines (`plan example` erzeugt eine Vorlage) |
| `panda task board <id>` | Geschwister-Nachrichtenboards einer Aufgabe anzeigen (Plan-/Eltern-Scope plus Kinder) |
| `panda task artifact <id>` | Ausgabe-Artefakt einer Aufgabe auflisten oder entpacken (`--list`, `--file <pfad>`, `--out <verz>`) |
| `panda session` | Sitzungen auflisten, forken und fortsetzen (`session tree` zeigt die Familie) |
| `panda project list` | Workspace-Projekte und Kontext verwalten |
| `panda card` | Fähigkeitskarte dieses Knotens anzeigen, neu scannen und bearbeiten |
| `panda skill` | Workflow-Fähigkeiten durchsuchen, importieren und installieren (Hub, URL oder Datei) |
| `panda auth login` | Modell-Abo per OAuth anmelden (z. B. `anthropic`) |
| `panda mcp` · `panda rpc` | Eigene Werkzeuge des Knotens per MCP stdio bereitstellen · NDJSON-Einbettung |
| `panda update` | Nach Updates suchen oder vor Ort selbst aktualisieren (`upgrade` wendet direkt an) |
| `panda doctor` | PATH, Konfiguration, Adapter und Datenbank prüfen |
| `panda version` | Aktuelle Binärversion ausgeben |

---

## 🗺️ Fahrplan

| Version | Thema |
|---|---|
| **v0.0.8** (stabile Basis) | Multi-Agenten-Orchestrierung auf einer Maschine, voll nutzbar: Absichtsklassifikation, Dispatch, Überwachungsschleife, Failover, gestufte Freigabe, Richtlinien für Prompt-Sprachen |
| **v0.0.9** (stabil) — "Periapsis" | Hybrid-Transport/DTN-Architektur vollendet: latenzgewichtetes Mesh-Routing, DTN-Bundles auf der Leitung, Token-Budgets, Schattenkopien, Aktuator-Pfad, UDP-Datagramm-Ebene mit mesh-koordiniertem NAT-Hole-Punching, verschlüsselte Payloads, Kontaktpläne, Ed25519-Knotenidentität, Lite-Build — plus gemerkte Genehmigungen, Execution-Attribution, ein Web-Sidebar-Node-Chip und Alltags-CLI-Politur |
| **v0.0.10** — "Apoapsis" | Hinaus ins LAN und an den Edge: Discovery mit fingerprint-bestätigter Aufnahme und TOFU-Key-Pinning, Worktree-reisende delegierte Datei-Tasks, die Klärungs-Schleife, Aktor-Dispatch mit fünf Referenztreibern (inkl. seriellem MCU), der Pi-Adapter und ein Python-freier Generic-Executor — plus forkbare Sitzungsbäume, Auto-Kompaktierung, die `panda rpc`-Einbettungsoberfläche, Abo-OAuth, signierte Event-/Audit-Ketten, OS-level Sandbox und eine Steady-State-Perf-Runde. Relizenziert zu AGPL-3.0-or-later mit kommerzieller Doppellizenz |
| **v0.0.11** — "Rendezvous" | Das Mesh selbst enger ziehen: pro Verbindung ausgehandelte AEAD-Verschlüsselung im signierten Hello, Bluetooth-artiges `panda pair`, signierte Discovery-Beacons und stabile `k:<pub>`-Identitäten, harte Node-Pins, die ihr Ziel über Multi-Hop-Routen behalten, Outbox-Verwahrung, die Urteile über Linkabbrüche hinweg erneut zustellt, Daemon-Autoritätsarbitrage, 240-s-Silence-Watchdog, ausgehandelte Binär-Datenframes — und eine Teilmesh-Datenebene, die Kontext und Artefakte entlang der Delegationskette weiterreicht. Dieser Schnitt bringt außerdem das Computer-use-Paar (`hardware:screen_capture`, `hardware:gui_input`; POSIX- und Windows-`.ps1`-Treiber, nur unter einer echten GUI-Session beworben), abrufbare Task-Artefakte (`panda task artifact`, Konsolen-Vorschau) und eine mesh-only-Vertrauensregel für Bildschirminhalte, die den Bytes durch Sends, Pulls und Stufen-Eingabeübergaben folgt |
| **v0.0.12** (Vorschau) — "Constellation" | Die Flotte als ein Himmel gezeichnet und Agenten im Formationsflug: Tasks desselben Projekts laufen parallel über deklarierte Pfad-Scopes und isolierte Worktrees unter End-zu-End-Delegationsgrenzen; ein laufender Task nimmt eine eingereihte Notiz oder ein Live-Steer an, und ein durch Runden-Budget geparkter Task kann fortgesetzt werden statt nur angenommen oder abgelehnt. Die Web-Konsole konsolidiert sich zu einem einzigen Arbeitsplatz — Queue, Pläne und Projekte verschmelzen zur Control-Seite, eine Workspace-Seite zeichnet die gesamte Flotte als Live-Topologiekarte und schickt Turns direkt in den gewählten Thread, und eine Profilseite beantwortet, wer dieser Knoten ist und wie beschäftigt er war. Darunter: prozessübergreifende Ausführungs-Leases, Kontextüberlauf-Recovery, eine Token-Politik pro Modell über einen Katalog auf dem aktuellen Generationsstand, PowerShell-Completion, pprof auf dem Daemon — sowie ein Lebenszyklus-Zaun je Dispensations-Attempt, der abgelaufene Ausführungsgewalt neu routet, die Outbox-Zustellung über Linkflattern ordnet und verhindert, dass veraltete Urteile auf rotierte Versuche treffen |
| **v0.1.0** (aktuell) — "Constellation" | Der erste stabile Schnitt der reifen Linie und der erste mit gehärteten Installern (Downgrade-Schutz auf jedem Pfad, geprüfte Release-Signatur): das Constellation-Funktionsset mit den Korrektheits- und Sicherheitskorrekturen des Release-Audits — Mehrhop-Artefaktabrufe über Relays, vom Modell gemeldete Fehler werden nicht mehr als erledigt verbucht, abgelehnte Arbeit fällt auf einen fähigen Ursprung zurück, Identitäts-Pinning im Hello, begrenzte Projektpaket-Admission, ein UDP-Ebenen-Crashfix, Lese-Verweigerung für Geheimnisse im strict-Modus und die Thread-/Skill-APIs der eigenständigen Konsole. Auf einer Mac → Relay → Cloud-Kette über WAN verifiziert, einschließlich eines 3-MiB-Projektbaums von Ende zu Ende — und seit dem ersten Schnitt: Diktat in der Konsole (`POST /api/voice/transcribe`), ein Modell pro Agent (`injection.agents`) und Gerätepanels im Arbeitsbereich. |
| **v0.0.x** — der Kernel | **Den Kernel polieren, bis jede ausgelieferte Funktion bewiesen ist.** Die Experimental-Spuren reifen durch echte Flotten-Validierung zu stabil — LAN-Discovery mit Fingerprint-Aufnahme, Pairing + Sitzungs-AEAD, Trust-Zonen, das Task-Nachrichtenbrett, Session-Bäume/RPC/OAuth, Worktree-reisende Delegation, die Klärungs-Schleife, Aktor-Treiber, Computer-Use und der Spracheingang — plus erstes echtes Zünden der Referenztreiber; Stabilität, Performance und Randfall-Feinschliff durchgehend |
| **v0.1.x** — der Assistent | **Spezialisierung zu einem Assistenten, der wirklich Dinge für dich erledigt.** Eine dünne Desktop-Hülle über dem Kernel (Tray-resident, globaler Hotkey, native Benachrichtigungen, Self-Update); die Dense-Mesh-Schicht — dynamische Plan-Rekomposition per sub-main scout→plan, TURN-artiger Relay-Fallback für symmetrische NAT, Nachrichtenbrett-Außenstände (Multi-Device-e2e, MCP-Pull, Board-Ansicht); und eine minimal ehrliche „Präsenz“ — eine Kollisionsmaschine über der Liste ungelöster Fragen auf dem Dream-Zyklus, die nur spricht, wenn es sich lohnt. Kommerzielle Qualität, Grenzen ehrlich deklariert |
| **v0.2.x** — Selbstevolution | **OpenPanda iteriert über sich selbst.** Selbstmodifikation landet als agenten-erzeugte Patch-Sets durch eine Build → Gate → Human-Merge-Pipeline, die mit dem signierten Release-Kanal koexistiert; knoten-signierte Self-Builds werden zur dritten Provenienz-Klasse unter den Trust-Zonen; `self_evolution.mode` (off → propose → auto) mit dem letzten signierten Release als permanentem Fallback — das vollständige Agenten-Selbstevolutions-Paradigma |

---

## 🔭 Vision

Die Architektur von OpenPanda ist für große heterogene Cluster ausgelegt: Steuerung von Drohnenschwärmen, Kommunikationsplanung für Satellitenkonstellationen im tiefen Raum und koordinierte Flotten autonomer Fahrzeuge. Diese Szenarien teilen ein grundlegendes Problem — **jeder Knoten hat andere Rechenleistung, andere Fähigkeiten und andere Aufgaben, und trotzdem müssen alle gemeinsam koordiniert, geplant und überwacht werden.**

Heute dient OpenPanda den Geräten und Agenten eines Entwicklers. Das langfristige Ziel ist, denselben Orchestrierungskern auf autonome Knoten in Cluster-Größe auszuweiten.

---

## 📚 Dokumentation

Die Dokumentation liegt unter [`docs/`](docs/README.md): [Projektstatus](docs/status.md) (was verifiziert vs. nur gebaut ist), die [Installationsanleitung](docs/install.md), das [FAQ](docs/faq.md), die [Bus-Protokoll](docs/protocol.md)-Referenz sowie Design-Entwürfe und historische Berichte. Die meisten internen Dokumente sind auf Chinesisch verfasst.

---

## 🤝 Mitwirken

Wir freuen uns über Beiträge aus der Community! Bitte beachte [CONTRIBUTING.de.md](CONTRIBUTING.de.md), [SECURITY.de.md](SECURITY.de.md) (maßgeblich ist die chinesische [SECURITY.md](SECURITY.md)) und [CODE_OF_CONDUCT.de.md](CODE_OF_CONDUCT.de.md), und führe `make gate` lokal aus. Nichttriviale Beiträge setzen die Zustimmung zum [CLA](CLA.de.md) voraus; Tippfehler-/Doku-Fixes und Änderungen unter `adapters/` sind ausgenommen.

---

## 📄 Lizenz

OpenPanda wird unter dualem Lizenzmodell bereitgestellt:

- **Community** — [GNU Affero General Public License v3.0 oder später](LICENSE) (AGPL-3.0-or-later)
- **Kommerziell** — proprietäre Lizenz für Closed-Source-Einbettung oder gehostete Angebote; siehe [COMMERCIAL.de.md](COMMERCIAL.de.md)

Vor der Lizenzänderung veröffentlichte Versionen bleiben unter der MIT-Lizenz verfügbar.

Die Nutzung von OpenPanda unterliegt den [Nutzungsbedingungen und dem Haftungsausschluss](TERMS.de.md) ([English](TERMS.md)).
