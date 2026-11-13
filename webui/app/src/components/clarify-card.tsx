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

import { useEffect, useRef, useState } from 'preact/hooks'
import type { ClarifyAnswer, ClarifySpec } from '../api/client'
import { t } from '../i18n'
import { Markdown } from '../md/render'
import { Icon } from './icons'

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
  const cardRef = useRef<HTMLDivElement>(null)
  const planRef = useRef<HTMLDivElement>(null)
  const [planOpen, setPlanOpen] = useState(false)
  const [planClipped, setPlanClipped] = useState(false)
  // The running turn is parked on this answer, so the keyboard belongs here
  // rather than in the composer behind the card. Re-focus on every question:
  // answering one rebuilds the option buttons, and the focused button going
  // away would hand the keyboard back to the page mid-round.
  useEffect(() => {
    cardRef.current?.focus({ preventScroll: true })
  }, [index])
  // A plan longer than the collapsed three lines gets a toggle. The question is
  // what the card exists for; a task description can otherwise push the
  // options off a laptop screen.
  useEffect(() => {
    const el = planRef.current
    if (!el) return
    const line = parseFloat(getComputedStyle(el).lineHeight) || 20
    // Half a line of slack: a paragraph that merely rounds past three lines
    // should render whole, not hide its last words behind a toggle.
    setPlanClipped(el.scrollHeight > line * 3.4 + 2)
  }, [spec.plan])
  const q = spec.questions[index]
  if (!q) return null
  const preview = q.multi_select ? '' : (q.options[focused]?.preview ?? '').trim()
  const recMarker = t('clarify.recommended')
  // The marker ships with its own brackets ("（推荐）" / "(Recommended)") for
  // the CLI's plain-text card; the web card draws it as a tag, where the
  // brackets only add width.
  const recTag = recMarker.replace(/[()（）\s]/g, '') || recMarker
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
  // Back reopens the previous question with the answer it already carried:
  // people revise a choice far more often than they abort the whole round.
  const back = () => {
    if (index === 0) return
    const prev = index - 1
    const prevAnswer = answers.current[prev]
    answers.current = answers.current.slice(0, prev)
    setIndex(prev)
    setFocused(0)
    const restored: Record<string, boolean> = {}
    for (const label of prevAnswer?.selected ?? []) restored[label] = true
    setPicked(restored)
    const text = prevAnswer?.other ?? ''
    setOther(text)
    setOtherOpen(text !== '')
  }
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
  // Number keys pick an option, Escape backs out — the same two gestures the
  // CLI's choice card answers to. Typing inside "Other" keeps its own keys.
  const onKeyDown = (e: KeyboardEvent) => {
    const tag = (e.target as HTMLElement | null)?.tagName
    if (tag === 'INPUT' || tag === 'TEXTAREA') {
      // Inside "Other" Escape closes the field first; the round itself stays
      // open, so one keypress cannot throw away the answer being typed.
      if (e.key === 'Escape' && otherOpen) {
        e.preventDefault()
        setOtherOpen(false)
      }
      return
    }
    if (e.key === 'Escape') {
      if (onCancel) {
        e.preventDefault()
        onCancel()
      }
      return
    }
    const n = Number(e.key)
    const pick = q.options[n - 1]
    if (!Number.isInteger(n) || n < 1 || !pick) return
    e.preventDefault()
    choose(pick.label)
  }

  return (
    <div
      class="clarify-card"
      data-testid="session-clarify"
      role="dialog"
      aria-label={q.question}
      ref={cardRef}
      tabIndex={-1}
      onKeyDown={onKeyDown}
    >
      <div class="clarify-head">
        <span class="badge">{t('clarify.title')}</span>
        <span class="clarify-steps" aria-hidden="true">
          {spec.questions.map((_, i) => (
            <span class={`clarify-step${i === index ? ' is-on' : i < index ? ' is-done' : ''}`} />
          ))}
        </span>
        <span class="clarify-progress">
          {t('clarify.progress', { i: String(index + 1), n: String(spec.questions.length) })}
        </span>
      </div>
      {spec.plan && (
        <>
          <div
            ref={planRef}
            class={`clarify-plan${planOpen ? ' is-open' : ''}${planClipped ? ' is-clipped' : ''}`}
          >
            {spec.plan}
          </div>
          {planClipped && (
            <button
              type="button"
              class="clarify-plan-toggle"
              aria-expanded={planOpen}
              onClick={() => setPlanOpen((v) => !v)}
            >
              {t(planOpen ? 'clarify.collapse' : 'clarify.expand')}
            </button>
          )}
        </>
      )}
      <div class="clarify-question">
        {q.header && <span class="clarify-header">{q.header}</span>}
        <span class="clarify-qtext">{q.question}</span>
      </div>
      <div class="clarify-body">
        <div class="clarify-options">
          {q.options.map((o, i) => {
            const on = Boolean(picked[o.label])
            const recommended = o.label === q.recommended
            return (
              <button
                type="button"
                class={`clarify-option${on ? ' is-picked' : ''}${recommended ? ' is-recommended' : ''}`}
                aria-pressed={q.multi_select ? on : undefined}
                onMouseEnter={() => setFocused(i)}
                onFocus={() => setFocused(i)}
                onClick={() => choose(o.label)}
              >
                <span class="clarify-option-key" aria-hidden="true">{i + 1}</span>
                <span class="clarify-option-main">
                  <span class="clarify-option-label">
                    {o.label}
                    {recommended && !labelHasMarker(o.label, recMarker) && (
                      <span class="clarify-recommended">{recTag}</span>
                    )}
                  </span>
                  {o.description && <span class="clarify-option-desc">{o.description}</span>}
                </span>
                {q.multi_select && (
                  <span class={`clarify-option-tick${on ? ' is-on' : ''}`} aria-hidden="true">
                    {on && <Icon name="check" size={11} />}
                  </span>
                )}
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
        {index > 0 && (
          <button type="button" class="btn small ghost" onClick={back}>
            {t('clarify.back')}
          </button>
        )}
        {q.multi_select && (
          <button
            type="button"
            class="btn primary small"
            disabled={selectedLabels().length === 0}
            onClick={() => advance({ id: q.id, selected: selectedLabels() })}
          >
            {t('clarify.confirm')}
          </button>
        )}
        <button
          type="button"
          class="btn small ghost"
          aria-expanded={otherOpen}
          onClick={() => setOtherOpen((v) => !v)}
        >
          {t('clarify.other')}
        </button>
        <button type="button" class="btn small ghost" onClick={() => advance({ id: q.id })}>
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
