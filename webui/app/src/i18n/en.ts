import type { Messages } from './index'

// English is the fallback dictionary: every key used anywhere in the app
// must exist here (the other locales may lag behind and fall back gracefully).
const en: Messages = {
  // Shell / navigation
  'app.name': 'OpenPanda',
  'app.tagline': 'Personal task orchestration across your devices',
  'nav.queue': 'Queue',
  'nav.ask': 'Ask',
  'nav.projects': 'Projects',
  'nav.nodes': 'Nodes',

  // Token gate
  'token.title': 'Connect to your node',
  'token.description':
    'Enter the panel token from this node\'s config (network.panel_token in config.yaml).',
  'token.label': 'Panel token',
  'token.submit': 'Connect',
  'token.invalid': 'Token rejected — check it and try again.',
  'token.logout': 'Disconnect',

  // Common
  'common.loading': 'Loading…',
  'common.error': 'Something went wrong.',
  'common.retry': 'Retry',
  'common.cancel': 'Cancel',
  'common.save': 'Save',
  'common.close': 'Close',
  'common.empty': 'Nothing here yet.',

  // Task states (wire-stable identifiers from internal/core/state.go)
  'state.submitted': 'submitted',
  'state.queued': 'queued',
  'state.dispatched': 'dispatched',
  'state.waiting_context': 'waiting for context',
  'state.running': 'running',
  'state.review': 'needs review',
  'state.done': 'done',
  'state.failed': 'failed',
  'state.cancelled': 'cancelled',
  'state.expired': 'expired',

  // Queue view
  'queue.subtitle': 'Every task this node knows about, live.',
  'queue.allStates': 'All states',
  'queue.allProjects': 'All projects',
  'queue.empty': 'No tasks. Say something in Ask to create one.',
  'queue.title': 'Title',
  'queue.project': 'Project',
  'queue.state': 'State',
  'queue.owner': 'Owner',
  'queue.updated': 'Updated',

  // Detail view
  'detail.back': 'Back to queue',
  'detail.approve': 'Approve',
  'detail.reject': 'Reject',
  'detail.cancelTask': 'Cancel task',
  'detail.rejectedViaWeb': 'rejected via web panel',
  'detail.id': 'Task ID',
  'detail.project': 'Project',
  'detail.owner': 'Owner node',
  'detail.attempt': 'Attempt',
  'detail.created': 'Created',
  'detail.updated': 'Updated',
  'detail.risk': 'Risk',
  'detail.result': 'Result',
  'detail.timeline': 'Timeline',

  // Ask view
  'ask.subtitle': 'One prompt in — an answer or a delegated task out.',
  'ask.hint':
    'Ask anything. OpenPanda answers directly, runs small tools, or submits a task to your devices.',
  'ask.placeholder': 'Ask OpenPanda… (Enter to send, Shift+Enter for a new line)',
  'ask.authorize': 'Authorize risky actions',
  'ask.voice': 'Voice input',
  'ask.send': 'Send',
  'ask.thinking': 'Thinking…',
  'ask.taskCreated': 'Task created.',

  // Projects view
  'projects.subtitle': 'A project scopes task queues and per-project memory.',
  'projects.namePlaceholder': 'new project name',
  'projects.create': 'Create',
  'projects.empty': 'No projects yet — create one above.',

  // Nodes view
  'nodes.subtitle': 'The capability directory: every device this node can delegate to.',
  'nodes.empty': 'No nodes registered yet.',
  'nodes.lastSeen': 'Last seen',
  'nodes.never': 'never',
}

export default en
