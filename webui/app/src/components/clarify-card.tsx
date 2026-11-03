// SPDX-License-Identifier: AGPL-3.0-or-later

// The pre-execution question card. The entry model asked (or the runtime's
// plan-confirm gate fired) before it will create a task; the running SSE turn
// is parked on the answer. Questions are answered one at a time so four of
// them stay readable in a narrow chat column. Option labels, the recommended
// marker, multi-select, free text ("Other") and skip mirror the CLI's choice
// card and Claude Code's AskUserQuestion.
//
// Shared by the thread and the side conversation: once a side ask can dispatch
// work, it hits the same plan-confirm gate, and a card that only the thread
// rendered would leave the side turn parked on an invisible question.

import { useRef, useState } from 'preact/hooks'
import type { ClarifyAnswer, ClarifySpec } from '../api/client'
import { t } from '../i18n'
import { Markdown } from '../md/render'

function labelHasMarker(label: string, marker: string): boolean {
  const bare = marker.replace(/[()（）\s]/g, '')
  return label.includes(marker) || (bare !== '' && label.includes(bare))
}

export function ClarifyCard({
  spec,
  onSubmit,
  onCancel,
}: {
  spec: ClarifySpec
  onSubmit(answers: ClarifyAnswer[]): void
  /** Omitted for a parked task's card: there is no turn to abort, and the
   *  task's own Reject button is the way to throw the work away. */
  onCancel?: () => void
}) {
  const [index, setIndex] = useState(0)
  const [picked, setPicked] = useState<Record<string, boolean>>({})
  const [other, setOther] = useState('')
  const [otherOpen, setOtherOpen] = useState(false)
  // Which option's preview is on screen. The recommended option is first, so
  // 0 shows a preview before the user touches anything.
  const [focused, setFocused] = useState(0)
  const answers = useRef<ClarifyAnswer[]>([])
  const q = spec.questions[index]
  if (!q) return null
  const preview = q.multi_select ? '' : (q.options[focused]?.preview ?? '').trim()
  const recMarker = t('clarify.recommended')
  // A spec that already offers a Cancel choice (the runtime's plan-confirm
  // card does) must not render a second Cancel in the action row: the option
  // is the answer, the action would be a different way to abort the turn.
  const cancelMarker = t('clarify.cancel')
  const hasCancelOption = spec.questions.some((question) =>
    question.options.some((o) => o.label.trim() === cancelMarker),
  )

  const advance = (answer: ClarifyAnswer) => {
    const next = [...answers.current, answer]
    answers.current = next
    if (index + 1 < spec.questions.length) {
      setIndex(index + 1)
      setPicked({})
      setOther('')
      setOtherOpen(false)
      return
    }
    onSubmit(next)
  }
  const selectedLabels = () => q.options.filter((o) => picked[o.label]).map((o) => o.label)
  const choose = (label: string) => {
    if (q.multi_select) {
      setPicked((p) => ({ ...p, [label]: !p[label] }))
      return
    }
    advance({ id: q.id, selected: [label] })
  }
  const submitOther = (e: Event) => {
    e.preventDefault()
    const text = other.trim()
    if (!text) return
    advance({
      id: q.id,
      selected: q.multi_select ? selectedLabels() : undefined,
      other: text,
    })
  }

  return (
    <div class="clarify-card" data-testid="session-clarify" role="dialog" aria-label={q.question}>
      <div class="clarify-head">
        <span class="badge">{t('clarify.title')}</span>
        <span class="clarify-progress">
          {t('clarify.progress', { i: String(index + 1), n: String(spec.questions.length) })}
        </span>
      </div>
      {spec.plan && <div class="clarify-plan">{spec.plan}</div>}
      <div class="clarify-question">
        <span class="clarify-header">{q.header}</span> {q.question}
      </div>
      <div class="clarify-body">
        <div class="clarify-options">
          {q.options.map((o, i) => {
            const on = Boolean(picked[o.label])
            return (
              <button
                type="button"
                class={`clarify-option${on ? ' is-picked' : ''}`}
                aria-pressed={q.multi_select ? on : undefined}
                onMouseEnter={() => setFocused(i)}
                onFocus={() => setFocused(i)}
                onClick={() => choose(o.label)}
              >
                <span class="clarify-option-label">
                  {q.multi_select && <span class="clarify-check">{on ? '✓' : ''}</span>}
                  {o.label}
                  {o.label === q.recommended && !labelHasMarker(o.label, recMarker) && (
                    <span class="clarify-recommended">{recMarker}</span>
                  )}
                </span>
                {o.description && <span class="clarify-option-desc">{o.description}</span>}
              </button>
            )
          })}
        </div>
        {preview && (
          <div class="clarify-preview">
            <Markdown text={preview} />
          </div>
        )}
      </div>
      {otherOpen && (
        <form class="clarify-other" onSubmit={submitOther}>
          <input
            value={other}
            onInput={(e) => setOther(e.currentTarget.value)}
            placeholder={t('clarify.otherPlaceholder')}
            autofocus
          />
          <button type="submit" class="btn primary small">
            {t('clarify.submit')}
          </button>
        </form>
      )}
      <div class="clarify-actions">
        {q.multi_select && (
          <button type="button" class="btn primary small" onClick={() => advance({ id: q.id, selected: selectedLabels() })}>
            {t('clarify.confirm')}
          </button>
        )}
        <button type="button" class="btn small" onClick={() => setOtherOpen((v) => !v)}>
          {t('clarify.other')}
        </button>
        <button type="button" class="btn small" onClick={() => advance({ id: q.id })}>
          {t('clarify.skip')}
        </button>
        {onCancel && !hasCancelOption && (
          <button type="button" class="btn small danger" onClick={onCancel}>
            {t('clarify.cancel')}
          </button>
        )}
      </div>
    </div>
  )
}
