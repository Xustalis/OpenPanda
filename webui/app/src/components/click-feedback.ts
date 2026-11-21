// SPDX-License-Identifier: AGPL-3.0-or-later

// A single capture listener covers lazy routes, menus and modal controls. The
// feedback stays at the activation point even if the action replaces its row.
export function installClickFeedback(): () => void {
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)')
  const selector = 'button, a[href], input, textarea, select, summary, label, [role="button"], [role="option"], [role="tab"], [tabindex="0"], [contenteditable="true"]'
  function paint(target: Element, x: number, y: number, control: boolean) {
    const dot = document.createElement('span')
    dot.className = control ? 'click-feedback control' : 'click-feedback'
    dot.setAttribute('aria-hidden', 'true')
    dot.style.left = x + 'px'
    dot.style.top = y + 'px'
    // Native dialogs and popovers paint above every ordinary stacking context.
    const layer = target.closest('dialog[open], [popover]') ?? document.body
    layer.appendChild(dot)
    const remove = () => dot.remove()
    dot.addEventListener('animationend', remove, { once: true })
    window.setTimeout(remove, 400)
  }
  function click(event: MouseEvent) {
    if (!event.isTrusted || event.button !== 0 || reduced.matches || !(event.target instanceof Element)) return
    const target = event.target
    const control = target.closest(selector)
    if (target.closest('[inert], :disabled, [aria-disabled="true"]')) return
    // Clicking a label forwards a second click to its associated control.
    if (target.closest('label') && !target.matches('input, textarea, select') && target.closest('label')?.control) return
    if (event.detail === 0) {
      if (!control) return
      const bounds = control.getBoundingClientRect()
      paint(target, bounds.left + bounds.width / 2, bounds.top + bounds.height / 2, true)
    } else {
      paint(target, event.clientX, event.clientY, !!control)
    }
  }
  // Custom session rows activate directly on keydown. Native controls already
  // emit a keyboard click, so only the focused custom target needs this path.
  function keydown(event: KeyboardEvent) {
    if (!event.isTrusted || event.repeat || reduced.matches || !['Enter', ' '].includes(event.key)) return
    const target = event.target
    if (!(target instanceof HTMLElement) || !target.matches('[role="button"]:not(button, a, input)') || target.closest('[inert], [aria-disabled="true"]')) return
    const bounds = target.getBoundingClientRect()
    paint(target, bounds.left + bounds.width / 2, bounds.top + bounds.height / 2, true)
  }
  document.addEventListener('keydown', keydown, true)
  document.addEventListener('click', click, true)
  return () => {
    document.removeEventListener('click', click, true)
    document.removeEventListener('keydown', keydown, true)
  }
}
