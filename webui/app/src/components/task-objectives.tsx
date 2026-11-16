// SPDX-License-Identifier: AGPL-3.0-or-later
import { t } from '../i18n'
import type { Task } from '../api/client'

export function TaskObjectives({ task }: { task: Task }) {
 let policy: { revision?: number; dimensions?: { dimension: string; priority: string; weight: number; requirement: string; reason: string; criteria: string[] }[] } | undefined
 try { policy = JSON.parse(task.spec ?? '{}').objectives } catch { /* legacy task */ }
 if (!policy?.dimensions?.length) return null
 return <section class="task-objectives" aria-label={t('activity.objectives')}>
  <h4>{t('activity.objectives')} <span class="dim">v{policy.revision}</span></h4>
  {policy.dimensions.map(d => <details key={d.dimension} class="task-objective" open>
   <summary><strong>{d.dimension}</strong><span class={'objective-priority is-' + d.priority.toLowerCase()}>{d.priority === 'MAX' ? 'MAX' : Math.round(d.weight * 100) + '%'}</span></summary>
   <p>{d.requirement}</p><p class="dim">{d.reason}</p>
   {!!d.criteria?.length && <ul>{d.criteria.map((c, i) => <li key={i}>{c}</li>)}</ul>}
  </details>)}
 </section>
}
