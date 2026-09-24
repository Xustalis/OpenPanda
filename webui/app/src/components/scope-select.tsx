import type { ApprovalScope } from '../api/client'
import { t } from '../i18n'

/** Where an approval answer is remembered — the scope half of every
 *  approve/reject choice. `once` stores nothing; `session` answers this
 *  conversation's later gates in memory; `project` persists on the project
 *  row so every later session inherits the answer. The select stays small
 *  enough to ride inside an approval card's action row. */
export function ScopeSelect(props: {
  value: ApprovalScope
  onChange(scope: ApprovalScope): void
  disabled?: boolean
  id?: string
}) {
  return (
    <select
      class="scope-select"
      id={props.id}
      value={props.value}
      disabled={props.disabled}
      title={t('approval.scopeHint')}
      aria-label={t('approval.scope')}
      onChange={(e) => props.onChange((e.target as HTMLSelectElement).value as ApprovalScope)}
    >
      <option value="once">{t('approval.scopeOnce')}</option>
      <option value="session">{t('approval.scopeSession')}</option>
      <option value="project">{t('approval.scopeProject')}</option>
    </select>
  )
}
