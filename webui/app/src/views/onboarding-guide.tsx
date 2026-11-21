// SPDX-License-Identifier: AGPL-3.0-or-later

import { createPortal } from 'preact/compat'
import { useLayoutEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { navigate } from '../nav'

const STEPS = [
  { target: 'workspace', title: 'nav.workspace', description: 'onboarding.guide.nav.workspace' },
  { target: 'control', title: 'nav.control', description: 'onboarding.guide.nav.control' },
  { target: 'memory', title: 'nav.memory', description: 'onboarding.guide.nav.memory' },
  { target: 'new-chat', title: 'sessions.new', description: 'onboarding.guide.nav.newChat' },
  { target: 'workdir', title: 'sessions.chooseDir', description: 'onboarding.guide.workdirDesc' },
  { target: 'team', title: 'sessions.teamLabel', description: 'onboarding.guide.teamDesc' },
  { target: 'composer', title: 'onboarding.guide.composer', description: 'onboarding.guide.composerDesc' },
] as const

const EDGE = 12
const GAP = 15
const PADDING = 4
const MIN_SIDE_WIDTH = 260

function clamp(value: number, low: number, high: number) {
  return Math.max(low, Math.min(value, high))
}

function position(rect: DOMRect, bubble: HTMLElement, keepPanelClear: boolean) {
  const viewportWidth = document.documentElement.clientWidth
  const viewportHeight = document.documentElement.clientHeight
  if (!rect.width || !rect.height || rect.right <= 0 || rect.left >= viewportWidth
      || rect.bottom <= 0 || rect.top >= viewportHeight) return null

  const left = Math.max(0, rect.left - PADDING)
  const top = Math.max(0, rect.top - PADDING)
  const right = Math.min(viewportWidth, rect.right + PADDING)
  const bottom = Math.min(viewportHeight, rect.bottom + PADDING)
  const leftSpace = left - GAP - EDGE
  const rightSpace = viewportWidth - right - GAP - EDGE
  const sideSpace = Math.max(leftSpace, rightSpace)
  // A tall roster on a smaller desktop leaves very little room above or
  // below. Let the explanation narrow enough to fit beside it.
  const maxWidth = keepPanelClear && sideSpace >= MIN_SIDE_WIDTH ? sideSpace : undefined
  const bounds = bubble.getBoundingClientRect()
  const width = Math.min(bounds.width, maxWidth ?? bounds.width)
  const height = bounds.height
  const centerX = (left + right) / 2
  const centerY = (top + bottom) / 2
  const corner = (value: number, length: number) => clamp(value, 17, length - 17)
  const hole = `polygon(evenodd, 0 0, 100% 0, 100% 100%, 0 100%, 0 0, ${left}px ${top}px, ${right}px ${top}px, ${right}px ${bottom}px, ${left}px ${bottom}px, ${left}px ${top}px, 0 0)`
  const highlight = { left, top, width: right - left, height: bottom - top }

  if (width <= rightSpace) {
    const y = clamp(centerY - height / 2, EDGE, viewportHeight - height - EDGE)
    return { hole, highlight, x: right + GAP, y, arrow: corner(centerY - y, height), side: 'right', maxWidth }
  }
  if (width <= leftSpace) {
    const y = clamp(centerY - height / 2, EDGE, viewportHeight - height - EDGE)
    return { hole, highlight, x: left - GAP - width, y, arrow: corner(centerY - y, height), side: 'left', maxWidth }
  }
  const below = viewportHeight - bottom >= top
  const x = clamp(centerX - width / 2, EDGE, viewportWidth - width - EDGE)
  const y = clamp(below ? bottom + GAP : top - GAP - height, EDGE, viewportHeight - height - EDGE)
  return { hole, highlight, x, y, arrow: corner(centerX - x, width), side: below ? 'below' : 'above', maxWidth }
}

function directoryDialog() {
  return document.querySelector('[data-tour-target="workdir"] .dir-browser-modal')
}

function directoryTrigger() {
  return document.querySelector<HTMLButtonElement>('[data-tour-target="workdir"] [data-tour-focus]')
}

function closeDirectoryPanel() {
  const trigger = directoryTrigger()
  if (trigger?.getAttribute('aria-expanded') === 'true') trigger.click()
}

export function GuideTour({ onFinish, setRailOpen, setTeamLessonActive }: {
  onFinish(): void
  setRailOpen(open: boolean): void
  setTeamLessonActive(active: boolean): void
}) {
  const [index, setIndex] = useState(0)
  const [browsing, setBrowsing] = useState(false)
  const [placement, setPlacement] = useState<ReturnType<typeof position>>(null)
  const bubble = useRef<HTMLElement>(null)
  const primary = useRef<HTMLButtonElement>(null)
  const step = STEPS[index]!
  const final = index === STEPS.length - 1
  const directory = step.target === 'workdir'
  const team = step.target === 'team'
  const picker = directory || team
  const firstChat = index >= 3

  function finish() {
    if (directory) closeDirectoryPanel()
    onFinish()
  }

  function back() {
    if (directory) closeDirectoryPanel()
    setIndex(index - 1)
  }

  function advance(fromTarget = false) {
    if (final) {
      finish()
      return
    }
    if (directory) closeDirectoryPanel()
    if (step.target === 'new-chat' && !fromTarget) navigate({ view: 'sessions', id: null })
    setIndex(index + 1)
  }

  // Keep the composer controls visible while the reader is practicing.
  useLayoutEffect(() => {
    setRailOpen(!picker && !final)
    return () => setRailOpen(false)
  }, [picker, final, setRailOpen])

  useLayoutEffect(() => {
    if (!team) return
    setTeamLessonActive(true)
    return () => setTeamLessonActive(false)
  }, [team, setTeamLessonActive])

  useLayoutEffect(() => {
    const panel = bubble.current!
    let target: HTMLElement | null = null
    let region: HTMLElement | null = null
    let paused = false
    let previous = ''
    let frame = 0
    const resize = new ResizeObserver(() => scheduleMeasure())
    const measure = () => {
      frame = 0
      const nextTarget = document.querySelector<HTMLElement>(`[data-tour-target="${step.target}"]`)
      if (nextTarget !== target) {
        if (target) resize.unobserve(target)
        target = nextTarget
        if (target) {
          resize.observe(target)
          target.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'instant' })
        }
      }
      const nextRegion = target?.querySelector<HTMLElement>('[data-tour-region]') ?? null
      if (nextRegion !== region) {
        if (region) resize.unobserve(region)
        region = nextRegion
        if (region) resize.observe(region)
      }
      const pause = directory && !!directoryDialog()
      if (pause !== paused) {
        paused = pause
        setBrowsing(pause)
      }
      let bounds = target?.getBoundingClientRect()
      if (bounds && region) {
        const card = region.getBoundingClientRect()
        const left = Math.min(bounds.left, card.left)
        const top = Math.min(bounds.top, card.top)
        bounds = new DOMRect(left, top, Math.max(bounds.right, card.right) - left, Math.max(bounds.bottom, card.bottom) - top)
      }
      const next = bounds && !pause ? position(bounds, panel, team && !!region) : null
      const signature = JSON.stringify(next)
      if (signature !== previous) {
        setPlacement(next)
        previous = signature
      }
    }
    function scheduleMeasure() {
      if (!frame) frame = requestAnimationFrame(measure)
    }
    // Observe the console only. The portal's style changes must not trigger
    // more layout work, while route mounts and mobile drawer changes must.
    const shell = document.querySelector('.shell')!
    const mutations = new MutationObserver(scheduleMeasure)
    mutations.observe(shell, { subtree: true, childList: true, attributes: true, attributeFilter: ['class', 'hidden'] })
    resize.observe(panel)
    resize.observe(shell)
    window.addEventListener('resize', scheduleMeasure)
    window.addEventListener('scroll', scheduleMeasure, true)
    measure()
    primary.current?.focus({ preventScroll: true })

    const isTarget = (node: EventTarget | null) => node instanceof Node && !!target?.contains(node)
    function onClick(event: MouseEvent) {
      // Let the real link/button handle its click, including normal routing.
      if (!final && !picker && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && isTarget(event.target)) {
        advance(true)
      }
    }
    function onFocus(event: FocusEvent) {
      if (directory && directoryDialog()) return
      if (event.target instanceof Node && !panel.contains(event.target) && !isTarget(event.target)) {
        primary.current?.focus({ preventScroll: true })
      }
    }
    function onKey(event: KeyboardEvent) {
      if (directory && directoryDialog()) return
      if (event.key === 'Escape') {
        event.preventDefault()
        event.stopImmediatePropagation()
        const openTrigger = picker ? target?.querySelector<HTMLButtonElement>('button[aria-expanded="true"]') : null
        if (openTrigger) {
          openTrigger.click()
          openTrigger.focus({ preventScroll: true })
        } else {
          finish()
        }
        return
      }
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        event.stopPropagation()
        return
      }
      if (event.key === 'Tab') {
        const targetItems = picker
          ? [...(target?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled)') ?? [])]
          : target ? [target] : []
        const items = [...targetItems, ...panel.querySelectorAll<HTMLElement>('button')]
          .filter((item) => item.getClientRects().length)
        const current = items.indexOf(document.activeElement as HTMLElement)
        const next = current === -1 ? (event.shiftKey ? items.length - 1 : 0)
          : (current + (event.shiftKey ? -1 : 1) + items.length) % items.length
        event.preventDefault()
        items[next]?.focus({ preventScroll: true })
        return
      }
      // The final highlight lets the reader focus and type in the editor
      // without allowing a practice message to be submitted by Enter.
      if (final && isTarget(event.target) && event.key === 'Enter' && !event.isComposing && !event.shiftKey) {
        event.preventDefault()
        event.stopPropagation()
      }
    }
    document.addEventListener('click', onClick, true)
    document.addEventListener('focusin', onFocus, true)
    document.addEventListener('keydown', onKey, true)
    return () => {
      cancelAnimationFrame(frame)
      resize.disconnect()
      mutations.disconnect()
      window.removeEventListener('resize', scheduleMeasure)
      window.removeEventListener('scroll', scheduleMeasure, true)
      document.removeEventListener('click', onClick, true)
      document.removeEventListener('focusin', onFocus, true)
      document.removeEventListener('keydown', onKey, true)
    }
    // The current step owns its observers and its click handler.
  }, [index, final, onFinish])

  return createPortal(
    <div class="guide-tour" hidden={browsing}>
      <div class="guide-tour-shade" style={{ clipPath: placement?.hole }} />
      {placement && <div class="guide-tour-highlight" style={placement.highlight} />}
      <section
        class="guide-tour-bubble"
        ref={bubble}
        role="dialog"
        aria-labelledby="guide-tour-title"
        aria-describedby="guide-tour-desc"
        data-side={placement?.side ?? 'center'}
        style={placement ? {
          left: placement.x,
          top: placement.y,
          maxWidth: placement.maxWidth,
          '--guide-arrow': `${placement.arrow}px`,
        } : undefined}
      >
        <div class="guide-tour-body">
          <div class="guide-tour-progress" aria-live="polite">
            <span>{t(firstChat ? 'onboarding.guide.firstChat' : 'onboarding.guide.overview')}</span>
            <span>{index + 1} / {STEPS.length}</span>
          </div>
          <div class="guide-tour-track" aria-hidden="true">
            {STEPS.map((item, i) => <span key={item.target} data-status={i === index ? 'current' : i < index ? 'done' : 'todo'} />)}
          </div>
          <h2 id="guide-tour-title">{t(step.title)}</h2>
          <p id="guide-tour-desc">{t(step.description)}</p>
          {final ? (
            <div class="guide-tour-example">
              <strong>{t('onboarding.guide.exampleLabel')}</strong>
              <p>{t('onboarding.guide.example')}</p>
            </div>
          ) : !picker && <p class="guide-tour-hint">{t('onboarding.guide.tryClick')}</p>}
        </div>
        <footer class="guide-tour-actions">
          <button type="button" class="btn ghost" onClick={finish}>{t('onboarding.guide.skip')}</button>
          <div class="guide-tour-nav">
            {index > 0 && <button type="button" class="btn" onClick={back}>{t('onboarding.guide.back')}</button>}
            <button type="button" class="btn primary" ref={primary} onClick={() => advance()}>
              {t(final ? 'onboarding.guide.done' : 'onboarding.next')}
            </button>
          </div>
        </footer>
      </section>
    </div>,
    document.body,
  )
}
