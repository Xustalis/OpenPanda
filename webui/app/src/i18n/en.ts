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
}

export default en
