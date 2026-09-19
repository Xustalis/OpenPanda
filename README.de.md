# 🐼 OpenPanda

**Open-Source, Local-First Betriebssystem für persönliche KI-Agenten und geräteübergreifender Orchestrator**

> Verbinde alle deine Geräte zu einem privaten Peer-to-Peer-Mesh und „heuere“ Terminal-KI-Coding-Agenten (Claude Code, Codex, Grok etc.) an, um als einheitliches Team maschinenübergreifend zusammenzuarbeiten.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release](https://img.shields.io/github/v/release/Xustalis/OpenPanda?label=release&color=blue)](https://github.com/Xustalis/OpenPanda/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
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
                               │ Direktes P2P-WebSocket (Keine Cloud)
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
- **Dynamische Fähigkeitskarten**: Jeder Knoten erfasst automatisch sein Hardwareprofil (CPU, RAM, OS) und verfügbare Agenten.
- **Intelligente Aufgabenverteilung**: Schwere Kompiliervorgänge gehen an leistungsstarke Server, Sensoraufgaben an energieeffiziente Edge-Knoten.
- **Privates P2P-Netzwerk**: Direkte Kommunikation über authentifiziertes WebSocket. Dein Code und Kontext verlassen niemals deine Geräte.

### 2. 🤖 Universelle Agenten-Orchestrierung & Ausfallsicherung
- **Vorkonfigurierte Adapter**: Funktioniert direkt mit Claude Code, OpenAI Codex, Grok Build, DeepSeek Harness, OpenCode und Shell-Befehlen.
- **Automatische Modellinjektion**: Bei aufgebrauchten API-Kontingenten oder ungültigen Schlüsseln (401/403) injiziert OpenPanda automatisch konfigurierte Ausweichmodelle.
- **Vollständige Transparenz**: Verfolge Bash-Befehle, Dateiänderungen und Werkzeugaufrufe live im Terminal oder Browser.

### 3. 🛡️ Autonome Sicherheit & menschliche Freigabe (Human-in-the-Loop)
- **Gestufte Risikobewertung**: Sichere, umkehrbare Aktionen (Code lesen, kompilieren, Tests ausführen) laufen autonom durch.
- **Interaktive Freigabepforten**: Irreversible Aktionen (`git push`, Datenbankänderungen, Dateilöschungen) pausieren sicher für deine Bestätigung.
- **Schutz vor Endlosschleifen**: Aktive Sicherungen verhindern unkontrollierten Token-Verbrauch bei Fehlversuchen.

### 4. 🧠 Zweischichtiger Speicher & lernende Fähigkeiten
- **Strikte Speichertrennung**: Persönliche Präferenzen (`USER.md`) sind strikt vom Projektkontext (`MEMORY.md`) isoliert.
- **Selbstverbessernde Fähigkeiten**: Erfolgreiche Abläufe werden in `SKILL.md`-Leitfäden gespeichert und mit jeder Nutzung präziser.
- **Skills Hub & autonome Erkennung**: Ein kuratierter Offline-Katalog (`panda skill hub`), Import aus Pfad, URL oder Archiv (`panda skill import`), und ein Assistent, der den Skill, den eine Aufgabe braucht, während des Laufs selbst findet und installiert.
- **Projektkontext-Roaming**: Bei der Aufgabenübergabe reisen Projektspeicher und Arbeitsbaum-Zusammenfassungen automatisch mit.

### 5. 🖥️ Drei einheitliche Schnittstellen
- **Interaktive Terminal-TUI**: Bubble Tea mit Vollbild-Alternate-Screen-Modus, Tastaturnavigation, Erststarts-Assistent, Live-Fortschritt und Richtungswechsel während der Ausführung.
- **Integrierte Web-Konsole**: Kanban-Board, Echtzeit-SSE-Streaming, Skills-Verwaltung, Sitzungsabbruch und automatische Anmeldung.
- **Skriptfähige CLI**: Schnelle Befehle wie `panda ask` zur nahtlosen Einbindung in eigene Skripte.

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
brew tap Xustalis/openpanda
brew install openpanda
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

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

1. Auf Gerät A: `panda pair` ausführen, um den Kopplungscode zu erhalten.
2. Auf Gerät B: `panda nodes add <Adresse-von-Gerät-A>` ausführen.
*Beide Geräte sind jetzt in deinem privaten Agenten-Mesh verbunden!*

---

## 🛠️ Befehlsübersicht

| Befehl | Beschreibung |
|---|---|
| `panda` | Vollständige interaktive Bubble Tea TUI starten |
| `panda ask "<anfrage>"` | Direkt ausführen: antworten, Werkzeug nutzen oder delegieren |
| `panda web` | Eingebettete Web-Konsole starten und Browser öffnen |
| `panda nodes` | Verbundene Geräte im P2P-Netzwerk anzeigen |
| `panda pair` | Kopplungscode für neue Knoten erzeugen |
| `panda queue` | Wartende, laufende und zu genehmigende Aufgaben anzeigen |
| `panda approve <id>` | Ausstehende irreversible Stufe-2-Aktion freigeben |
| `panda project list` | Workspace-Projekte und Kontext verwalten |
| `panda skill` | Workflow-Fähigkeiten durchsuchen, importieren und installieren (Hub, URL oder Datei) |
| `panda doctor` | PATH, Konfiguration, Adapter und Datenbank prüfen |
| `panda version` | Aktuelle Binärversion ausgeben |

---

## 🗺️ Fahrplan

| Version | Thema |
|---|---|
| **v0.0.8-preview** (aktuelle Basis) | Multi-Agenten-Orchestrierung auf einer Maschine, voll nutzbar: Absichtsklassifikation, Dispatch, Überwachungsschleife, Failover, gestufte Freigabe |
| **v0.0.9** | Präzisere Agentensteuerung: Überwachungsurteile, Stabilität des Agenten-Routings, Ausführungstransparenz |
| **v0.0.10** | Geräteübergreifende Zusammenarbeit als Hauptthema: knotenübergreifende Delegation, Lease-Schutz, wiederaufnehmbare Ausführung — gehärtet und im Feld getestet |
| **v0.0.x (darüber hinaus)** | Stabilität, Performance und Feinschliff für Randfälle |
| **v0.1.0** | Desktop-Fähigkeiten und stärkere Steuerung und Verwaltung — kommerzielle Qualität |

---

## 🔭 Vision

Die Architektur von OpenPanda ist für große heterogene Cluster ausgelegt: Steuerung von Drohnenschwärmen, Kommunikationsplanung für Satellitenkonstellationen im tiefen Raum und koordinierte Flotten autonomer Fahrzeuge. Diese Szenarien teilen ein grundlegendes Problem — **jeder Knoten hat andere Rechenleistung, andere Fähigkeiten und andere Aufgaben, und trotzdem müssen alle gemeinsam koordiniert, geplant und überwacht werden.**

Heute dient OpenPanda den Geräten und Agenten eines Entwicklers. Das langfristige Ziel ist, denselben Orchestrierungskern auf autonome Knoten in Cluster-Größe auszuweiten.

---

## 🤝 Mitwirken

Wir freuen uns über Beiträge aus der Community! Bitte beachte [CONTRIBUTING.de.md](CONTRIBUTING.de.md), [SECURITY.md](SECURITY.md) und [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

---

## 📄 Lizenz

OpenPanda ist Open-Source-Software unter der [MIT-Lizenz](LICENSE).
