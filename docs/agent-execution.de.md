# Agentenmodelle und Parallelitätsgrenzen

Unter **Einstellungen → Agents** lassen sich Haupt- und Kindmodelle sowie Parallelitäts- und Delegationsgrenzen konfigurieren. Beispielmodelle durch tatsächlich verfügbare CLI-Modell-IDs ersetzen.

```yaml
injection:
  model: never
routing:
  max_concurrent: 6
  agent_concurrency:
    codex: 2
    claude_code: 2
  agent_models:
    codex: [my-root-model]
    claude_code: [my-claude-model]
  subagent_models:
    codex: [my-child-model]
    claude_code: [my-child-claude-model]
delegation:
  enabled: true
  max_concurrent: 3
  fanout: 3
  max_children: 12
  max_depth: 2
```

- `routing.max_concurrent` begrenzt aktive Agentenaufrufe und Queue-Zulassung dieses Workers. Null erbt die Fähigkeitskarte.
- `agent_concurrency` ergänzt Limits je Agent. Schlüssel sind Kartennamen oder Adapterdateien; Aliasnamen teilen das dateibasierte Limit.
- `subagent_models` gilt für von OpenPanda erzeugte Kinder. Fehlende Einträge erben das Hauptmodell; explizites `[]` nutzt den CLI-Standard. Ein leeres UI-Feld stellt Vererbung wieder her.
- `delegation.max_concurrent` begrenzt aktive Kinder über alle Eltern; null fügt kein Limit hinzu. `fanout` begrenzt erzeugte und erwartete Kinder je Elternaufgabe, `max_children` die kumulative Anzahl und `max_depth` die Generationen.
- Eine Senkung unterbricht laufende Aufrufe nicht. Wartende Eltern belegen keinen Agentenaufrufplatz.
- Dies sind Limits aktiver Adapteraufrufe eines Workers, keine Clusterquote oder Kontrolle interner Anbieteragenten bzw. inaktiver Sitzungen. Unabhängige Worker teilen kein striktes Prozesssemaphor.
- Bei einem projektgebundenen Batch mit effektivem `fanout` über eins werden Geschwister ohne `paths`, `isolate` und `workdir` automatisch isoliert und können parallel laufen, begrenzt durch Worker-, Agent- und Delegationslimits; Änderungen werden konfliktbewusst zusammengeführt. Eine einzelne Aufgabe ohne Bereich sperrt weiterhin das ganze Projekt. Explizite, disjunkte `paths` vermeiden die Kopie; überlappende Pfade im gemeinsamen Arbeitsbaum bleiben seriell.

Die Modellwahl ist CLI-nativ. Injizierte Modelle und explizite CLI-Modellvariablen behalten Vorrang. Das Beispiel nutzt mit `injection.model: never` eigene CLI-Anmeldedaten; nicht übernehmen, wenn Panda-Zugangsdaten benötigt werden. Für Endpoint und Zugangsdaten weiterhin `injection.agents` nutzen. Einmodell-CLIs wählen den ersten Kandidaten, OpenCode prüft seinen Katalog. ZCode unterstützt `provider/model`, generic-Vorlagen verwenden `{model}`.

Speichern gilt im aktuellen Worker sofort. Andere lokale Worker synchronisieren etwa jede Sekunde und vor der Ausführung. Laufende Aufrufe behalten ihre Modellsnapshots.

```sh
panda config routing set agent_models codex my-root-model
panda config routing set subagent_models codex my-child-model
panda config routing set max_concurrent 6
panda config routing set agent_concurrency codex 2
panda config delegation set max_concurrent 3
panda config delegation set fanout 3
panda config routing get --json
```

API: `GET /api/settings/execution`, `PUT /api/settings/execution` (Bearer token).

## Ausführungsfristen und Kontext

- Kandidatenwechsel, Wiederholungen bei vorübergehenden Fehlern und Credential-Fallback teilen eine Frist pro Runde: Aufgabenwert oder globales Timeout plus Beendigungsfrist. Nach Abbruch starten keine neuen Kandidaten. Eine Panic beendet den eigenen Versuch und gibt Ressourcen frei.
- OpenCode beendet Aufrufe bei Provider-/Sitzungsfehlern sowie bei terminalem Idle nach einer Antwort. Initiales Idle, Werkzeugfehler und fremde Sitzungsereignisse gelten nicht als Abschluss. Verwaltete Codex-Aufgaben speichern Sitzungen für die Fortsetzung; eigenständige Diagnosen bleiben standardmäßig temporär. `thread.started` und ältere Sitzungsereignisse werden unterstützt.
- Kontext erhält je nach Komplexität ein weiches Budget von 12.000 / 32.000 / 64.000 Bytes; unbekannte Komplexität verwendet 32.000. Anforderungen, Abnahmekriterien, Geltungsbereich und Protokollregeln bleiben vollständig. Skill-Inhalte und Kurzindizes teilen das Restbudget. Persönliche Speicherindizes enthalten höchstens sechs relevante Themen innerhalb von 4.000 Bytes; die Projektisolation bleibt erhalten. `panda help` erschließt den vollständigen Befehlskatalog.
- Bei Kontextüberlauf wird das Budget halbiert und auf höchstens 32.000 Bytes begrenzt. Laufnotizen können auf 12.000 Bytes mit sichtbaren Auslassungen und gültigem UTF-8 schrumpfen. Fortsetzungen behalten vollständige Aufgabenanforderungen für Kaltstarts. `prompt_bytes` und `context_budget_bytes` in `exec_agent_start` messen Bytes, keine Token.
- Prüfung und Zusammenfassung verwenden dasselbe Modell mit separater Kurzkonfiguration: höchstens 2.048 Ausgabetoken, eine Transportwiederholung und angeforderte Deaktivierung erweiterten Denkens, abhängig vom Provider. Die Prüfung hat eine Frist von 45 Sekunden; Fehler gehen weiterhin zur menschlichen Prüfung. Dialog-/Agent-Konfigurationen bleiben unverändert. Das lesende Werkzeug `model_list` beantwortet Fragen zu konfigurierten/aktiven Modellen ohne Agent-Aufgabe.

Die Änderungen erfordern die Bereitstellung der neuen Binärdatei und Adapter; Konfigurations-Hot-Reload ersetzt keinen laufenden Programmcode.

## Untersuchungsaufgaben und Ausführungsnachweise

- Der Spec unterstützt `task_kind`: `code_question`, `investigation`, `edit` oder `operations`. Ohne Feld bleiben alte Aufgaben kompatibel; es erteilt keine Berechtigung. Untersuchungen/Codefragen nutzen schreibgeschütztes `plan`; Änderungen beachten die Gesprächsrechte. Tiefe Untersuchungen gehören in einen Coding-Harness. Eine fehlgeschlagene Delegation macht eine Verzeichnisübersicht nicht zu einer abgeschlossenen Prüfung.
- Untersuchungen benötigen Implementierungs-, Aufrufer-/relevante Testbelege, Umfang und offene Punkte. Statische Analyse erfordert keinen Build/Test. Begrenzte Suchen beweisen kein repositoryweites Fehlen. Änderungen berichten relevante Prüfergebnisse, Operationen prüfen den gewünschten Folgezustand. Schreibgeschützte Aufgaben führen acceptance-Befehle nicht automatisch aus.
- `execution_environment` protokolliert je Attempt lokal beim Worker cwd, Git-Commit/Branch/Dirty-Status (auch unversionierte Dateien) und größenbegrenzte Fingerabdrücke der Root-Dateien `AGENTS.md`/`CLAUDE.md`. Kein vollständiger Regelindex oder Abhängigkeitstest; keine Regeltexte und keine Projektbefehle. Fehlgeschlagene Git-Prüfungen bleiben unknown.
- `adapter_execution_profile` erfasst an der vertrauenswürdigen Prozessgrenze angeforderte Modellpräferenz, Injektion/Werkzeugrichtlinie, Einschränkung/Zustimmung, Limits, Wiederaufnahme und deklarierte Fähigkeiten. Persistente Sitzungen behalten die Startkonfiguration. Dies bestätigt keine native CLI-Konfiguration: unbestätigtes Modell/Effort/Version bleiben unknown. Prompts, Zugangsdaten, Provider-URLs, MCP-Inhalte und Befehlsschablonen fehlen absichtlich.
- Worker-lokale Ereignisse ohne neue spezielle UI oder knotenübergreifende Umgebungssynchronisierung; kein vollständiges Aussagen-/Evidenzregister oder Abschlussprüfer. Die neue Binärdatei muss bereitgestellt werden.

## Grenzen für Routing, Berechtigungen und Pläne

- Begriffliche Fragen behalten den Schnellpfad. Konkrete Codesymbole/Pfade erfordern das Lesen des tatsächlichen Quelltexts statt einer Antwort ohne Werkzeuge. Tiefgehende Repository-Untersuchungen und vertiefende Rückfragen im Repository-Kontext (einschließlich Side) benötigen einen Coding-Harness. Fehlende Delegierung, Warteschlangen-/Genehmigungsbelege und Fehler dürfen durch Konvergenzzusammenfassungen nicht zu abgeschlossener Arbeit werden. Dieses Routing ändert weder den Reasoning-Effort des nativen CLI noch prüft es jede Aussage gegen Belege.
- Die globale Werkzeugrichtlinie ist eine Obergrenze; Aufgaben dürfen sie nur einschränken. `extended` gewährt keine zusätzlichen Betriebssystemrechte. Codex verwendet normalerweise `workspace-write`, bei Einschränkung `read-only`, und wechselt aufgrund der Werkzeugrichtlinie nicht zu `danger-full-access`. Typisierte Untersuchungen/Codefragen bleiben auch bei gespeicherter Zustimmung schreibgeschützt; native Befehle ohne Read-only-Sandbox sind dafür nicht zulässig.
- Eingangswerkzeuge einer `plan`-Unterhaltung benötigen eine explizite Read-only-Deklaration; lokale Kurzbefehle beachten dieselbe Grenze. `task_submit`/`task_budget` behalten die eingeschränkte Delegierung. Unbekannte MCP-Werkzeuge ohne Deklaration werden standardmäßig abgewiesen; eine vertrauenswürdige MCP-Read-only-Konfiguration ist noch nicht implementiert. Skill-Lesezugriffe betrachten nur vorhandene Inhalte, ohne integrierte Skills implizit zu installieren.
- Mehrstufige Pläne erhalten `task_kind`, Unterhaltungsberechtigungen und Agentenauswahl und übernehmen einen begrenzten Snapshot des Quellarbeitsbereichs. Fehlender Artifact-Speicher, Packfehler oder ausgelassene Dateien führen vor der Aufgabenerstellung zum Fehler. Entfernte Stufen verwenden eigene Verzeichnisse des empfangenden Knotens, niemals das absolute cwd des Ursprungs. Ergebnisse werden als Artifacts weitergereicht und **nicht automatisch in den ursprünglichen Checkout zurückgeschrieben oder zusammengeführt**.
- Klassifizierung mit Arbeitsbereichsübersicht oder Lesewerkzeugen umgeht persistente, nur pfadbasierte Caches, damit nicht eingecheckte Änderungen keine veralteten Urteile wiederverwenden. Verifikationsbeobachtungen erfordern abgeschlossene, nicht als fehlerhaft markierte Befehlswerkzeug-Ergebnisse sowie exakt passende Programme und Argumente. `echo`, Teilzeichenfolgen, unfertige oder fehlgeschlagene Befehle zählen nicht; komplexe Shell-Verknüpfungen werden nicht interpretiert. Diese Beobachtungen ersetzen keine unabhängige bestandene Abnahme.
- Side führt einen eigenen Verlauf und nutzt dieselben Werkzeuge (einschließlich Delegierung) sowie Berechtigungs-/Freigaberegeln wie der Hauptthread. Dessen Verlauf ist nur Referenz und wird von Side weder ergänzt noch komprimiert. Schnell-/Tiefenrouting und Reasoning-Konfiguration sind getrennte Ebenen und garantieren keine Gleichheit mit dem nativen Werkzeug.
- Entfernte Pläne erhalten weiterhin Projektwissen, ohne einen weiteren Checkout anzuhängen; Quell-Snapshots und die Reihenfolge der Vorgängereingaben bleiben erhalten.

## Stufenabnahme, Blockaden und Planung

- Generierte und YAML-Stufen unterstützen `acceptance`, `success_definition`, `tools_policy` und `max_turns`. Pfade, Richtlinien und das Rundenlimit 0..200 werden geprüft; die Werkzeugrichtlinie erteilt keine Autorisierung und erweitert keine Einschränkung.
- Das Urteil `blocked` verlangt geänderte äußere Bedingungen oder Eingaben und hält die Aufgabe zur Prüfung an. Eine Genehmigung erklärt bekannte unvollständige Arbeit nicht für fertig; sie muss fortgesetzt oder abgelehnt werden.
- Unabhängige Zweige auf demselben Rechner können innerhalb von Abhängigkeiten, Kapazität und Ressourcensperren parallel laufen. Innerhalb eines Plans und einer Priorität bestimmt der verbleibende gewichtete kritische Pfad die Reihenfolge; manuelle Warteschlangenreihenfolge hat Vorrang.

## Gesprächswiederherstellung und Fortschritt

Anbieter-Zusammenfassungen werden getrennt von der Antwort gespeichert. Sie dienen nur der Anzeige, werden nicht erfunden und nicht an das Modell zurückgesendet. Laufender Text, Zusammenfassungen und Fortschritt werden gesichert; nach dem Neuladen stoppt dieselbe Operations-ID den Vorgang und bewahrt bereits empfangene Inhalte.

Die Komprimierung erhält den Originalverlauf und begrenzt den Modellkontext über einen Zusammenfassungszeiger. Von älteren Versionen gelöschte Nachrichten oder nie gespeicherte Zusammenfassungen sind nicht rekonstruierbar. Native CLI-Zugangsdaten und Endpunkte bleiben erhalten; Codex-Injektion nutzt Responses. Guthabenfehler erfordern nach erlaubter Rettung eine Kontokorrektur statt endloser Wiederholung.

Nach Abschluss bleibt ein eigener Übergabebericht im ursprünglichen Gespräch erhalten (bis zu 24.000 Zeichen; gekürzte Berichte verweisen ausdrücklich auf die Originalausgabe). Die Modellhistorie verwendet nur die kurze Zusammenfassung. Tatsächliche Schritte, geänderte Pfade, unabhängige Prüfung und offene Punkte sind direkt sichtbar; Bericht und Verlauf sind aufklappbar. `done` bedeutet ohne dokumentierte Prüfungen nicht verifiziert. Ältere Berichte übernehmen nur Ausgaben desselben Versuchs, damit Wiederholungen sie nicht umschreiben. Die TUI zeigt Ergebnis, Schritte, Prüfung und Hinweise für Vordergrund- und Hintergrundabschlüsse.

- Während sie auf Kinder warten, geben Eltern Ausführungskapazität und Arbeitsverzeichnissperren frei. Heartbeat, Abbruch und Geräteausschluss bleiben aktiv. Vor der Fortsetzung werden Kapazität und Sperren erneut erworben. Der Wartestatus gilt für einen Versuch und wird bei Wiederholung, Abbruch oder Wiederherstellung nach Neustart gelöscht.
- Fehler beim isolierten Zusammenführen, fehlende Ausgangsdaten und Konflikte blockieren die Aufgabe zur Prüfung mit geänderter Eingabe; das Arbeitsverzeichnis bleibt erhalten. Eine Freigabe allein bestätigt keine Lieferung. Geschwister teilen pro Stapel einen unveränderlichen Quellstand; explizite Arbeitsverzeichnisse behalten ihre Bedeutung.
- Der vollständige Aufgabengraph wird in einer Transaktion veröffentlicht. Vom Kern erzeugte kausale Metadaten erhalten Änderungen und Löschungen. Nachfahren haben Vorrang vor Vorfahren; unterschiedliche gleichzeitige Änderungen erzeugen Konflikte, gleiche Ergebnisse werden zusammengeführt. Neue Pläne gehen nur an Knoten mit angekündigter Protokollunterstützung. Mehrere alte Snapshots ohne verlässliche Basis werden blockiert. Planausgaben werden nicht automatisch in den ursprünglichen Checkout zurückgeschrieben.
