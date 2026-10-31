// SPDX-License-Identifier: AGPL-3.0-or-later

// The task detail page's sections, as components — so the chat's task card can
// show the same task the same way.
//
// These used to be private to `views/detail.tsx`. The card in the chat flyout
// had grown its own, thinner version of each (a JSON blob for the brief, the
// raw result envelope, no log at all), which meant the same task read
// differently depending on where you opened it — and the card's version was
// always the worse one. One implementation, two hosts: the page lays them out
// at 980px, the card stacks them in 360px, and the rendering rules live here.
//
// Everything below is presentation only: no fetching, no navigation of its
// own. `onOpenTask` is passed in by whichever host has somewhere to go.

import { useState } from 'preact/hooks'
import type { Task, TaskEvent } from '../api/client'
import { locale, t } from '../i18n'
import { formatDuration } from '../format/time'
import { AgentTag } from './agent-tag'
import { EventTimeline } from './event-timeline'
import { Icon } from './icons'

/** Rows of a long log shown before the reader asks for the rest. */
export const TIMELINE_TAIL = 5

/** Parses a result/spec blob into an object, or returns null when it is not
 *  one. Callers treat null as "show the text as-is". */
export function parseResult(raw: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(raw)
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null
  } catch {
    return null
  }
}

export function text(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

export function shortId(id: string): string {
  return id ? id.slice(0, 8) : '—'
}

/** Markdown decoration a one-line quote should not carry: a run that opens its
 *  report with `## 测试结果` should not have the verdict block read out the
 *  hashes. Also the rule the output card uses to recognise its own opening
 *  line once the decoration is gone. */
const MD_DECOR = /^[\s#>*_`~-]+/

export function plainLine(s: string): string {
  return s.replace(MD_DECOR, '')
}

export function fmtFull(iso: string): string {
  return iso ? new Date(iso).toLocaleString(locale()) : '—'
}

/** Compact stamp for the header line: the year is noise on a run that started
 *  today, and the seconds never helped anyone. The locale decides field order
 *  and the 12/24-hour cycle rather than a hand-rolled "MM-DD HH:mm" that
 *  reads as 24-hour time to a reader whose locale never shows it. */
export function fmtShort(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(locale(), {
      month: 'numeric',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
    }).format(d)
  } catch {
    return '—'
  }
}

/** What a spec has to say, before any of it is drawn: the counts a panel head
 *  quotes, the test that decides whether a fold is worth drawing, and the
 *  fields the body renders. Split out so the page's 任务简报 card and the card
 *  itself cannot disagree about whether there is a brief at all. */
export function briefShape(spec?: string, fallback?: string) {
  const s = spec ? parseResult(spec) : null
  const target = text(s?.target) || (fallback ?? '').trim()
  const success = text(s?.success_definition)
  const constraints = Array.isArray(s?.constraints)
    ? s.constraints.filter((c): c is string => typeof c === 'string' && c !== '')
    : []
  // Which agents the dispatcher allowed this run to use. It is the one field
  // that explains a task being run by the "wrong" CLI, and it was only ever
  // readable in the raw spec blob.
  const agents = Array.isArray(s?.allow_agents)
    ? s.allow_agents.filter((a): a is string => typeof a === 'string' && a !== '')
    : []
  const scopeFiles = text(s?.scope)
    .split(/[,;\n]/)
    .map((f) => f.trim())
    .filter(Boolean)
  // The sentence the user actually typed, when the dispatcher wrapped it into
  // the intent envelope. The brief is the dispatcher's *reading* of it —
  // target, constraints, the bar — so the original is only worth a block when
  // it says something those three do not.
  const raw = rawRequestOf(fallback ?? '', target)
  return {
    target,
    success,
    constraints,
    agents,
    scopeFiles,
    raw,
    has: Boolean(
      target || success || constraints.length > 0 || agents.length > 0 || scopeFiles.length > 0,
    ),
    // A brief that is only a target has nothing to unfold — the toggle used to
    // sit there re-printing the sentence directly above it.
    more:
      constraints.length > 0 ||
      scopeFiles.length > 0 ||
      agents.length > 0 ||
      Boolean(success) ||
      Boolean(raw),
  }
}

/** The task spec (spec_json) as a readable brief: target as prose,
 *  constraints as a checklist, scope as file chips, success as the bar the
 *  supervisor evaluates. Absent or unparseable specs render nothing —
 *  older tasks simply have no brief.
 *
 *  Folded by default, but never silent: the collapsed state still shows the
 *  first sentence of the target, so the reader always gets "what was this
 *  asked to do" without a click. Open, the brief was 266px of dense prose
 *  sitting between the result and the log.
 *
 *  `fallback` is the intent sentence the user actually typed. A task with no
 *  spec envelope still has one, and a brief section that disappears because
 *  the dispatcher did not fill a JSON field is a worse answer than showing the
 *  sentence that started the run. The detail page leaves it unset (its header
 *  already carries the intent); the chat card passes it. */
export function SpecCard({
  spec,
  fallback,
  bare,
}: {
  spec?: string
  fallback?: string
  /** Render the brief's body alone — no section frame, no fold. The chat card
   *  gives each section its own button and one shared panel, so a fold inside
   *  the panel would be a fold on a fold. */
  bare?: boolean
}) {
  const [open, setOpen] = useState(false)
  const shape = briefShape(spec, fallback)
  const { target, success, constraints, agents, scopeFiles, raw, more: hasMore } = shape
  if (!shape.has) return null

  // The brief's body, drawn the same way wherever it is shown: the panel in the
  // chat card gets exactly what the page's unfolded state shows.
  const body = (
    <>
      {target && <p class="dt-spec-target">{target}</p>}
      {constraints.length > 0 && (
        <ul class="dt-spec-list">
          {constraints.map((c, i) => (
            <li key={i}>
              {/* A dot, not a check: these are guardrails the run was
                  held to, not items it ticked off. */}
              <span class="dt-spec-dot" aria-hidden="true" />
              <span>{c}</span>
            </li>
          ))}
        </ul>
      )}
      {(scopeFiles.length > 0 || agents.length > 0 || success) && (
        <dl class="dt-spec-foot">
          {scopeFiles.length > 0 && (
            <>
              <dt>{t('detail.spec.scope')}</dt>
              <dd>
                <span class="dt-spec-files">
                  {scopeFiles.map((f, i) => (
                    <code key={i} class="result-file-chip" title={f}>
                      {f}
                    </code>
                  ))}
                </span>
              </dd>
            </>
          )}
          {agents.length > 0 && (
            <>
              <dt>{t('detail.spec.agents')}</dt>
              <dd>
                <span class="dt-spec-files">
                  {agents.map((a) => (
                    <AgentTag key={a} name={a} />
                  ))}
                </span>
              </dd>
            </>
          )}
          {success && (
            <>
              <dt>{t('detail.spec.success')}</dt>
              <dd>{success}</dd>
            </>
          )}
        </dl>
      )}
      {raw && (
        <div class="dt-spec-raw">
          <span class="dt-spec-raw-label">{t('detail.spec.raw')}</span>
          <p class="dt-spec-raw-text">{raw}</p>
        </div>
      )}
    </>
  )

  if (bare) return <div class="dt-bare">{body}</div>

  return (
    <section class="dt-sec">
      <div class="dt-sec-head">
        <h2 class="dt-sec-title">{t('detail.spec.title')}</h2>
        {constraints.length > 0 && (
          <span class="dt-sec-hint">
            {t('detail.spec.constraints', { n: String(constraints.length) })}
          </span>
        )}
        {hasMore && (
          <button type="button" class="dt-sec-action" onClick={() => setOpen((v) => !v)}>
            {open ? t('events.collapse') : t('detail.spec.expand')}
          </button>
        )}
      </div>

      {(!open || !hasMore) && target && <p class="dt-spec-preview">{target}</p>}

      {open && hasMore && body}
    </section>
  )
}

/** The sentence the user actually typed, lifted out of the dispatcher's intent
 *  envelope.
 *
 *  The envelope is a title line, then 目标 / 约束 / 成功标准 in the dispatcher's
 *  own words, then the user's message under a label line of its own. The brief
 *  above already carries the first part, so only that tail is new — and the
 *  label is localized, which is why it is recognised by its shape (a short line
 *  ending in a colon, opening the last paragraph) rather than by its words. A
 *  tail that merely repeats the target is dropped: printing the same sentence
 *  twice, ten pixels apart, is the page's oldest habit. */
function rawRequestOf(intent: string, target: string): string {
  const paras = intent
    .trim()
    .split(/\n\s*\n/)
    .map((p) => p.trim())
    .filter(Boolean)
  // One paragraph means there is no envelope to read the tail out of.
  if (paras.length < 2) return ''
  const last = paras[paras.length - 1] ?? ''
  const nl = last.indexOf('\n')
  if (nl < 0) return ''
  const head = last.slice(0, nl).trim()
  if (!head || head.length > 60 || !/[:：]$/.test(head)) return ''
  const body = last.slice(nl + 1).trim()
  if (!body || body === target.trim()) return ''
  return body
}

// —---------------------------------------------------------------------------
// Info — reference data, folded.
//
// These are the values you check once ("which attempt was this, on which
// node") and never read again. Open, they were the first band under the
// header: five columns of grey ids ahead of the result. Folded, the header
// line still carries the four that identify the run.
// —---------------------------------------------------------------------------
export function InfoSection({
  task,
  onOpenTask,
  bare,
}: {
  task: Task
  onOpenTask?(id: string): void
  /** The fact grid alone, without the fold header — see `SpecCard`. */
  bare?: boolean
}) {
  const [open, setOpen] = useState(false)
  const summary = [
    `#${shortId(task.id)}`,
    task.project || '',
    task.owner || '',
    fmtShort(task.created_at),
  ]
    .filter(Boolean)
    .join(' · ')
  // Where the run actually happened, not where the submitter pinned it: a
  // finished row is located on the machine that ran it.
  const workDir = task.exec_work_dir || task.work_dir || ''
  // The result envelope carries the run's bill. Same keys the verdict block
  // reads on the task page; the card has no verdict block, so this is the one
  // place they can appear.
  const r = task.result ? parseResult(task.result) : null
  const tokens = typeof r?.tokens === 'number' && r.tokens > 0 ? String(r.tokens) : ''
  const cost = typeof r?.cost === 'number' && r.cost > 0 ? '$' + r.cost.toFixed(4) : ''
  const queue = [queueWord(task.priority), task.seq ? `#${task.seq}` : ''].filter(Boolean).join(' · ')
  const plan = task.plan_meta?.plan_id
    ? [task.plan_meta.stage_id || '', task.plan_meta.plan_id].filter(Boolean).join(' / ')
    : ''
  const rounds =
    task.supervision?.round !== undefined
      ? `${task.supervision.round}/${task.supervision.budget ?? '?'}`
      : ''
  const elapsed = elapsedOf(task)
  const facts = (
    <dl class="dt-facts">
      <Fact label={t('detail.id')} value={task.id} mono title={task.id} />
      {task.parent_id && (
        <div class="dt-fact">
          <dt>{t('detail.parent')}</dt>
          <dd>
            <button
              type="button"
              class="linklike mono"
              onClick={() => onOpenTask?.(task.parent_id)}
              title={task.parent_id}
            >
              #{shortId(task.parent_id)}
            </button>
          </dd>
        </div>
      )}
      <Fact label={t('detail.project')} value={task.project || '—'} />
      <Fact label={t('detail.owner')} value={task.owner || '—'} />
      <Fact label={t('detail.attempt')} value={task.attempt_id || '—'} mono title={task.attempt_id} />
      <Fact label={t('detail.created')} value={fmtFull(task.created_at)} />
      <Fact label={t('detail.updated')} value={fmtFull(task.updated_at)} />
      {task.risk && <Fact label={t('detail.risk')} value={task.risk} />}
      {/* Everything below is the card's alone. The task page answers these
          questions in its own header, verdict block and trace strip; the
          card's panel is the only surface it has, so it carries them all. */}
      {bare && task.session_id && (
        <Fact label={t('detail.session')} value={task.session_id} mono title={task.session_id} />
      )}
      {bare && workDir && <Fact label={t('detail.workdir')} value={workDir} mono title={workDir} />}
      {bare && queue && <Fact label={t('detail.queue')} value={queue} />}
      {bare && plan && <Fact label={t('detail.plan')} value={plan} mono title={plan} />}
      {bare && rounds && <Fact label={t('detail.verdict.rounds')} value={rounds} />}
      {bare && elapsed && <Fact label={t('detail.verdict.elapsed')} value={elapsed} />}
      {bare && tokens && <Fact label={t('detail.verdict.tokens')} value={tokens} />}
      {bare && cost && <Fact label={t('detail.verdict.cost')} value={cost} />}
      {bare && (task.events?.length ?? 0) > 0 && (
        <Fact label={t('detail.events')} value={String(task.events!.length)} />
      )}
      {bare && (task.children?.length ?? 0) > 0 && (
        <Fact label={t('detail.subtasks')} value={String(task.children!.length)} />
      )}
      {bare && task.scope_paths && task.scope_paths.length > 0 && (
        <Fact
          label={t('detail.scoped')}
          value={task.scope_paths.join(', ')}
          mono
          title={task.scope_paths.join('\n')}
        />
      )}
      {bare && task.isolate && <Fact label={t('detail.isolated')} value={t('detail.isolateHint')} />}
    </dl>
  )
  if (bare) return <div class="dt-bare">{facts}</div>
  return (
    <section class="dt-sec">
      <button
        type="button"
        class={'dt-fold' + (open ? ' is-open' : '')}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span class="dt-sec-title">{t('detail.info')}</span>
        <span class="dt-fold-summary">{summary}</span>
        <Icon name={open ? 'chevron-down' : 'chevron-right'} size={13} class="dt-fold-ico" />
      </button>
      {open && facts}
    </section>
  )
}

/** Priority as the queue words it, or '' when the row carries none. */
function queueWord(priority?: string): string {
  return priority ? t(`queue.priority.${priority}`) : ''
}

/** Wall-clock the run held the queue. Only for settled tasks: an open one
 *  would read as a duration and then keep growing while the card sits. */
function elapsedOf(task: Task): string {
  if (['submitted', 'queued', 'dispatched', 'running', 'waiting_context'].includes(task.state)) {
    return ''
  }
  const ms = Date.parse(task.updated_at) - Date.parse(task.created_at)
  if (!Number.isFinite(ms) || ms <= 0) return ''
  return formatDuration(ms)
}

function Fact({
  label,
  value,
  mono,
  title,
}: {
  label: string
  value: string
  mono?: boolean
  title?: string
}) {
  return (
    <div class="dt-fact">
      <dt>{label}</dt>
      <dd class={mono ? 'mono' : ''} title={title ?? value}>
        {value}
      </dd>
    </div>
  )
}

/** The task's event log. A run that failed after forty tool calls has
 *  thousands of pixels of transcript, and the page used to open on all of it:
 *  the result, the brief and the facts sat above a wall you had to scroll
 *  past to reach anything else. Long logs therefore open on their tail — the
 *  part that says how the run ended — with the count and a way to see it all.
 *
 *  The fold trips at the tail size itself. It used to need three times the
 *  tail before it folded at all, so an eighteen-event run — the common case —
 *  printed every row: 963px, 55% of the page, for a log nobody came here to
 *  read top-down. */
export function TimelineSection({
  events,
  suppressResult,
  tail = TIMELINE_TAIL,
  bare,
}: {
  events: TaskEvent[]
  suppressResult?: boolean
  /** How many rows the folded state shows. The chat card, at 360px, has less
   *  room than the page and asks for fewer. */
  tail?: number
  /** The log alone, unfolded and unlabelled — see `SpecCard`. */
  bare?: boolean
}) {
  const [open, setOpen] = useState(false)
  const long = events.length > tail
  const shown = long && !open ? events.slice(-tail) : events
  if (bare) {
    return (
      <div class="dt-bare">
        {events.length > 0 ? (
          <EventTimeline events={events} suppressResultBody={suppressResult} />
        ) : (
          <p class="dim">{t('common.empty')}</p>
        )}
      </div>
    )
  }
  return (
    <section class="dt-sec">
      <div class="dt-sec-head">
        <h2 class="dt-sec-title">{t('detail.timeline')}</h2>
        {long && (
          <>
            <span class="dt-sec-hint">
              {open
                ? t('detail.timeline.count', { n: String(events.length) })
                : t('detail.timeline.tail', { n: String(tail) })}
            </span>
            <button type="button" class="dt-sec-action" onClick={() => setOpen((v) => !v)}>
              {open ? t('events.collapse') : t('detail.timeline.expand')}
            </button>
          </>
        )}
      </div>
      {events.length > 0 ? (
        <EventTimeline events={shown} suppressResultBody={suppressResult} />
      ) : (
        <p class="dim">{t('common.empty')}</p>
      )}
    </section>
  )
}
