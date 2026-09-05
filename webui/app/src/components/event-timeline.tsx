import { useState } from 'preact/hooks'
import type { TaskEvent } from '../api/client'
import { Markdown } from '../md/render'
import { formatTaskEvent } from './event-parser'
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

  return (
    <div class={`event-timeline-container ${className}`}>
      <ol class="timeline-steps">
        {events.map((ev, i) => {
          const formatted = formatTaskEvent(ev.type, ev.data)
          return (
            <li key={i} class={`timeline-step-item type-${ev.type}`}>
              <div class="step-marker" aria-hidden="true" />
              <div class="step-content">
                <div class="step-header">
                  <span class="step-time dim">
                    {new Date(ev.ts * 1000).toLocaleTimeString()}
                  </span>
                  <span class={`badge badge-${formatted.badgeClass}`}>
                    {formatted.label}
                  </span>
                  <code class="step-raw-type dim">{ev.type}</code>
                </div>

                {formatted.thought ? (
                  <ThoughtCard
                    text={formatted.thought}
                    defaultOpen={defaultOpenThought || i === events.length - 1}
                  />
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
      <div class="thought-card-header" onClick={() => setOpen(!open)}>
        <span class="thought-card-icon" aria-hidden="true">🧠</span>
        <span class="thought-card-title">{t('sessions.thoughtTitle')}</span>
        <span class="dim thought-preview">{!open && preview}</span>
        <span class="grow" />
        <button
          type="button"
          class="btn-icon thought-copy-btn"
          onClick={copy}
          title={t('common.copy')}
        >
          {copied ? '✓' : '📋'}
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
        {open ? '▼ 隐藏原始数据' : '▶ 原始 JSON'}
      </button>
      {open && (
        <pre class="raw-json-block">
          <code>{raw}</code>
        </pre>
      )}
    </div>
  )
}
