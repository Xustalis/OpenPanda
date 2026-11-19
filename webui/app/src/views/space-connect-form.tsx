// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from 'preact/hooks'
import { Icon } from '../components/icons'
import { locale, t } from '../i18n'

export const basicScopes = ['tasks:read', 'members:read']
const hostedTSync = 'https://xustalis.site/tsync/'
const moreScopes = ['tasks:write', 'execution:read', 'execution:claim', 'execution:report', 'delivery:write', 'delivery:report-decision']

export function SpaceConnectForm({ scopes, setScopes, busy, connect }: {
  scopes: string[]; setScopes(next: string[]): void; busy: boolean; connect(url: string): void
}) {
  const [custom, setCustom] = useState(false)
  const [root, setRoot] = useState('')
  const destination = custom ? root.trim() : hostedTSync
  const option = (scope: string) => <label class="space-option" key={scope}>
    <input type="checkbox" checked={scopes.includes(scope)} disabled={busy} onChange={e => setScopes(e.currentTarget.checked ? [...scopes, scope] : scopes.filter(s => s !== scope))} />
    <span>{t(`work.scope.${scope}`)}</span>
  </label>
  return <form class="space-connect" aria-label={t('work.bindAccount')} onSubmit={e => { e.preventDefault(); if (destination && scopes.length) connect(destination) }}>
    <header class="space-intro"><span class="space-symbol"><Icon name="users" size={24} /></span><div><h2>{t('work.bindAccount')}</h2><p>{t('work.bindIntro')}</p></div></header>
    <div class="space-service"><span>{custom ? t('work.spaceUrl') : 'xustalis.site/tsync'}</span><button class="btn" type="button" disabled={busy} aria-expanded={custom} onClick={() => setCustom(!custom)}>{t(custom ? 'work.useHosted' : 'work.otherService')}</button></div>
    <div class="space-entry">{custom && <input id="space-url" aria-label={t('work.spaceUrl')} type="url" required value={root} disabled={busy} spellcheck={false} placeholder={t('work.urlPlaceholder')} onInput={e => setRoot(e.currentTarget.value)} />}
      <button class="btn primary" type="submit" disabled={busy || !destination || scopes.length === 0}>{t('work.bindAccount')}<Icon name="arrow-right" size={16} /></button></div>
    <div class="space-consent"><Icon name="shield" size={16} /><p>{basicScopes.length === scopes.length && basicScopes.every(s => scopes.includes(s)) ? t('work.readOnlySummary') : scopes.length ? t('work.accessSummary', { actions: new Intl.ListFormat(locale()).format(scopes.map(s => t(`work.scope.${s}`))) }) : t('work.chooseOne')}</p></div>
    <details class="space-more"><summary>{t('work.moreScopes')}<Icon name="chevron-down" size={14} /></summary><div class="space-options">{[...basicScopes, ...moreScopes].map(option)}</div></details>
  </form>
}
