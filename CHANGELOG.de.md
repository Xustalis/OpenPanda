# Änderungsprotokoll

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## Installation mit einem Befehl

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

**macOS (Homebrew)**

```sh
brew install Xustalis/openpanda/openpanda
```

Nach der Installation führst du `panda init` aus, um den Knoten einzurichten, oder tippst einfach `panda`, um in die REPL zu kommen. Eine ältere Installation wird mit demselben Befehl an Ort und Stelle aktualisiert — Benutzerdaten bleiben erhalten.

## Über das Projekt

OpenPanda (**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant) ist ein persönliches Task-Orchestrierungs-Kernel: Ein `panda`-Binary läuft auf jedem deiner Geräte, die Knoten finden sich über einen authentifizierten WebSocket-Bus, ein Entry-Modell verwandelt jede Anfrage in eine direkte Antwort oder eine ausführbare Task-Spezifikation, und der Scheduler leitet jede Aufgabe an das am besten geeignete Gerät und den passenden Agenten. Die CLI ist die Hauptschnittstelle des Kernels — ein nacktes `panda` öffnet die interaktive REPL — und die Web-Konsole ist eine dünne Hülle über demselben Store und derselben Engine.

## Versionsregeln

- Versionen folgen `MAJOR.MINOR.PATCH`. Das Projekt ist in der Anfangsentwicklung (`0.0.x`): Ein Patch-Release darf Funktionen hinzufügen, Fehler beheben und — ausnahmsweise — Breaking Changes einführen, die stets unter **Breaking Changes** aufgeführt werden.
- Ein Release wird durch das Tag `vX.Y.Z` geschnitten; jeder Commit seit dem vorherigen Tag gehört zum Abschnitt der neuen Version. `[Unreleased]` sammelt die Arbeit seit dem letzten Tag.
- Jede Version wird in vier Kategorien dokumentiert: **Hinzugefügt** (neue Funktionen), **Behoben** (Fehlerkorrekturen), **Verbessert** (Verfeinerungen), **Breaking Changes** (erfordert Handeln beim Upgrade).
- Jeder Eintrag benennt die Änderung und ihre sichtbare Wirkung in ein bis drei Zeilen; der einführende Commit wird zitiert, wo das der Archäologie hilft.
- Die englische Datei ist maßgeblich. Die Übersetzungen zh-CN / ja / es / de spiegeln sie und können um ein Release kurz verzögert sein.

## [Unreleased]

## [0.0.7] - 2026-08-31

Das Usability-Release: Die Capability-Karte — die Datei, die dem Scheduler sagt, was dieser Knoten kann — lässt sich nun von jeder Oberfläche (CLI, REPL, TUI und Webkonsole) bearbeiten, ohne den Daemon neu zu starten; ein zweites Gerät hinzuzufügen ist jetzt ein Produktfluss statt eines Konfigurationsdatei-Rätsels; und jedes Aufgabenergebnis erhält eine menschenlesbare Zusammenfassung, sodass der Nutzer sieht, was passiert ist, statt einer Wand aus rohem stdout.

### Hinzugefügt

- **Strukturierte Kartenbearbeitung überall** — `panda card native add|remove`, `panda card agent add|remove|set`, `panda card manual add|remove` (strukturierte Unterkommandos, nicht nur der Editor); dieselben Operationen über `/card` im REPL und TUI; sowie ein vollständiger Karten-Editor in der Webkonsole (`/api/card` plus agent/native/manual-Endpunkte). Alle Schreibpfade laufen durch dieselbe Validator + `.bak` + atomare Schreib-Pipeline, sodass eine fehlerhafte Bearbeitung die Karte nicht beschädigen kann (1b8e2b7).
- **Geräte-Pairing** — `panda pair` erzeugt ein gemeinsames Secret, gibt die Beitrittsanleitung für das neue Gerät aus und schreibt beide Konfigurationen; `panda nodes add <addr>` fügt einen Peer hinzu und wählt ihn live ohne Neustart an; der „Gerät einladen“-CTA der Webkonsole verbindet nun mit dem echten Pairing-Fluss der Knotenseite (763bff6, 5748cec).
- **Hot-Reload der Karte** — das Bearbeiten der Karte (von jeder Oberfläche) löst `ReloadCard` aus: Der Scheduler liest neu ein, registriert Fähigkeiten erneut und sendet einen Heartbeat mit der neuen Karte an alle verbundenen Peers, sodass sich Änderungen ohne Daemon-Neustart verbreiten (3d6feeb).
- **Bubble-Tea-TUI** — `panda` öffnet nun ein Bubble-Tea-Frontend mit funktionierendem Tier-2-Genehmigungspfad (Inline-Genehmigungskarte, `y` zum Genehmigen, `n` zum Zurückstellen für `/approve`); das klassische REPL bleibt über `PANDA_CLASSIC_REPL=1` verfügbar (06cca6a).
- **LLM-Aufgaben-Zusammenfassung** — nach jeder Inline-Aufgabe (Erfolg oder Fehlschlag) ruft die Engine das Einstiegsmodell auf, um eine menschenlesbare Zusammenfassung des Geschehenen zu erzeugen; die Zusammenfassung wird im REPL, TUI und der Webkonsole vor der rohen Ausgabe angezeigt, sodass der Nutzer „was getan wurde + wichtige Ausgabe“ (Erfolg) oder „warum es fehlschlug + was als Nächstes zu tun ist“ (Fehlschlag) sieht statt rohem stdout/stderr. Ein Modellfehler degradiert anmutig — die Zusammenfassung wird übersprungen und die rohe Ausgabe gezeigt (dieses Release).
- **Web: Gedanken-Streaming und Aufgabenfortschritt** — die Überlegungen des Modells werden als einklappbarer Gedankenblock in den Chat gestreamt (03a4301); Aufgabennachrichten zeigen Fortschritt und Ergebnis statt nur der Nutzlast (4ba931f).
- **Remote-Tier-2-Fortsetzung** — wenn eine Tier-2-Aufgabe nach der Delegation an einen Remote-Knoten genehmigt wird, findet der Wiederholungslauf auf dem Executor statt (wo die Arbeit hingehört), nicht auf der Maschine des Genehmigers (3d6feeb).
- **Recover-Wache für dauerhafte Goroutinen** — das neue `internal/guard` umhüllt langlebige Goroutinen: Ein Panic wird mit vollständigem Stack protokolliert und löst ein kontrolliertes Herunterfahren aus, statt einen halbtoten Prozess weiterlaufen zu lassen; ein Panic in der Leseschleife einer einzelnen Busverbindung schließt nur diese Verbindung.
- **Geordnetes Herunterfahren unter Windows** — die Konsolenereignisse CTRL_CLOSE/LOGOFF/SHUTDOWN lösen nun denselben geordneten Shutdown-Pfad aus wie SIGTERM unter Unix (`SetConsoleCtrlHandler`, kurzes Aufräumfenster).
- **Farben in der Windows-Konsole** — die TUI-Palette aktiviert Farben auf einer Windows-Konsolen-TTY, wenn TERM nicht gesetzt ist; `dumb` und `NO_COLOR` haben weiter Vorrang.
- **`make build-darwin-amd64`** — Intel-Mac-Buildziel neben den anderen plattformspezifischen Zielen.
- **Agent-Fähigkeitsfläche und Task-weises Tool-Policy** — die Agent-Registry deklariert nun die nativen Fähigkeiten jeder CLI (Skills, MCP, Subagenten) statt Hardcoding pro Adapter; das Einstiegsmodell kann ein Task-weises Tool-Policy (`minimal` / `extended`) anfordern, das das globale Routing-Policy überschreibt, sodass eine hochkomplexe Aufgabe die volle Fähigkeitsfläche des Agents nur für diese Aufgabe freischalten kann. Claude-Code-Subagenten-Erzeugungen (das Task-Tool) erscheinen als typisierte `subagent`-Fortschrittsereignisse und landen ungedrosselt in der Task-Zeitleiste (dieses Release).
- **Agent-Timeouts nach Aufgabenart** — `timeouts.agent_by_kind` überschreibt das Agent-Wanduhr-Budget pro Aufgabenart (ein Training darf länger laufen als ein Schnellfix); nicht gelistete Arten behalten `timeouts.agent_s`, und der Task-Lease bleibt stets über dem jeweils geltenden Budget (bcbe1d2, e573c2e, 9fc2d04).
- **Circuit-Breaker-Zustand in Heartbeats** — ein Knoten, dessen Agent-CLI dauernd fehlschlägt, meldet das im Heartbeat, sodass Peers keine Arbeit mehr an einen kaputten Agenten routen, bevor sie selbst darauf stoßen (bcbe1d2).
- **Agent-Sitzungsfortsetzung und Nutzungserfassung** — Aufsichtsrunden setzen die eigene Konversation des Agents fort, statt sie jede Runde kalt zu starten (`session_id` + `resume` im Adapter-Drahtprotokoll), und Adapter melden eine strukturierte Token-Nutzungsaufschlüsselung, die als `agent_usage`-Ereignisse aufgezeichnet wird (e8dc68b, 183bf6f, 1722144).
- **Obergrenze für Delegations-Tiefe** — der Konsens reist mit einem Hop-Limit über die Leitung: Ein Task kann nur eine begrenzte Zahl von Hops weiterdelegiert werden, sodass Delegationsketten nicht mehr grenzenlos wachsen können (ca5770e).
- **Zuverlässige Cancel-Zustellung** — `task_cancel` reist nun über dieselbe Outbox wie Ergebnisse: ein Cancel, der bei getrenntem Executor ausgegeben wird, wird persistiert und beim Wiederverbinden erneut zugestellt, statt verloren zu gehen (dc4412a).

### Behoben

- **P0-Sicherheitsbefunde geschlossen** — plan_id/stage_id-Pfad-Traversal (beliebiges Verzeichnislesen und -abfluss über `../../../../` in Stage-Arbeitsverzeichnissen) wird nun durch ID-Validierung + Wurzel-Präfix-Assertion blockiert; die Ergebniszustellung nach Peer-Verbindungsabbruch wird in einer Outbox persistiert und beim Wiederverbinden erneut zugestellt (Review P0-2); die TUI-Unterbrechungs-/Beendigungssemantik wurde korrigiert, sodass Ctrl+C tatsächlich beendet (763bff6, 5129461).
- **P1-Sicherheitsverschärfung** — die Standard-Höradresse wurde von `0.0.0.0:7836` auf `127.0.0.1:7836` geändert (der Daemon bindet standardmäßig nicht mehr alle Schnittstellen); `context_fetch` verlangt nun, dass der Peer in der Delegationskette der Aufgabe ist; Unerreichbarkeit des Supervisors parkt die Aufgabe zur Prüfung, statt ein ungeprüftes Ergebnis still zu akzeptieren (763bff6).
- **Einstiegsmodell: keine verdoppelten Nutzerzüge mehr** — strenge Anbieter (Anthropic-kompatibel) gaben 400 zurück, wenn die Sitzungswiederholung einen Nutzerzug verdoppelte oder hängen ließ; der Normalisierungsschritt führt nun aufeinanderfolgende Nur-Text-Züge derselben Rolle zusammen (8174e78).
- **Orchestrierungszeitnahme und Web-Nachrichten-Rennbedingung** — die Richterlaufzeit wird nicht mehr der ausführenden Stage zugerechnet (ein eigener `judge_start`-Trace-Marker); die Aufsichtsschleife tracet die Ausführung vor dem Rundenergebnis, sodass weiter→weiter-Pfade die Wiederholung nicht verbergen; der optimistische Web-Zugzustand ist in `chatstate.ts` ausgelagert, und bei Fehlern wird die optimistische Blase entfernt, sodass die Antwort des Assistenten nicht mehr in einer Nutzernachricht landet (97d5c62).
- **Abbruch-Rennbedingung mit Executor-Akzeptanz** — ein Abbruch, der während des Akzeptanzfensters des Executors ankam, wurde verworfen; der Abbruch wird nun eingereiht und nach Abschluss der Akzeptanz angewendet (a19b33b).
- **Windows-Gate und deterministischer Mutual-Dial-Handschlag** — das plattformübergreifende CI-Gate besteht nun unter Windows; der Tie-Break des gegenseitigen Anwählens ist unabhängig von der Ankunftsfolge deterministisch (526c731).
- **CI: parallele Gate-Jobs** — der Gate-Workflow führt nun build/vet/test/typecheck als parallele Jobs aus, begrenzt den Race-Detektor auf die Pakete, die ihn brauchen, und gatet den Typecheck der Webkonsole (3f302f1).
- **Wechselseitiger Ausschluss bei Migrationen** — Schema-Migrationen laufen unter `BEGIN IMMEDIATE` und prüfen `user_version` innerhalb der Transaktion erneut, sodass zwei Prozesse, die dieselbe Datenbank öffnen, jede Version genau einmal anwenden; eine Binärdatei, die älter als das Datenbankschema ist, schlägt jetzt ausdrücklich fehl, statt still fortzufahren.
- **Web: ein einziger Ereignisbus** — die Konsole hält nun eine einzelne referenzgezählte SSE-Verbindung, authentifiziert per `Authorization`-Header (kein Token in der URL), mit exponentieller Backoff-Wiederverbindung, und verteilt change- und trace-Ereignisse an alle Abonnenten.
- **Web: Rennbedingung im Sitzungsstrom** — Streaming-Schreibvorgänge greifen nur, solange die Sitzung aktiv ist; ein Sitzungswechsel mitten im Strom mischt keine Bubbles mehr zwischen Threads, und veraltete Transkript-Ladungen werden beim Wechsel abgebrochen.
- **Web: Robustheit und Barrierefreiheit** — eine Top-Level-Fehlergrenze mit Wiederholung; Fokusfalle in Befehlspalette und Bestätigungsdialogen; Kanban-Karten per Tastatur bedienbar (Enter/Space, sichtbarer Fokus); System-Polling pausiert bei ausgeblendetem Tab und überspringt noch laufende Abfragen; stabile Listen-Keys.
- **`panda skill --help` / `panda reminder --help`** — geben die Nutzung aus und beenden mit 0, statt `--help` als unbekanntes Verb zu behandeln.
- **CI: Gate- und Installer-Strecken repariert** — alle vier fehlgeschlagenen Gate-Strecken und die Installer-Pipeline repariert (7c418b0).
- **CLI: eingeklappte Gedankenblöcke zeigen keinen Schlüssel mehr an, der sie nicht entfalten kann** (e772598).
- **Verwaiste Weiterleitungen und veraltete Verzeichniszeilen** — von einem toten Peer hängengelassene Relais werden gerettet und abgeschlossen, und Verzeichniszeilen, die kein Peer mehr trägt, werden aufgeräumt, statt für immer zu bleiben (32e4489).
- **Push bricht bei Timeout die nachgelagerte Arbeit ab** — ein Push mit Zeitüberschreitung cancelt seine nachgelagerte Arbeit, statt sie weiterlaufen zu lassen, und das Retry-Budget überlebt Neustarts (f7efb70).

### Verbessert

- **Standard-Höradresse** — der Daemon bindet nun standardmäßig `127.0.0.1:7836` statt `0.0.0.0:7836`. Bestehende Deployments, die sich auf den alten Standard verließen, müssen `network.listen_addr` ausdrücklich in `config.yaml` setzen oder `OPENPANDA_LISTEN_ADDR` verwenden.
- **Plattformgerechtes Systemkonfigurationsverzeichnis** — das System-Fallback-Konfigurationsverzeichnis bleibt unter Unix `/etc/openpanda` und ist unter Windows `%ProgramData%\OpenPanda`.
- **Ein einziger Store-Initialisierungspfad** — Daemon und Web-Panel öffnen den Store über dieselbe Funktion (`cmd/panda/store.go`); das Panel übersieht das Artefakt-Pool-Verzeichnis nicht mehr.
- **Web-Panel: Ereignis-Scans entkoppeln sich von der Verbindungszahl** — Task-/Node-/Reminder-Fingerabdrücke werden für ein Poll-Intervall zwischengespeichert, sodass die Scan-Last auch bei wachsenden Abonnenten nahezu konstant bleibt.
- **Adapterweise Abstimmung** — die übrigen Agent-Adapter erhielten jeweils CLI-spezifische Aufrufbehandlung statt eines generischen Pfads (24df1c1).

## [0.0.6] - 2026-08-27

Das Release über geräteübergreifendes Rechnen nimmt Form an: Eine Anfrage, die für verschiedene Schritte verschiedene Maschinen braucht, ist jetzt ein Plan erster Klasse, dessen Stufen dort laufen, wo die Hardware ist — und beide Oberflächen, CLI und Web-Konsole, bekamen die Präsentationsschicht, die ihnen fehlte: Live-Feedback während ein ask konvergiert, echtes Markdown im Browser und den Eingabe-Editor, den der Alltag verlangt.

### Hinzugefügt

- **Plan plane — Pipelines, deren Stufen auf verschiedenen Maschinen laufen** — eine Stufe IST eine gewöhnliche Aufgabe (CAS-Zustandsmaschine, Lease, Retry, Supervision, Review-Parken), also erbt eine Pipeline alles, was eine Aufgabe schon hat; das Arbeitsverzeichnis einer beendeten Stufe wird gepackt, in Chunks über den Bus zur Maschine der nächsten Stufe transportiert. Zwei Einstiege: `panda plan example > train.yaml`, `panda plan run train.yaml [--dry-run]`, `panda plan show <id>` — oder ein Satz über `panda ask`, wobei das Modell genau dann einen Plan ausgibt, wenn eine Anfrage die Maschine wechseln muss. Keine Stufe trägt Tier-2-Zustimmung: Eine irreversible Stufe parkt im Review für einen Menschen (c10b8af).
- **Routing nach deklarierter Hardware** — `resource_profile` ist ein Hartfilter (`ledger.Fits`), und der Scorer rangiert nach freier Kapazität + Queue-Tiefe + Tier, abgezinst um Herz-Frische — zwei gleichzeitig gestellte Aufgaben landen auf zwei Maschinen; der Eingabe-Prompt trägt die echte Hardware jedes Knotens, damit das Modell den Routing-Filter sehend füllt statt blind (c10b8af).
- **`panda voice`** — Wake Word → ASR → dieselbe Eingabe-Pipeline → TTS: der Desktop-Tier-Einstieg für ein Gerät ohne Tastatur (c10b8af).
- **`panda card show | rescan | edit | set`** — eine Befehlsfamilie über der Fähigkeitskarte: ausgeben (und aus welcher Datei sie stammt), Hardware und installierte Agent-CLIs neu scannen (`rescan` druckt ein Diff, `--write` wendet es an und behält `.bak`, handgeschriebene Entscheidungen bleiben), im `$EDITOR` öffnen oder Felder headless setzen. `panda detect`, der Karten-Rescan und das Panel teilen sich jetzt eine Erkennungsschicht (`internal/hwinfo`) (fdb56b8).
- **Eine Präsentationsschicht für das CLI** — `internal/cliui`: eine einmal aufgelöste Palette und eine Live-Statuszeile (Spinner, Verb, vergangene Sekunden, Token-Zähler — die letzten beiden wurden längst erfasst, nur nie gezeigt), die auf Pipes zu einer statischen Zeile zurückfällt. Der Zeileneditor lernt Bracketed Paste und mehrzeilige Eingabe (ein eingefügter mehrzeiliger Prompt ist ein einziger ask, die Historie ruft ihn als einen ab), Ctrl-R inkrementelle Historiensuche und Vervollständigung an Argumentposition für die IDs, die niemand neu tippt. Unbekannte Befehle erhalten ein did-you-mean; `/help` druckt inline nach Absicht gruppiert; neue Befehle decken ab, was nach dem ersten ask gebraucht wird (`/cost`, `/model`, `/status`, `/doctor`, `/export`, `/clear`), plus `@file`-Anhang und `!cmd`-Durchreiche, damit der Prompt nie verlassen wird (c538ab6).
- **Die Web-Chat-Oberfläche holt auf** — ein handgeschriebener Markdown-Renderer (null `innerHTML`, also keine Sanitizer-Abhängigkeit; 29 Node-Tests) ersetzt literales `**bold**` und ```-Zäune in Antworten; die Hauptaktion des Composers während des Streamings ist ein Stopp-Button (der SSE-Reader nimmt ein AbortSignal); Autoscroll reißt die Ansicht nicht mehr herunter, sobald der Leser hochscrollt; Cmd+K-Palette über dasselbe Navigationsvokabular wie die Sidebar; mobiler Thread-Drawer statt `display:none` (c538ab6).
- **Eine Statusseite** — `docs/status.md` hält fest, was funktioniert, was nur gebaut und was fehlt, samt Verifizierungsstand der Flaggschiff-Pipeline (76c5b69).
- **Veraltete Knotenzeilen lassen sich entfernen** — `panda nodes remove <id>` und ein Entfernen-Knopf auf Offline-Knotenkarten löschen eine Verzeichniszeile, die kein lebender Peer stützt (eine umbenannte Maschine, eine geänderte Identität, ein abgewickelter Knoten). Die Zeile des lokalen Knotens und Online-Knoten werden abgelehnt — beide registrieren sich selbst erneut, „Entfernen" wäre also ein No-Op im Erfolgskostüm.
- **Release-Notes-Werkzeug** — der Release-Workflow veröffentlicht die CHANGELOG-Sektion der Version plus plattformbezogene Installationsbefehle als Release-Body und lässt den Build scheitern, wenn die Sektion fehlt; die 0.0.5-Release-Seite wurde nach diesem Standard neu geschrieben, mit englischem Body und Sprachumschalter; jeder CHANGELOG beginnt jetzt mit der Ein-Befehl-Installation (4e12779, c25a3cb, 98e10df, 600ffb3).

### Behoben

- **Queue-Aufgaben und Plan-Stufen routen jetzt wirklich** — der Queue-Pfad behielt eine lokale Abkürzung „wenn ich es kann, mache ich es", die der Router selbst längst entfernt hatte — und genau dieser Pfad lief für jeden Panel-Task und jede Plan-Stufe: Der Hardware-Filter lief nie dort, wo die Flaggschiff-Pipeline tatsächlich läuft (eine GPU-Stufe blieb auf dem Pi, solange der Pi die Fähigkeit hatte; ein Stoß Aufgaben blieb geschlossen auf dem annehmenden Knoten). Die Entscheidung gehört jetzt dem Scheduler; eine leere Fähigkeitsliste bedeutet „keine Bedingung", nicht „niemand passt"; Plan-Stufen bekommen je einen Ressourcenschlüssel, sodass unabhängige Stufen fächern statt sich gegenseitig zu blockieren (a5b792e).
- **Das Ergebnis eines beendeten Laufs passt in einen Frame** — die `task_result`-Ausgabe wird auf die Bus-Frame-Größe geklemmt, sodass das Ergebnis eines abgeschlossenen Laufs beim Einreicher ankommt, statt den Frame zu überlaufen und zu verschwinden (c1310da).
- **Memory-Zäune lassen sich von innen nicht schließen** — der `<memory_data>`-Zaum umwickelte den Körper, ohne die Tags des Körpers selbst anzufassen: Ein Eintrag mit dem literalen Schließ-Tag beendete den Zaum vorzeitig und der Rest wurde als Anweisung gelesen — und Memory ist durch die eigenen Werkzeuge des Modells, das Panel und beförderte Traum-Kandidaten schreibbar. Innere Tags werden neutralisiert; der Text bleibt sichtbar zur Prüfung (3f18994).
- **Ein Knoten beschreibt nicht mehr die Hardware einer anderen Maschine** — jede dieser Stellen war ein fest codierter Wert, wo eine Sonde hingehörte: Der Standard-Knotenname war „macbook", also meldete sich jeder Knoten ohne `panda init` unter dem Laptop-Namen des Autors; macOS/Windows hatten keine Machine-ID-Quelle, Umbenennen sah wie ein neuer Knoten aus; die Windows-Sandbox entfernte PATHEXT/SYSTEMROOT/TEMP — genau deshalb konnte ein Windows-Compute-Knoten gar keinen Adapter starten; `python3` ist kein portabler Interpretername (jetzt wird gesondet, Windows probiert `py -3` zuerst); eine Time-out-Aufgabe hing unter Windows für immer, weil das Harness einen Prozessbaum nicht töten konnte (jetzt `taskkill /T`); eine Karte, die eine native Fähigkeit ohne vorhandenes Kommando bewarb, gewann die Route und starb mit 127 (beim Laden entfernt); eine GPU, deren Größe keine Sonde lesen konnte, schrieb 0 und wurde von genau der Arbeit ausgeschlossen, für die sie existiert („unbekannt" ist jetzt ein dritter Zustand); `deploy-pi.sh` defaultete auf die LAN-Adresse eines Entwicklers (jetzt Pflichtfeld) (fdb56b8).
- **i18n-Regressions geschlossen** — fest codiertes Chinesisch im Voice-Pfad, in der Plan-Ausgabe von ask/repl, in Gesprächszusammenfassungen, Deinstallationsfehlern und einem Hinweis in `panda help` wanderte nach `internal/i18n` in alle fünf Sprachen — ja/es/de-Nutzer sahen in diesen Oberflächen Chinesisch (c538ab6).
- **Die REPL öffnet in unter einer Sekunde, nicht nach dem Peer-Dial-Timeout** — der interaktive Start wählte jeden konfigurierten Peer **seriell** vor dem Banner an und wartete dann auf deren Festigung: ein offline Peer verbrannte das volle 10-s-Timeout des Dialers als tote Stille vor dem ersten Prompt. REPL, `panda session` und `panda voice` wählen Peers jetzt im Hintergrund (ein offline Peer ist in einer langlebigen Session Routine, und sein Fehlschlag druckt keine WARN-Zeilen mehr mitten im Tippen), und der einmalige `panda ask` wählt parallel — ein unerreichbarer Peer blockiert keinen erreichbaren mehr.

## [0.0.5] - 2026-08-25

Das Drei-Geräte-Labor-Patch: der erste echte macOS- + Orange-Pi- + Windows-Cluster — über die öffentlichen Installer installiert, per LAN verbunden, Ende-zu-Ende betrieben — legte offen, dass Queue-Aufgaben ihren Ursprungsknoten nie verließen, Tier-2-Zustimmung an der Delegationsgrenze starb und eine ausgesperrte Agent-CLI Routing anziehen und minutenlang hängen konnte. Fünf Commits, alle auf genau dieser Hardware verifiziert.

### Hinzugefügt

- **`panda task add --requires`** — deklariert die Fähigkeiten, die ein Task braucht (`--requires gpio:read`, kommasepariert); ein Queue-Task ohne lokalen Treffer wird auf ein Gerät geroutet, das sie hat — dieselbe Root-Scheduler-Policy, die `panda ask` immer verwendet hat (c4e1bc7).

### Behoben

- **Queue-Aufgaben routen jetzt geräteübergreifend** — Tasks aus `panda task add` und der Web-Konsole wurden ausschließlich vom Ursprungsknoten geclaims und ausgeführt: Ein Task, dessen Fähigkeit nur ein anderes Gerät hatte, scheiterte direkt (`route: no capability matches` beim Einreichen von `pi.uptime` auf einem Mac). Beim Claim befragt der Scheduler jetzt den Root-Scheduler; ohne lokalen Treffer wird der Claim an einen fähigen Peer umgeleitet (Loop-Schutz über abgelehnte Knoten, Lease zur Erkennung eines toten Executors), und das Ergebnis des Peers vervollständigt die Zeile des Ursprungs. Im Labor in alle drei Richtungen verifiziert: Mac→OrangePi, OrangePi→Mac, Windows→OrangePi (c4e1bc7).
- **Tier-2-Autorisierung reist mit der Delegation** — die `--authorize`-Zustimmung war lokal auf den einreichenden Knoten beschränkt, sodass ein delegierter Agent-Task an der Defense-Schicht des Executors abprallte, obwohl der Nutzer ihn genehmigt hatte. Die Zustimmung propagiert jetzt über den authentifizierten Bus, und der Executor ehrt sie: Eine kreditialenlose Orange Pi, die einen autorisierten Coding-Task beim claude des Macs einreicht, läuft jetzt durch, statt in review zu sterben (c4e1bc7).
- **Ausgesperrte Agent-CLIs ziehen kein Routing mehr an** — die Capability-Karte ist statisch, aber eine installierte CLI kann unbrauchbar sein: Ein `claude.exe` auf der Windows-Maschine ohne Login-State und ohne Model-Key bewarb `agent:*` in der Flotte, das Routing schickte ihm einen Coding-Task, und es hing minutenlang, bevor es mit einem Netzwerkfehler starb. Die lokale Fallback-Kette und das über hello beworbene Capability-Summary prüfen jetzt Viability — CLI im PATH *und* ein erreichbares Modell (eigene Credentials oder Injection); das Summary dieser Windows-Maschine bewirbt jetzt nur noch `win.sysinfo` (2db530f).
- **`panda web` stirbt nicht mehr an einem belegten Port** — ein zweites `/web` (oder ein leftover Prozess) brach mit `bind: address already in use` ab und druckte einen Token zum Abtippen. Die Konsole weicht jetzt auf einen Nachbar-Port aus und sagt das; der Browser öffnet sich bereits authentifiziert (der Token wird nie gedruckt), und ein laufendes `/web` öffnet den Browser neu eingeloggt. `--no-browser` druckt weiterhin eine Token-URL für manuelle Nutzung (c4e1bc7).
- **Peer-hello meldet die echte Version** — alle drei Hello-Pfade bewarben ein hartkodiertes `0.1.0-dev`, sodass `panda nodes` in einer gemischten Flotte falsche Versionen zeigte; sie melden jetzt `version.Version` (alle drei Labor-Geräte zeigen 0.0.5) (2db530f).
- **Die Capability-Karte neben der aufgelösten Config schlägt `./capabilities.yaml`** — ein Daemon-Start aus einem Verzeichnis, das zufällig ein capabilities.yaml enthält (ein Repo-Checkout, die Karte eines anderen Knotens), lud lautlos die falsche Karte; die von init neben die Config-Datei geschriebene Karte gewinnt jetzt, `--card` bleibt die höchste Instanz (2db530f).
- **Windows-Datenverzeichnis kollidiert nicht mehr mit dem Installationspräfix** — das Standard-State-Dir `%LOCALAPPDATA%\openpanda` und das Installationspräfix `%LOCALAPPDATA%\OpenPanda` sind auf case-insensitivem NTFS dasselbe Verzeichnis: SQLite-Store, Memory und Projekte lagen *innerhalb* des Installationspräfixes, und ein Uninstall räumte sie weg. Das Datenverzeichnis ist jetzt `%LOCALAPPDATA%\openpanda-data`; Windows-Knoten von 0.0.4 starten mit einem frischen Store (fc50721).
- **Installer überleben rate-limitete GitHub-API und kaputten WinPS-5.1-HTTP-Stack** — `api.github.com` erlaubt 60 unauthentifizierte Requests pro IP und Stunde; bei Erschöpfung lösen beide Installer die neueste Version jetzt über den 302-Redirect von `/releases/latest`. `install.ps1` erzwingt TLS 1.2 vorab, bevorzugt das mitgelieferte `curl.exe` (Windows 10 1803+) mit `Invoke-WebRequest` als Fallback und ergänzt Timeouts, sodass eine kaputte WinINET-Proxy-Konfiguration schnell fehlschlägt, statt zu hängen. Beides trat bei der echten Drei-Geräte-Installation auf (109b567).
- **Homebrew-Tap-Push authentifiziert** — der Tap-Update-Schritt des Release-Workflows scheiterte mit `could not read Username`, wenn dem Job-Token der Grant fehlte; die Push-URL bettet jetzt das Token ein (6868a63).

## [0.0.4] - 2026-08-25

> GA: Release für verteilte Knoten. Vollständige Details siehe [Abschnitt 0.0.4 in CHANGELOG.md](CHANGELOG.md).
>
> Highlights: physical / VM Knotentyp + stabile Identity, Singleton-Daemon-Guard pro Host (`nodeidentity`-Paket), Adapterprotokoll-Härtung + Vertragstests, `/api/self` + `/api/nodes` samt Nodes-Webseite, 3-Knoten-Verteilt-Labor-Tools und die Root-Cause-Behebung von SQLITE_CANTOPEN 14 bei Homebrew-Installs / beliebigem cwd. Seit der Beta: Entscheidungscaches des Entry-Modells, geschichteter Systemprompt, Web-Onboarding ohne Konfiguration, gemeinsamer Adapter-Harness, Tier-2-Autorisierungs-UX, Installer-/Deinstaller-Aufräumen, Changelog-Digest im Updater, einfragen `panda init` und Szenario-FAQ.

### Hinzugefügt

- `node.kind = physical | vm` + stabile Identity; VM verlangt `node.identity` explizit; peer hello v2 überträgt die Felder; `employee_cache`-Migration v10 befüllt bestehende Zeilen mit `DEFAULT 'physical'`.
- Singleton-Daemon-Guard via OS-Level File-Lock: `flock(2)` unter Unix, `LockFileEx` unter Windows — ein zweiter `panda daemon` zur gleichen Identity beendet sich sauber mit Diagnose.
- Einheitlicher Frame `{ok, result, exit_code}` inkl. stderr-als-Diagnose bei Non-Zero-Exits. Neuer Test `tests/adapter_contract_test.py`.
- Routen `/api/self` + `/api/nodes` + Web-Tab Nodes mit running/last-seen-Tabelle.
- `scripts/lab/*` + `scripts/scenario-model/` + `scripts/task-timeline/` + Testplan `docs/testing/distributed-lab-plan.md`.

### Behoben

- **Startfehler (SQLITE_CANTOPEN 14) unter Homebrew / beliebigem cwd**:
  1. `config.Default()` an `UserDataDir()` (plattformspezifisch) verankert.
  2. `resolveRelativePaths()` in `Load()`: alte relative Pfade werden zum YAML-Verzeichnis statt zum Shell-cwd aufgelöst.
  3. `storage.Open()` erzeugt DB-Elternverzeichnis via MkdirAll.
  4. `panelStore()` (REPL / web / queue / …) erzeugt jetzt alle Storage-Verzeichnisse, analog zu `runDaemon`.

### Verbessert

- `panda nodes` Ausgabe um Spalte `Kind` (physical | vm) erweitert.

## [0.0.3] - 2026-08-23

### Hinzugefügt

- **Multi-Agent-Adapter-Registry** — `internal/agents` ist die einzige Wahrheitsquelle für die Agent-CLIs, an die PANDA delegiert (Adapter-Skript, Probe-Binary, Installationsbefehl, Docs-URL). `panda detect`, `panda agents`, die Web-Einstellungs-API und der Verfügbarkeits-Probe des Commanders lesen daraus; einen Agenten hinzuzufügen ist damit eine Ein-Zeilen-Änderung.
- **Vier neue Agent-Adapter** — Grok Build, DeepSeek Harness (`dsh`), OpenClaw und Hermes gesellen sich zu Codex, Claude Code und OpenCode: jeweils eine kleine headless Python-Brücke, die das CLI ausführt und `{ok, result, exit_code}` zurückgibt.
- **`panda agents`** — `list` (Standard) tastet jeden Agenten im PATH mit einer Best-Effort-Version ab; `test <name>` führt einen Verbindungstest aus; `install|update <name>` gibt den Installationsbefehl + Docs-Link aus. Ist nichts installiert, listet die Ausgabe für jeden fehlenden Agenten Installationsbefehl und Download-URL.
- **Agent-Roster in den Web-Einstellungen** — die Agentenliste der Einstellungsseite zeigt jetzt für jeden fehlenden Agenten seinen Installationsbefehl und einen Download-Link (`/api/agents` gibt `install_hint` + `install_url` zurück).
- **Übergeordnete Aufgabenprüfung (`superior task review`)** — nach einem Agent-Lauf bewertet das Entry-Modell das Ergebnis gegen die Erfolgskriterien der Aufgabe (`entry.Supervise`, Ausgabe `done`/`continue`). Ein `continue`-Urteil delegiert die Folgeanweisung (was fehlt + nächster Schritt) erneut an die Agent-Kette, bis der Prüfer die Arbeit annimmt oder ein begrenztes Rundenbudget (Standard 5) aufgebraucht ist.
- **Risikobasierte Endzustands-Weiterleitung** — eine abgeschlossene reversible Aufgabe landet in **done** (已完成); eine akzeptierte irreversible (Tier-2) Aufgabe — Pushes, Löschungen, irreversible Zustandsänderungen — parkt mit ihrem Ergebnis zur menschlichen Freigabe in **review** (待审批); eine Aufgabe, die der Prüfer fortlaufend ablehnt, parkt mit `needs_followup`-Markierung in **review**. Die Prüfereignisse werden im Web-Aufgabendetail wiedergegeben.
- **Ein-Zeilen-Installer** — `scripts/install.sh` (POSIX) und `scripts/install.ps1` (PowerShell) laden das passende Release-Archiv, prüfen dessen SHA-256, entpacken das Binary samt seiner Agent-Adapter in ein nutzerbezogenes Präfix und verlinken `panda` in den `PATH`, optional mit Auto-Start-Dienst (`panda daemon` beim Login). Ein Homebrew-Tap (`brew tap Xustalis/openpanda && brew install openpanda`) deckt macOS ab.
- **Release-Paketierung** — `scripts/package.sh` (und `make package`) cross-kompilieren jede unterstützte Plattform in `dist/panda-<version>-<os>-<arch>.tar.gz` / `.zip` samt einer `checksums.txt`, bereit für GitHub Releases.
- **Self-Update** — `panda web` und `/web` in der REPL prüfen während des Betriebs im Hintergrund den Release-Kanal auf eine neuere CLI; die Web-Konsole lädt und verifiziert das Update und wendet es, sobald die Aufgaben-Warteschlange leer ist, mit einem Klick an (atomarer Binary-Tausch, Adapter-Aktualisierung, Neustart). Das Verwerfen eines heruntergeladenen Updates hinterlässt keine Reste; unter Windows wird der `.old`-Sidecar des Tauschs beim nächsten Start entfernt.

### Behoben

- **Mehrzeilige `--version`-Ausgabe** (z. B. Hermes) verschmutzt die einzeilige Agent-Tabelle nicht mehr — die Versionsausgabe wird sowohl in der CLI als auch in der Web-Einstellungs-API auf die erste Zeile gekürzt.

## [0.0.2] - 2026-08-22

Das CLI-zuerste-Release: Das Kernel-Redesign (Stufen A–C) landet — jede Web-Fähigkeit erhält ein CLI-Pendant, die REPL wird die Vordertür, und die CLI erhält Konversationsgedächtnis, Live-Aufgabenberichte und Markdown-Rendering je nach Ausgabekanal.

### Hinzugefügt

- **CLI-Befehlsfamilien** — jede Web-Fähigkeit hat ein CLI-Pendant: `panda session | task | memory | config | agents | project`, alle teilen die Dienstschicht des Panels; `panda ask` erhält `--output-format json|stream-json` für Headless-Nutzung (a4cba5f).
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
- **Hermes-Gedächtnis und Skills** — Tagesnotizen, Dreaming mit Sedimentation, Projekt-Gedächtnis und ladbare Skills; `panda skill` verwaltet Skill-Freigaben von der CLI und die Konsole trägt eine Skills-Ansicht (9a41b3e, c36cad1).
- **Sprach-Sidecar** — Wachwort, STT, TTS und VAD (hardwaregegate), mit `OPENPANDA_WAKE_KEYWORD` / `OPENPANDA_WAKE_MODEL`-Overrides (84faf08).
- **Echtgeräte-Deployment** — drei Knoten auf Mac / Windows / Orange Pi verifiziert, Scope-Routing und die headless Kernel-Form (0aa9f73, 7f1f8bd).
- **Audit und Migrationen** — `prev_hash`-Audit-Ketten, PRAGMA-`user_version`-SQLite-Migrationen, Slow-DoS-Schutz, MCP-Client-Hard-Timeout (7582754).
- **Scheduler-Mechanismen** — DCPS-gewichtete Bewertung, abgezinst um die TMB-Heartbeat-Frische (30-Minuten-Halbwertszeit); kapazitätsgesteuertes Accept/Decline; Auto-Umleitung bei Ablehnung unter Ausschluss historischer Ablehner (f454909, 7385a89).
- **Einmalige CLI-Panel-Befehle** — `panda status`, `panda queue` und `panda task | cancel | approve | reject | logs` inspizieren den Knoten und verwalten Aufgaben ohne die REPL zu betreten (307e13a).
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
