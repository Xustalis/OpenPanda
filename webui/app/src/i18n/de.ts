import type { Messages } from './index'

const de: Messages = {
  // Shell / navigation
  'app.name': 'OpenPanda',
  'app.tagline': 'Persönliche Aufgaben-Orchestrierung über deine Geräte hinweg',
  'nav.queue': 'Warteschlange',
  'nav.ask': 'Fragen',
  'nav.projects': 'Projekte',
  'nav.nodes': 'Knoten',

  // Token gate
  'token.title': 'Mit deinem Knoten verbinden',
  'token.description':
    'Gib das Panel-Token aus der Konfiguration dieses Knotens ein (network.panel_token in config.yaml).',
  'token.label': 'Panel-Token',
  'token.submit': 'Verbinden',
  'token.invalid': 'Token abgelehnt — prüfen und erneut versuchen.',
  'token.logout': 'Trennen',

  // Common
  'common.loading': 'Lädt…',
  'common.error': 'Etwas ist schiefgelaufen.',
  'common.retry': 'Erneut versuchen',
  'common.cancel': 'Abbrechen',
  'common.save': 'Speichern',
  'common.close': 'Schließen',
  'common.empty': 'Hier ist noch nichts.',

  // Task states
  'state.submitted': 'eingereicht',
  'state.queued': 'in Warteschlange',
  'state.dispatched': 'zugewiesen',
  'state.waiting_context': 'wartet auf Kontext',
  'state.running': 'läuft',
  'state.review': 'Prüfung erforderlich',
  'state.done': 'fertig',
  'state.failed': 'fehlgeschlagen',
  'state.cancelled': 'abgebrochen',
  'state.expired': 'abgelaufen',

  // Queue view
  'queue.subtitle': 'Alle Aufgaben, die dieser Knoten kennt — live.',
  'queue.allStates': 'Alle Zustände',
  'queue.allProjects': 'Alle Projekte',
  'queue.empty': 'Keine Aufgaben. Sag etwas unter „Fragen“, um eine zu erstellen.',
  'queue.title': 'Titel',
  'queue.project': 'Projekt',
  'queue.state': 'Zustand',
  'queue.owner': 'Verantwortlich',
  'queue.updated': 'Aktualisiert',

  // Detail view
  'detail.back': 'Zurück zur Warteschlange',
  'detail.approve': 'Genehmigen',
  'detail.reject': 'Ablehnen',
  'detail.cancelTask': 'Aufgabe abbrechen',
  'detail.rejectedViaWeb': 'über Web-Panel abgelehnt',
  'detail.id': 'Aufgaben-ID',
  'detail.project': 'Projekt',
  'detail.owner': 'Verantwortlicher Knoten',
  'detail.attempt': 'Versuch',
  'detail.created': 'Erstellt',
  'detail.updated': 'Aktualisiert',
  'detail.risk': 'Risiko',
  'detail.result': 'Ergebnis',
  'detail.timeline': 'Zeitverlauf',

  // Ask view
  'ask.subtitle': 'Eine Eingabe — eine Antwort oder eine delegierte Aufgabe.',
  'ask.hint': 'Frag alles. OpenPanda antwortet direkt, führt kleine Werkzeuge aus oder delegiert eine Aufgabe an deine Geräte.',
  'ask.placeholder': 'Frag OpenPanda… (Enter zum Senden, Shift+Enter für neue Zeile)',
  'ask.authorize': 'Risikoreiche Aktionen erlauben',
  'ask.voice': 'Spracheingabe',
  'ask.send': 'Senden',
  'ask.thinking': 'Denkt nach…',
  'ask.taskCreated': 'Aufgabe erstellt.',

  // Projects view
  'projects.subtitle': 'Ein Projekt gliedert Aufgaben-Warteschlangen und projektbezogenes Gedächtnis.',
  'projects.namePlaceholder': 'Neuer Projektname',
  'projects.create': 'Erstellen',
  'projects.empty': 'Noch keine Projekte — oben eines erstellen.',

  // Nodes view
  'nodes.subtitle': 'Das Fähigkeitenverzeichnis: alle Geräte, an die dieser Knoten delegieren kann.',
  'nodes.empty': 'Noch keine Knoten registriert.',
  'nodes.lastSeen': 'Zuletzt gesehen',
  'nodes.never': 'nie',
}

export default de
