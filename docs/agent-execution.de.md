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
- Parallele Schreibzugriffe erfordern getrennte `paths`, andere `workdir`-Werte oder `isolate`. Überlappende oder unbekannte Bereiche bleiben seriell.

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
