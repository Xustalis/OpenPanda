# Änderungsprotokoll

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## Über das Projekt

OpenPanda (**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant) ist ein persönliches Task-Orchestrierungs-Kernel: Ein `panda`-Binary läuft auf jedem deiner Geräte, die Knoten finden sich über einen authentifizierten WebSocket-Bus, ein Entry-Modell verwandelt jede Anfrage in eine direkte Antwort oder eine ausführbare Task-Spezifikation, und der Scheduler leitet jede Aufgabe an das am besten geeignete Gerät und den passenden Agenten. Die CLI ist die Hauptschnittstelle des Kernels — ein nacktes `panda` öffnet die interaktive REPL — und die Web-Konsole ist eine dünne Hülle über demselben Store und derselben Engine.

## Versionsregeln

- Versionen folgen `MAJOR.MINOR.PATCH`. Das Projekt ist in der Anfangsentwicklung (`0.0.x`): Ein Patch-Release darf Funktionen hinzufügen, Fehler beheben und — ausnahmsweise — Breaking Changes einführen, die stets unter **Breaking Changes** aufgeführt werden.
- Ein Release wird durch das Tag `vX.Y.Z` geschnitten; jeder Commit seit dem vorherigen Tag gehört zum Abschnitt der neuen Version. `[Unreleased]` sammelt die Arbeit seit dem letzten Tag.
- Jede Version wird in vier Kategorien dokumentiert: **Hinzugefügt** (neue Funktionen), **Behoben** (Fehlerkorrekturen), **Verbessert** (Verfeinerungen), **Breaking Changes** (erfordert Handeln beim Upgrade).
- Jeder Eintrag benennt die Änderung und ihre sichtbare Wirkung in ein bis drei Zeilen; der einführende Commit wird zitiert, wo das der Archäologie hilft.
- Die englische Datei ist maßgeblich. Die Übersetzungen zh-CN / ja / es / de spiegeln sie und können um ein Release kurz verzögert sein.

## [Unreleased]

## [0.0.2] - 2026-08-22

Das CLI-zuerste-Release: Das Kernel-Redesign (Stufen A–C) landet — jede Web-Fähigkeit erhält ein CLI-Pendant, die REPL wird die Vordertür, und die CLI erhält Konversationsgedächtnis, Live-Aufgabenberichte und Markdown-Rendering je nach Ausgabekanal.

### Hinzugefügt

- **CLI-Befehlsfamilien** — jede Web-Fähigkeit hat ein CLI-Pendant: `panda session | task | memory | config | agents | project`, alle teilen die Dienstschicht des Panels (a4cba5f).
- **Ressourcenbewusste lokale Aufgaben-Warteschlange** — `core.Submit` wird asynchron: Reihenfolge Drag-Sequenz → Priorität → FIFO, kontrolliert über eine Ressourcen-Sperr-Registry plus `MaxConcurrent`; Aufgaben mit disjunkten Ressourcen ziehen an einer blockierten Warteschlange vorbei; Aufgaben erhalten `priority`/`seq`/`session_id`/`resource_keys` (SQLite v9) (0e8d850).
- **REPL-Konversationsgedächtnis** — 24k-Zeichen-Budget mit paarweisem Entfernen (ein Nutzerturn wird nie ohne seine Antwort wiedergegeben), persistiert in `~/.local/state/openpanda/conversation.json`; `/new`, `/history`, `!!` und `panda ask --continue` (f0a1b9f).
- **Out-of-Band-Aufgabenberichte** — ein REPL-Watcher druckt eine ✓/✗-Zeile, sobald eine Aufgabe einen Endzustand erreicht (Board, Web-Konsole, Peer-Delegation), ohne die Eingabezeile zu stören; Inline-Asks werden nie doppelt gemeldet (f0a1b9f).
- **Live-Aufgabenboard** — `panda queue --watch` und `/tasks watch` zeichnen die Warteschlange alle 2s neu, Zeilen nach Status gefärbt; Strg-C beendet die Ansicht, nicht den Prozess (f0a1b9f).
- **`internal/mdtext`** — Markdown-Rendering je nach Ziel: ANSI-Betonung auf Farb-TTYs, Klartext für Pipes und nackte Konsolen, vor TTS immer entfernt; Streaming-Deltas werden Zeile für Zeile durch dieselben Regeln gerendert (e94f72f).
- **Live-Agenten-Fortschritt** — Adapter streamen NDJSON-Fortschrittsnotizen auf stderr, als gedrosselte `EvProgress`-Ereignisse protokolliert: `panda task <id>` und die Panel-Zeitleiste zeigen, was der Agent während der Ausführung tut (93a453a).
- **Injektions-Policy** — `injection.model: auto|always|never`: agent-native Zugangsdaten gewinnen standardmäßig; jede Injektion wird in der Aufgaben-Ausgabe angekündigt und audit-protokolliert (852b27e).
- **Kostenbewusstes Routing** — die Agentenauswahl bewertet Fähigkeit × cost_tier mit `preferred_agents`-Bonus, mit Rückfall auf den nächstbesten Agenten (852b27e).
- **Gedächtnis-Überholung** — konfigurierbare Obergrenzen (`memory.limits`), Mehrfach-Datei-Topics mit selektiver Injektion, Sedimentierung von Dreams mit niedrigem Gewicht (852b27e).
- **`internal/hwinfo`** — geteiltes Hardware-Sondierungspaket, trägt `panda detect` und den neuen Endpunkt `GET /api/self` (852b27e, 1a97fd7).
- **Panel-App-Einstellungen und Memory-Topics** — `GET/PUT /api/settings/app` mit validierter Policy-Speicherung; die Memory-API erhält Topics pro Datei; die Memory-Seite ist produktgerecht, Einstellungen gruppiert, i18n in fünf Sprachen synchron (1a97fd7).
- **`panda init`** — interaktiver Erststart, schreibt `config.yaml` + `capabilities.yaml`; `config.ResolvePath` vereinheitlicht die Auflösung (Flag > Umgebungsvariable > Benutzerkonfiguration > Systemstandard) (f5610fc).
- **Konsolen-Politur** — geteilte `PageHeader`/`ErrorState`-Komponenten, globale Toasts und Bestätigungsdialoge bei destruktiven Aktionen (45ee941).
- **REPL-Ergonomie** — Slash-Befehlsmenü unter dem Prompt, figlet-Banner in reinem ASCII, Englisch/ASCII-Rückfall bei TERM=linux und Auto-Erkennung für `--card` (`./capabilities.yaml` → `/etc/openpanda/capabilities.yaml`) (f0a1b9f).
- **`scripts/deploy-pi.sh`** — Orange-Pi-Deployment mit einem Befehl: Cross-Compile, atomarer Binärtausch, systemd-Installation, Gesundheitscheck (d7bc87f).

### Behoben

- **Adapter-Timeout über die gesamte Laufzeit** — ein mid-Stream feststeckendes CLI (Pipe offen, keine Ausgabe) blockierte die Leseschleife für immer; das Timeout deckte nur den Schwanz nach dem stdout-EOF ab. Beide Adapter starten das CLI in einer eigenen Prozessgruppe mit einem Watchdog-Thread, der zum Stichtag den ganzen Baum tötet (332f2d4).
- **Anthropic-Tools-API-Kompatibilität** — tool_use-Blöcke tragen jetzt immer `input` (leeres Objekt für Tools ohne Argumente); strenge Anthropic-kompatible Anbieter (DeepSeek /anthropic) wiesen Folgerunden zuvor mit einer 400 ab. Tool-Namen mit Punkten wurden zu Unterstrichen umbenannt, um `^[a-zA-Z0-9_-]+$` zu erfüllen (93a453a).
- **codex konnte sich unter einem nicht-interaktiven Elternprozess nicht initialisieren** (EPERM beim Schreiben seiner Zustands-DB und PATH-Aliasse vor der ersten Runde) — läuft mit `-s danger-full-access`, vom externen PANDA-Sandbox gefangen (332f2d4).
- **Agentenfehler protokollierten eine leere Begründung** — die Adapter-Diagnose wird nun in Stderr gespiegelt, sodass `store.Fail` und Aufgabenergebnisse den echten Fehler tragen (93a453a).
- **Gegenseitige Wahl-Wiederverbindungs-Sturmböe** — die finale hello-Antwort des Dedup-Verlierers ging über die Registry-Verbindung statt über die ankommende, sodass die Peer-Identität nie gebunden wurde und jede Sekunde neu gewählt wurde (869 Wiederverbindungen in 15 Minuten auf echter Hardware; jetzt 1) (93a453a).
- **Fehlender work_path** zeigte sich als irreführendes fork/exec-ENOENT, das die Befehls-Binärdatei beschuldigte — der Daemon legt beim Boot alle Storage-Wurzeln an (f0a1b9f).
- **Nachgestellte Flags wurden stumm in Positionale geschluckt** (`panda task <id> --config x` verlor die Konfiguration) — jeder Subcommand hebt Flags nun vor (f0a1b9f).
- **Auto-Complete-Schleife** — `/e` schnappte auf `/exit ` zu und Rücklöschen löste es erneut aus (f0a1b9f).
- **SQLite-v9-Migration crashte** auf Alt-Datenbanken, die vor der Existenz der `tasks`-Tabelle erstellt wurden; die Tabelle wird jetzt fehlend angelegt (0e8d850).
- **API-Fehler kommen als Anleitung, nicht als Transport-Rauschen** — 401/403 zeigen auf `model.api_key`, 404 auf `base_url`/Modellname, anhaltende 429/5xx benennen Ratenbegrenzung, Verbindungsfehler raten zur Netzwerkprüfung (df47725).
- **Gates und Härtungen** — `make measure` referenzierte eine nicht existierende Konfiguration; gofmt-Drift; falsche Go-Version im README; `.gitignore` ohne `.openpanda/`; das Phantom-Peer der Beispielkonfig loopte Warnungen; das Panel erhielt `securityHeaders`-Middleware (cacde7b).

### Verbessert

- **Antwortdisziplin** — der Entry-Prompt verlangt Antworten, die mit dem Schluss beginnen; Agenten-Prompts tragen einen Ausgabe-Zusatz: die finale Nachricht berichtet, was getan wurde, das Detail bleibt in den `panda task <id>`-Ereignissen (93a453a).
- **Streaming-Resilienz** — `streamWithRetry` wiederholt transiente Abbrüche (429/5xx/Netzwerk) mit Backoff, solange nichts zugestellt wurde; `deltaGuard` hält Task-JSON aus Chat-Bubbles fern und hält Abbrüche mitten im JSON wiederholbar; ein erschöpfter Tool-Loop konvergiert über einen finalen tool-freien Aufruf; die Tool-Ausführung pinnt den Registry-Schnappschuss des Klassifikationszeitpunkts und verhindert „unknown tool“ beim MCP-Hot-Switch (df47725).
- **Gruppierte Sidebar-Navigation** — einklappbare Abschnitte (Aufgaben / Geräte & Agenten / Persönlich / System) mit dem Entry-Prompt als „Dirigent“-Persona (f5610fc).
- **Panel-Endpunkt-Tests** — siebzehn Tests schließen die riskantesten Lücken: Sessions-CRUD plus echtes Git-Ende-zu-Ende (Worktree-Ausschnitt über HTTP, Diff, Merge), Schlüssel-Maskierung (das rohe Geheimnis verlässt nie das Haus), MCP-Startfehler als 400, Skills-Lebenszyklus, Reminders-CRUD, System-Endpunkte (ad884bf).
- **Stilles Konfig-Laden bei interaktiven Befehlen** — interaktive Flächen stummen den slog-Lärm des Loaders; der Daemon behält das volle Log (f0a1b9f).

### Breaking Changes

- **Ein nacktes `panda` öffnet jetzt die interaktive REPL** statt des Daemon ohne Oberfläche; der Kernel zog in den expliziten Subcommand `panda daemon`. Die systemd-Unit, der LaunchAgent, die Windows-Starter und die Makefile-Run-Ziele wurden aktualisiert — Deployments, die `panda` direkt aufrufen, müssen auf `panda daemon` umstellen (f0a1b9f).

## [0.0.1] - 2026-08-19

Erstes Open-Source-Vorab-Release: der komplette Kernel-Funktionsumfang (Daemon, CLI, P2P-Delegation, Audit-Kette, Migrationen, Scheduler, SSE-Panel, eingebettete Web-Konsole, interaktive REPL, plattformübergreifender Installationslebenszyklus) plus die Assistenten-Schicht (Agenten-Sinne, Erinnerungen, MCP, Worktree-Chats, Kanban-Board). Durchgehend alle Gates grün: build / vet / volle Tests / `-race` / Cross-Compile.

### Hinzugefügt

- **Kernel-Fundament** — Task-Zustandsmaschine mit Leases und Absturzwiederherstellung, authentifizierter WebSocket-Knotenbus, Fähigkeitsverzeichnis und die lokale Ausführungspipeline mit dem OpenCode-Adapter (Sprint 0–1: 1be8f85..307e13a).
- **P2P-Delegation** — knotenübergreifendes Task-Routing, kontextgestufte Übertragung, das gestufte Berechtigungsmodell (Tier 1 automatisch / Tier 2 Freigabe), GPIO-Zugriff und DCPS-Scheduling-Scores (3040e18, 6324a87).
- **Verteidigungskette** — Scope-Drift-Erkennung, Retry-Loop-Erkennung und Befehlsklassifikation mit Tabelle destruktiver Befehle (590cacc, c647c96).
- **Hermes-Gedächtnis und Skills** — Tagesnotizen, Dreaming mit Sedimentation, Projekt-Gedächtnis und ladbare Skills (9a41b3e).
- **Sprach-Sidecar** — Wachwort, STT, TTS und VAD (hardwaregegate), mit `OPENPANDA_WAKE_KEYWORD` / `OPENPANDA_WAKE_MODEL`-Overrides (84faf08).
- **Echtgeräte-Deployment** — drei Knoten auf Mac / Windows / Orange Pi verifiziert, Scope-Routing und die headless Kernel-Form (0aa9f73, 7f1f8bd).
- **Audit und Migrationen** — `prev_hash`-Audit-Ketten, PRAGMA-`user_version`-SQLite-Migrationen, Slow-DoS-Schutz, MCP-Client-Hard-Timeout (7582754).
- **Scheduler-Papier-Mechanismen** — DCPS-gewichtete Bewertung, abgezinst um die TMB-Heartbeat-Frische (30-Minuten-Halbwertszeit); kapazitätsgesteuertes Accept/Decline; Auto-Umleitung bei Ablehnung unter Ausschluss historischer Ablehner (f454909, 7385a89).
- **Interaktive REPL** — Slash-Befehle über jede Panel-Fläche (`/ask`, `/tasks`, `/approve`, `/nodes`, `/web`…), i18n in fünf Sprachen, optionale Ask-Engine (6119493).
- **Eingebettete Web-Konsole** — auf Vite + Preact + TypeScript neu gebaut und per `go:embed` ins Binary gefaltet: Queue/Detail/Ask/Projekte/Knoten-Ansichten, Live-SSE, fünf UI-Sprachen (61cc519, c9768c1).
- **Panel-Schreibpfade + SSE** — `POST /api/ask` über das geteilte `askengine`-Paket, Projekte, Knoten, Abbruch, Logs und der `/api/events`-Änderungsstrom (b4fb9f5).
- **`panda web`** — Loopback-Konsole mit Null-Konfiguration und URL mit kurzlebigem Auto-Login-Token (47517e3).
- **`panda install` / `uninstall` / `doctor`** — plattformübergreifender Lebenszyklus: dauerhafte PATH-Registrierung, eigenständiger Selbstcheck, whitelist-sichere Deinstallation mit Confirm + Zip-Backup (86b9b9d).
- **Kanban-Board** — Erstellformular, Prioritäts-Zyklus, Spalten-weises Drag-Umsortieren, Inline-Freigaben (da9c9e1).
- **Chat-Sitzungen in Git-Worktrees** — Streaming-Antworten, live Gedankenkette, genau einmalige Zusammenfassungs-Rückfaltung (c36cad1).
- **MCP-Integration mit Hot-Reload** — ein stdio-Server, validiert durch tatsächliches Starten vor dem Wechsel; Tools treten ohne Neustart bei (c36cad1).
- **Terminierte Erinnerungen** — vom Agenten über das Tool `reminder.set` selbst geplant; Web Push plus SSE-Countdowns; `panda reminder` CLI (c36cad1).
- **Agenten-Sinne** — die System-Tools `time.now` und `weather.get`: das Modell hat weder Uhr noch Fenster (c36cad1).
- **codex-Adapter + Sichtbarkeit** — Sondierung installierter CLIs mit Konnektivitätstests; `panda detect` scannt Hardware in einen capabilities.yaml-Entwurf (c36cad1).
- **`panda metrics [--csv]` und `panda audit verify [--task <id>]`** — Delegations-Metriken-Export und Audit-Ketten-Verifikation (7582754).
- **`scripts/smoke-delegate`** — prozessübergreifender Delegations-Prüfer: Exit 0 heißt, eine Nur-Peer-Fähigkeits-Aufgabe erreichte done auf einem Peer.
- **Open-Source-Dokumentation** — fünf READMEs, CONTRIBUTING mit Merge-Gates (`make gate`) und die öffentliche Roadmap (51031eb).

### Behoben

- **Gegenseitiges Wahl-Verbindungsflattern** — zwei sich gleichzeitig anwählende Knoten erzeugten einen endlosen ~1s-Verbindungs-/Trennungs-Zyklus; der deterministische Tie-Break in `ensurePeer` (der lexikographisch kleinere Knoten-ID gewinnt) lässt genau eine TCP-Verbindung überleben (879b42d).
- **Wire-Protokoll-Autorisierungslücken** — result/decline/accept/context-ack verifizieren den Sender als aktuellen Ausführenden; CAS-Wachen schließen TOCTOU-Rennen; `waiting_context` trägt immer einen Lease; lokale Fehler terminalisieren ohne Zombies (9622538).
- **Befehls-Klassifikations-Bypässe** — `env -S`-Werte rekursiv klassifiziert, `php -r` gescannt, `find -exec` / `tar --checkpoint-action` / `git push/commit` fail-closed zu Tier 2 (f5db449).
- **Prozessgruppen-Verwaltung** — Ganzbaum-Kill beim Abbrechen (Unix `Setpgid`, Windows `taskkill /T`) und ein 630s-Adapter-Hard-Timeout (f5db449).
- **Gedächtnis-Injektionskanäle** — atomare Schreibvorgänge für Hermes/Projects/skills, externe Eingabe als `[ext]` markiert, Gedächtnis in `<memory_data>` mit Daten-sind-keine-Anweisungen-Präambel eingezäunt (a742585).
- **Abbruch-Propagation** — `task_cancel` kaskadiert hoppenweise zu ausführenden Knoten entlang der Delegationskette (574632a).
- **Transaktionale Schreibvorgänge** — Task-Status-UPDATEs und ihre Audit-INSERTs committen in einer Transaktion (c5d34d4).
- **Umfassender Sweep (D1–D32)** — Delegations-Waisen terminalisiert, weitergeleitete Kopien verleast, Hello-HMAC an ein 5-Minuten-Fenster gebunden, NetworkGuard auf konfigurierte Endpunkte genagelt, begrenzte Ausgabe-Erfassung (1694b7d).
- **Weiße Konsole bei frischen Klonen** — ein von git wiederhergestelltes gehashtes `index.html` zeigte auf ignorierte Assets; der committete Platzhalter ist jetzt stabil und `make web` bewacht das Landen des echten Builds (ab87f90).
- **Unbekannte Subcommands starteten einen residenten Daemon** (`panda statsu`) — jetzt Exit 2 mit Verwendung (a742585).
- **Sprach-Wach-Defaults** — echte eingebaute Schlüsselwörter pro Backend (`hey_jarvis` / `porcupine`) (4ea73bf).
- **Vorab-Release-Audit-Fixes** — `panda help` existiert; „PANDA“-Markenreste aus Prompts und Beispielen entfernt; `config.example.yaml` dokumentiert `mcp:` und `model.api_type`; toter Roadmap-Link behoben (2f001c0).

### Verbessert

- **Harte Gedächtnis-Mauer** — persönliches Gedächtnis wird nie in Workspace-(worktree-angepinnte)-Konversationen injiziert; Projekt-Gedächtnis erreicht die Ausführung nur über den ContextPack des ausführenden Knotens (da9c9e1).
- **Agenten-Adapter treten dem Tier-Modell bei** — nicht deklarierte sind standardmäßig Tier 2 und werden vor dem Spawn des Subprozesses abgewiesen (a4d2d9e).
- **OpenAI-Wire-Format neben Anthropic** — das Entry-Modell spricht beide Endpunkt-Typen, mit Streaming auf beiden (c36cad1).
- **Geheimnis-Datei-Härtung** — Konfigs mit `api_key` / `shared_secret` / `panel_token` werden automatisch auf 0600 chmod-iert mit Umgebungsvariablen-Hinweis (6275fd4).
- **Das Panel standardmäßig auf Loopback** (`127.0.0.1:7840`); Nicht-Loopback-Binds warnen vor Klartext-HTTP (a742585).
- **Peer-Wiederverbindung ersetzt obsolete Verbindungen** — eine neue Verbindung derselben Identität wechselt in die Registry; das Entfernen matcht per Verbindungsidentität (7911bbe).
- **Die Interpreter-`-c`-Klassifikation ist whitelist-basiert** — nur nachweislich rein ausgebender Code bleibt Tier 1 (f5db449).
- **Deployment-Basislinie dokumentiert** — Klartext-`ws://` nur über Loopback/Tailscale/vertrauenswürdiges LAN; TLS + `wss://` im offenen Internet (7582754).

### Breaking Changes

- **Projekt in OpenPanda umbenannt** — Modulpfad `github.com/Xustalis/OpenPanda`, Umgebungsvariablen mit Präfix `OPENPANDA_`, Units `openpanda.service` / `com.openpanda.node.plist`, Standard-DB `openpanda.db`; das CLI-Binary behält den Kurznamen `panda` (ac71bb1, 6f2083e).

## Aufgeschobene Nacharbeiten

Bewusst geparkt, damit sie sichtbar bleiben:

- Tastaturkürzel für die Konsole (neuer Chat, Schnellaufgabe, Ansichtswechsel).
- Begleit-Browser-Oberfläche für den Assistenten.
- Git-Ansichten erster Klasse in der Konsole (Branch-Status, Historie, Remotes).
- Worktree-Verwaltung von der Konsole (auflisten/aufräumen/inspizieren).
- Vom Nutzer einstellbare Persönlichkeit und Darstellung des Assistenten.
- Web-Suche-Caching gegen wiederholte Abrufe und Latenz.
- Denk-Aufwand-Stufen pro Aufgabe (niedrig/mittel/hoch).
