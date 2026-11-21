// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from 'preact/hooks'
import { Icon } from '../components/icons'
import { t } from '../i18n'

export const basicScopes = ['tasks:read', 'members:read']
const hostedTSync = 'https://xustalis.site/tsync/'
const executionScopes = [...basicScopes, 'execution:read', 'execution:claim', 'execution:report', 'delivery:write', 'delivery:report-decision']
const moreScopes = ['tasks:write', 'execution:read', 'execution:claim', 'execution:report', 'delivery:write', 'delivery:report-decision']

export function SpaceConnectForm({ scopes, setScopes, busy, connect, existingRoot }: {
  existingRoot?: string; scopes: string[]; setScopes(next: string[]): void; busy: boolean; connect(url: string): void
}) {
  const [custom, setCustom] = useState(false)
  const [root, setRoot] = useState('')
  const destination = existingRoot || (custom ? root.trim() : hostedTSync)
  const option = (scope: string) => <label class="space-option" key={scope}>
    <input type="checkbox" checked={scopes.includes(scope)} disabled={busy} onChange={e => setScopes(e.currentTarget.checked ? [...scopes, scope] : scopes.filter(s => s !== scope))} />
    <span>{t(`work.scope.${scope}`)}</span>
  </label>
  return <form class="space-connect" aria-label={t(existingRoot ? 'work.manageAccess' : 'work.bindAccount')} onSubmit={e => { e.preventDefault(); if (destination && scopes.length) connect(destination) }}>
    <header class="space-intro"><span class="space-symbol"><Icon name="users" size={24} /></span><div><h2>{t(existingRoot ? 'work.manageAccess' : 'work.bindAccount')}</h2><p>{t('work.bindIntro')}</p></div></header>
    <fieldset class="space-modes"><legend>{t('work.connectionMode')}</legend>
      {([['read', basicScopes], ['execute', executionScopes]] as const).map(([mode, preset]) => <label class="space-mode" key={mode}>
        <input type="radio" name="access-mode" disabled={busy} checked={scopes.length === preset.length && preset.every(scope => scopes.includes(scope))} onChange={() => setScopes([...preset])} />
        <span><strong>{t(`work.mode.${mode}`)}</strong><small>{t(`work.mode.${mode}Hint`)}</small></span>
      </label>)}
    </fieldset>
    <details class="space-more"><summary>{t('work.moreScopes')}<Icon name="chevron-down" size={14} /></summary><div class="space-options">{[...basicScopes, ...moreScopes].map(option)}</div></details>
    <div class="space-service"><span>{existingRoot || (custom ? t('work.spaceUrl') : 'xustalis.site/tsync')}</span>{!existingRoot && <button class="btn" type="button" disabled={busy} aria-expanded={custom} onClick={() => setCustom(!custom)}>{t(custom ? 'work.useHosted' : 'work.otherService')}</button>}</div>
    <div class="space-entry">{custom && <input aria-label={t('work.spaceUrl')} type="url" required value={root} disabled={busy} spellcheck={false} placeholder={t('work.urlPlaceholder')} onInput={e => setRoot(e.currentTarget.value)} />}
      <button class="btn primary" type="submit" disabled={busy || !destination || scopes.length === 0}>{t(existingRoot ? 'work.manageAccess' : 'work.bindAccount')}<Icon name="arrow-right" size={16} /></button></div>
  </form>
}
