// Last-resort render guard. A render-time crash anywhere in the tree used to
// blank the whole console with no way back short of a reload; this catches
// it, explains it, and offers a retry that simply remounts the app tree.

import { Component, type ComponentChildren } from 'preact'
import { t } from '../i18n'

interface Props {
  children?: ComponentChildren
}

interface State {
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  componentDidCatch(error: Error): void {
    // The panel below tells the user what happened; the console gets the
    // real thing for debugging.
    console.error('[openpanda] render crashed:', error)
    this.setState({ error })
  }

  private retry = () => this.setState({ error: null })

  render() {
    const { error } = this.state
    if (!error) return this.props.children
    return (
      <div class="crash-panel" role="alert">
        <div class="crash-box">
          <h1>{t('common.error')}</h1>
          <p class="dim">{error.message || String(error)}</p>
          <button class="btn primary" onClick={this.retry}>
            {t('common.retry')}
          </button>
        </div>
      </div>
    )
  }
}
