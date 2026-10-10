// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from 'preact/hooks'
import type { TaskEvent } from '../api/client'
import { Markdown } from '../md/render'
import { blockDepth, formatTaskEvent, toolArgSummary, type AgentBlock } from './event-parser'
import { Icon } from './icons'
import { t } from '../i18n'

export interface EventTimelineProps {
  events: TaskEvent[]
  className?: string
  defaultOpenThought?: boolean
}

export function EventTimeline({
  events,
  className = '',
  defaultOpenThought = false,
}: EventTimelineProps) {
  if (!events || events.length === 0) {
    return <p class="dim chain-empty">{t('sessions.chainEmpty')}</p>
  }

  const depths = new Map<string, number>()
  return (
    <div class={`event-timeline-container ${className}`}>
      <ol class="timeline-steps">
        {events.map((ev, i) => {
          const formatted = formatTaskEvent(ev.type, ev.data)
          const block = formatted.agent
          const depth = block ? blockDepth(block, depths) : 0
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
                  <code class="step-raw-type dim">{ev.type}</code>
                </div>

                {formatted.thought ? (
                  <ThoughtCard
                    text={formatted.thought}
                    defaultOpen={defaultOpenThought || i === events.length - 1}
                  />
                ) : block ? (
                  <AgentBlockBody block={block} rawJson={formatted.rawJson} />
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
function AgentBlockBody({ block, rawJson }: { block: AgentBlock; rawJson?: string }) {
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
        </div>
      )
    }
    case 'tool_result': {
      const content = block.content ?? ''
      const clipped = content.length > 600 ? content.slice(0, 600) + '…' : content
      return (
        <div class="step-body">
          {clipped && (
            <pre class={`agent-result ${block.isError ? 'is-error' : ''}`}>{clipped}</pre>
          )}
          {content.length > 600 && <RawJsonToggle raw={content} />}
        </div>
      )
    }
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
