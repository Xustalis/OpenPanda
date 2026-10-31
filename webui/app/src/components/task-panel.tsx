// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useLayoutEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { Icon } from './icons'

// —---------------------------------------------------------------------------
// TaskPanel — one card of the task page's four-card body.
//
// The page used to be a single reading column: the result, then the brief,
// then the log, each as tall as its content. A run that produced 2,000
// characters of markdown pushed everything after it below the fold, so the
// four things a reader opens a task for — what came out, how it ran, what it
// was asked to do, what it is — could never be seen together.
//
// A panel is that unit: a title, the facts that fit on the head line, a lead
// that is always visible (the trace strip, the result's chips), and a body
// that is either clipped to a preview or folded away. The fold is local to the
// card, so opening the output never moves the other three, and the expanded
// body scrolls inside itself rather than growing the page.
// ---------------------------------------------------------------------------

export interface TaskPanelProps {
  title: string
  /** Muted text on the head line — the count, the summary, the stamp. */
  hint?: ComponentChildren
  /** Head-line controls that stay reachable in both states (copy, raw JSON). */
  actions?: ComponentChildren
  /** Span the full grid width. */
  wide?: boolean
  /** Shown in both states: the one line that is worth the space either way. */
  lead?: ComponentChildren
  /** The expandable body. */
  children?: ComponentChildren
  /** Collapsed preview height in px. When set, the body is shown clipped
   *  behind a fade instead of hidden — a preview, not a lid. Whether the fold
   *  is actually offered is then measured, not guessed. */
  clamp?: number
  /** Cap the expanded body at this height; it then scrolls internally. */
  openMax?: number
  /** Whether the body may fold at all. With `clamp`, the toggle only appears
   *  when the content really overflows it — a preview that fits needs no
   *  "expand" that changes nothing, and a cut preview needs one. */
  foldable?: boolean
  /** Shown in place of the body when the panel has nothing to report. */
  empty?: ComponentChildren
}

export function TaskPanel(props: TaskPanelProps) {
  const [open, setOpen] = useState(false)
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const [overflows, setOverflows] = useState(false)
  const clamped = props.clamp !== undefined
  // A body built from conditional children arrives as an array of `false`s,
  // which is truthy and not "no children": a panel with four closed branches
  // would draw an empty box and a fold that opens nothing.
  const kids = Array.isArray(props.children) ? props.children.flat(Infinity) : [props.children]
  const hasBody = kids.some((k) => k !== undefined && k !== null && k !== false && k !== '')

  // A character count cannot stand in for a height: the same 300 characters
  // are four lines in a half-width card and two in a full-width one. Measure
  // the real content against the clamp instead, in a layout effect so the
  // clipped state is what the browser paints first.
  useLayoutEffect(() => {
    if (!clamped) return
    const el = bodyRef.current
    if (!el) return
    const measure = () => setOverflows(el.scrollHeight > (props.clamp as number) + 1)
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [clamped, props.clamp, props.children])

  const foldable = Boolean(props.foldable) && hasBody && (!clamped || overflows || open)
  const expanded = open && foldable
  // A clamp without a fold would be content cut off with no way to reach it:
  // the preview only exists as the collapsed half of a fold.
  const clipped = foldable && !expanded && props.clamp !== undefined
  const showBody = hasBody && (!foldable || expanded || clipped)
  const style = [
    clipped ? `--dp-clamp:${props.clamp}px` : '',
    expanded && props.openMax ? `--dp-max:${props.openMax}px` : '',
  ]
    .filter(Boolean)
    .join(';')

  return (
    <section class={'dp' + (props.wide ? ' is-wide' : '') + (expanded ? ' is-open' : '')}>
      <header class="dp-head">
        <h2 class="dp-title">{props.title}</h2>
        {props.hint ? <span class="dp-hint">{props.hint}</span> : null}
        <div class="dp-rail">
          {props.actions}
          {foldable && (
            <button
              type="button"
              class="dp-toggle"
              aria-expanded={expanded}
              onClick={() => setOpen((v) => !v)}
            >
              <span>{expanded ? t('detail.panel.collapse') : t('detail.panel.expand')}</span>
              <Icon name="chevron-down" size={12} />
            </button>
          )}
        </div>
      </header>

      {props.lead ? <div class="dp-lead">{props.lead}</div> : null}

      {showBody ? (
        <div
          ref={bodyRef}
          class={
            'dp-body' + (clipped ? ' is-clipped' : '') + (expanded && props.openMax ? ' is-scroll' : '')
          }
          style={style || undefined}
        >
          {props.children}
        </div>
      ) : null}

      {!hasBody && props.empty ? <p class="dp-empty">{props.empty}</p> : null}
    </section>
  )
}
