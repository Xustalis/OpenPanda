// SPDX-License-Identifier: AGPL-3.0-or-later

import { useMemo, useState } from 'preact/hooks'
import type { TaskEvent } from '../api/client'
import { Markdown } from '../md/render'
import { blockDepth, formatTaskEvent, imageRefOf, toolArgSummary, type AgentBlock, type ImageRef } from './event-parser'
import { buildRows } from './event-rows'
import { Icon } from './icons'
import { ImagePreview } from './image-preview'
import { t } from '../i18n'

export interface EventTimelineProps {
  events: TaskEvent[]
  className?: string
  defaultOpenThought?: boolean
  /** Drop the body of the *last* result row. The task detail page renders the
   *  same output in its own 执行结果 section directly above the log, and
   *  printing it twice — once as a section, once as the log's final row — was
   *  half a screen of identical text. The row itself stays (with a pointer to
   *  the section), so the log still says when the result landed. Only the last
   *  one is suppressed: in a supervised run every earlier round has its own
   *  output, and the log is the only place that survives. */
  suppressResultBody?: boolean
}

export function EventTimeline({
  events,
  className = '',
  defaultOpenThought = false,
  suppressResultBody = false,
}: EventTimelineProps) {
  // Hooks must run before any early return: an empty log takes a different
  // branch, and Preact still expects the same hook count on every render.
  // useTaskEvents keeps the events array reference stable between frames
  // (a poll with no new events does not call setEvents), so this memo is
  // skipped on every re-render except the one that appends events.
  const rows = useMemo(() => buildRows(events ?? []), [events])
  if (rows.length === 0) {
    return <p class="dim chain-empty">{t('sessions.chainEmpty')}</p>
  }

  const depths = new Map<string, number>()
  // Pre-format once: lifecycle plumbing events are grouped into a single
  // pipeline row, so the render loop works on groups, not raw events.
  // Which row's result body the caller already renders elsewhere. Found by
  // scanning back for the newest row that carries a result — the page's own
  // section mirrors that one, not necessarily the final row of the log (a
  // "session linked" event usually closes it).
  let mutedResult = -1
  if (suppressResultBody) {
    for (let i = rows.length - 1; i >= 0; i--) {
      const row = rows[i]
      if (row && row.kind === 'event' && row.formatted.result) {
        mutedResult = i
        break
      }
    }
  }
  // A tool call and its result routinely name the same picture ("Read x.png"
  // → "File created successfully at: x.png"), and a re-read names it again.
  // One preview per picture per log: the first row to claim a path owns it.
  const shownImages = new Set<string>()
  return (
    <div class={`event-timeline-container ${className}`}>
      <ol class="timeline-steps">
        {rows.map((row, i) => {
          if (row.kind === 'pipeline') {
            return (
              <li key={i} class="timeline-step-item type-pipeline">
                <div class="step-marker" aria-hidden="true" />
                <div class="step-content">
                  <div class="step-header">
                    <span class="step-time dim">
                      {new Date(row.ts * 1000).toLocaleTimeString()}
                    </span>
                    <div class="pipeline" role="list">
                      {row.steps.map((s, j) => (
                        <span key={j} class={`pipeline-step${s.failed ? ' failed' : ''}`} role="listitem">
                          {j > 0 && <span class="pipeline-arrow" aria-hidden="true">→</span>}
                          <span class="pipeline-chip">
                            {s.step}
                            {s.node && <span class="pipeline-node dim"> {s.node}</span>}
                          </span>
                        </span>
                      ))}
                    </div>
                  </div>
                </div>
              </li>
            )
          }
          const ev = row.event
          const formatted = row.formatted
          const block = formatted.agent
          const depth = block ? blockDepth(block, depths) : 0
          // The picture this row is about, if it has not been shown already.
          let image: ImageRef | null = null
          if (block) {
            const ref = imageRefOf(block)
            const key = ref?.path ?? ref?.dataUrl
            if (ref && key && !shownImages.has(key)) {
              shownImages.add(key)
              image = ref
            }
          }
          if (formatted.quiet) {
            return (
              <li key={i} class="timeline-step-item type-quiet">
                <div class="step-marker" aria-hidden="true" />
                <div class="step-content step-quiet dim">
                  <span class="step-time">{new Date(ev.ts * 1000).toLocaleTimeString()}</span>
                  <span>{formatted.label}</span>
                  {(row.count ?? 0) > 1 && (
                    <span class="quiet-count" title={t('events.quietRepeatHint')}>
                      ×{row.count}
                    </span>
                  )}
                </div>
              </li>
            )
          }
          return (
            <li
              key={i}
              class={`timeline-step-item type-${ev.type}${block ? ` ev-${block.ev}` : ''}${block?.isError ? ' is-error' : ''}${depth > 0 ? ' agent-child' : ''}`}
              style={depth > 0 ? { marginLeft: `${depth * 20}px` } : undefined}
            >
              <div class="step-marker" aria-hidden="true" />
              <div class="step-content">
                <div class="step-header">
                  <span class="step-time dim">
                    {new Date(ev.ts * 1000).toLocaleTimeString()}
                  </span>
                  <span class={`badge badge-${formatted.badgeClass}`}>
                    {formatted.label}
                  </span>
                  {block?.parent && (
                    <span class="badge badge-dim agent-sub-badge" title={t('events.subagentHint')}>
                      {t('events.subagent')}
                    </span>
                  )}
                  {/* The raw type only surfaces for events the formatter does
                      not know — for mapped types the human label already says
                      what happened and the token reads as debug noise. */}
                  {formatted.label === ev.type && (
                    <code class="step-raw-type dim">{ev.type}</code>
                  )}
                  {(row.count ?? 0) > 1 && (
                    <span class="quiet-count" title={t('events.quietRepeatHint')}>
                      ×{row.count}
                    </span>
                  )}
                </div>

                {formatted.thought ? (
                  <ThoughtCard
                    text={formatted.thought}
                    defaultOpen={defaultOpenThought || i === rows.length - 1}
                  />
                ) : block ? (
                  <AgentBlockBody block={block} rawJson={formatted.rawJson} image={image} />
                ) : formatted.result ? (
                  i === mutedResult ? (
                    <div class="step-body">
                      <span class="step-muted-result">{t('detail.result.above')}</span>
                    </div>
                  ) : (
                    <ResultCard
                      result={formatted.result}
                      rawJson={formatted.rawJson}
                      fallback={formatted.summary}
                    />
                  )
                ) : (
                  <div class="step-body">
                    {formatted.summary && (
                      <div class="step-summary">{formatted.summary}</div>
                    )}
                    {formatted.tags && formatted.tags.length > 0 && (
                      <div class="step-tags">
                        {formatted.tags.map((tag, idx) => (
                          <span key={idx} class="step-tag">
                            <span class="tag-key dim">{tag.key}:</span>
                            <span class="tag-val">{tag.value}</span>
                          </span>
                        ))}
                      </div>
                    )}
                    {formatted.rawJson && (
                      <RawJsonToggle raw={formatted.rawJson} />
                    )}
                  </div>
                )}
              </div>
            </li>
          )
        })}
      </ol>
    </div>
  )
}

/** AgentBlockBody renders one typed activity block as a transcript row —
 * the everything-is-a-node view: text reads as prose, thinking collapses
 * into a card, tool calls show name + primary argument with the full
 * input behind a toggle, tool results show a bounded preview. */
function AgentBlockBody({
  block,
  rawJson,
  image,
}: {
  block: AgentBlock
  rawJson?: string
  /** Picture this row owns — see the dedupe in EventTimeline. */
  image?: ImageRef | null
}) {
  switch (block.ev) {
    case 'text':
      return (
        <div class="step-body agent-text">
          <Markdown text={block.text ?? ''} />
        </div>
      )
    case 'thinking':
      return <ThoughtCard text={block.thinking ?? ''} defaultOpen={false} />
    case 'tool_use': {
      const arg = toolArgSummary(block.input)
      const inputJson = block.input === undefined || block.input === null
        ? undefined
        : typeof block.input === 'string'
          ? block.input
          : JSON.stringify(block.input, null, 2)
      return (
        <div class="step-body">
          <div class="agent-tool-call">
            <code class="agent-tool-name">{block.name || 'tool'}</code>
            {arg && <code class="agent-tool-arg dim">{arg.length > 120 ? arg.slice(0, 120) + '…' : arg}</code>}
            {block.name === 'Task' && (
              <span class="badge badge-accent agent-sub-badge">{t('events.subagent')}</span>
            )}
          </div>
          {inputJson && <RawJsonToggle raw={inputJson} />}
          {image && <ImagePreview path={image.path} dataUrl={image.dataUrl} />}
        </div>
      )
    }
    case 'tool_result':
      return <ToolResultBody block={block} rawJson={rawJson} image={image} />
    case 'transcript_truncated':
      return (
        <div class="step-body">
          <div class="step-summary dim">{t('events.transcriptTruncatedNote')}</div>
        </div>
      )
    default:
      return rawJson ? (
        <div class="step-body">
          <RawJsonToggle raw={rawJson} />
        </div>
      ) : null
  }
}

/** A tool result is the agent's own output, and a shell or test run can be a
 *  screenful per call. Ten of those turned one task's log into 8,000px of
 *  transcript, so the row shows a few lines and keeps the rest one click
 *  away — same bargain the thought card and the result card already make.
 *  The fold counts lines, not characters: shell output is mostly short lines,
 *  and 300 characters of `git status` is still twenty rows of text. */
function ToolResultBody({
  block,
  rawJson,
  image,
}: {
  block: AgentBlock
  rawJson?: string
  image?: ImageRef | null
}) {
  const [open, setOpen] = useState(false)
  const content = block.content ?? ''
  const lines = content.split('\n')
  const long = lines.length > PREVIEW_LINES || content.length > 320
  const preview = lines.slice(0, PREVIEW_LINES).join('\n')
  const shown = open || !long ? content : preview + '…'
  // Bytes the adapter inlined ARE the result. Printing them would be a wall
  // of base64, so the picture replaces the text entirely.
  if (image?.dataUrl) {
    return (
      <div class="step-body">
        <ImagePreview dataUrl={image.dataUrl} />
      </div>
    )
  }
  return (
    <div class="step-body">
      {image?.path && <ImagePreview path={image.path} />}
      {shown && <pre class={`agent-result ${block.isError ? 'is-error' : ''}`}>{shown}</pre>}
      {long && (
        <button type="button" class="btn small result-expand" onClick={() => setOpen((v) => !v)}>
          {open ? t('events.collapse') : t('events.expand')}
        </button>
      )}
      {open && content.length > 600 && <RawJsonToggle raw={rawJson ?? content} />}
    </div>
  )
}

/** Lines of tool output a collapsed row shows. */
const PREVIEW_LINES = 4

function ThoughtCard({
  text,
  defaultOpen = false,
}: {
  text: string
  defaultOpen?: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  const [copied, setCopied] = useState(false)

  const lines = text.trim().split('\n')
  const preview = lines[0] ? (lines[0].length > 80 ? lines[0].slice(0, 80) + '…' : lines[0]) : ''

  function copy(e: Event) {
    e.stopPropagation()
    navigator.clipboard?.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div class={`thought-card ${open ? 'is-open' : 'is-collapsed'}`}>
      <div
        class="thought-card-header"
        onClick={() => setOpen(!open)}
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setOpen(!open)
          }
        }}
      >
        <Icon name="lightbulb" class="thought-card-icon" />
        <span class="thought-card-title">{t('sessions.thoughtTitle')}</span>
        <span class="dim thought-preview">{!open && preview}</span>
        <span class="grow" />
        <button
          type="button"
          class="btn-icon thought-copy-btn"
          onClick={copy}
          title={t('common.copy')}
        >
          {copied ? <Icon name="check" size={13} /> : <Icon name="copy" size={13} />}
        </button>
        <span class="thought-card-toggle">{open ? '▲' : '▼'}</span>
      </div>

      {open && (
        <div class="thought-card-body">
          <Markdown text={text} />
        </div>
      )}
    </div>
  )
}

function RawJsonToggle({ raw }: { raw: string }) {
  const [open, setOpen] = useState(false)

  return (
    <div class="raw-json-container">
      <button
        type="button"
        class="raw-json-btn"
        onClick={() => setOpen(!open)}
      >
        <Icon name={open ? 'chevron-down' : 'chevron-right'} size={13} /> {open ? t('events.hideRaw') : t('events.rawJson')}
      </button>
      {open && (
        <pre class="raw-json-block">
          <code>{raw}</code>
        </pre>
      )}
    </div>
  )
}

/** The terminal result event as a readable card: status line, then the
 *  result text as markdown. Long outputs collapse behind the raw toggle —
 *  the full JSON blob is never the default view. */
function ResultCard({
  result,
  rawJson,
  fallback,
}: {
  result: NonNullable<ReturnType<typeof formatTaskEvent>['result']>
  rawJson?: string
  /** What the row can still say when the payload carried no output at all —
   *  a failed run's reason. Without it the card was a badge over a bare
   *  raw-JSON toggle. */
  fallback?: string
}) {
  const [open, setOpen] = useState(false)
  const out = result.stdout ?? ''
  const long = out.length > 480
  const shown = open || !long ? out : out.slice(0, 480) + '…'
  return (
    <div class="step-body result-card">
      {result.stdout && (
        <div class="result-card-out">
          <Markdown text={shown} />
        </div>
      )}
      {result.stderr && <pre class="agent-result is-error">{result.stderr}</pre>}
      {!result.stdout && !result.stderr && fallback && (
        <div class="step-summary">{fallback}</div>
      )}
      {long && (
        <button type="button" class="btn small" onClick={() => setOpen((v) => !v)}>
          {open ? t('events.collapse') : t('events.expand')}
        </button>
      )}
      {rawJson && <RawJsonToggle raw={rawJson} />}
    </div>
  )
}
