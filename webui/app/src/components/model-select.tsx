// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { api, type ModelEntry } from '../api/client'
import { useAsync } from '../hooks'
import { t } from '../i18n'
import { navigate } from '../nav'
import { Icon } from './icons'
import { toast, toastError } from './toast'
import { type Conn, groupConnections, hostOf, labelOf } from '../views/model-connections'

/** The composer's model switch: the model's name and a chevron, sitting
 *  immediately left of Send, opening a card upward.
 *
 *  The card is two panes, because a registry is two levels: an account (an
 *  endpoint plus the key it dials with) and the models that key can ask for.
 *  One flat list of every model said the same account over and over in the
 *  sub-line and left the reader to spot where one account ended and the next
 *  began. Left picks the account, right picks the model inside it, and the
 *  pane on the right follows the pointer down the left one — the shape the
 *  provider switcher this page answers to uses.
 *
 *  What is copied from Codex's own composer rather than invented: the trigger
 *  is ghost chrome, not a control, so the row reads as the caption of what
 *  Send is about to do; the card is a menu (`role="menu"`, `menuitemradio`
 *  rows, a tick on the live model, a section label instead of a title bar);
 *  opening moves focus onto the checked row; Arrow/Home/End walk the rows,
 *  Escape closes back onto the trigger, Tab just leaves.
 *
 *  Two departures are ours: the card ends in a link to the registry (a pick
 *  can only switch, it cannot provision a model), and there is no reasoning
 *  slider — our registry has aliases, not effort tiers. */
export function ModelSelect(props: { disabled?: boolean }) {
  const [open, setOpen] = useState(false)
  const [tick, setTick] = useState(0)
  const [busy, setBusy] = useState('')
  /** Which account's models the card is showing. Null until the card opens,
   *  then the account that owns the live model. */
  const [pane, setPane] = useState<string | null>(null)
  /** The account list is folded away by default: the common visit is "another
   *  model on this account", not "another account". */
  const [connsOpen, setConnsOpen] = useState(false)
  const { data } = useAsync(() => api.models(), [], tick)
  /** Where the card sits, in viewport pixels. Measured rather than absolute:
   *  the card is 430px wide and the trigger sits near the right edge of the
   *  composer, so anchoring it with `right: 0` pushed it off the left of a
   *  phone. Fixed + clamped keeps it inside the window at every width, the
   *  same contract as components/select.tsx. */
  const [at, setAt] = useState<{ left: number; bottom: number; width: number } | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const panel = useRef<HTMLDivElement>(null)

  function place() {
    const r = trigger.current?.getBoundingClientRect()
    if (!r) return
    const width = Math.min(320, window.innerWidth - 16)
    setAt({
      left: Math.max(8, Math.min(r.right - width, window.innerWidth - 8 - width)),
      bottom: window.innerHeight - r.top + 8,
      width,
    })
  }

  const active = data?.active
  // The registry can carry a lone active model with an empty list — a node
  // that never registered an alias. It still has to be pickable, so it joins
  // the models of its account rather than being lost.
  const conns: Conn[] = useMemo(() => (data ? groupConnections(data) : []), [data])
  const liveConn = conns.find((c) => c.members.some((m) => m.entry.active))
  const current = conns.find((c) => c.id === pane) ?? liveConn ?? conns[0]
  const rows: ModelEntry[] = current?.members.map((m) => m.entry) ?? []

  function close(restoreFocus: boolean) {
    setOpen(false)
    if (restoreFocus) trigger.current?.focus()
  }

  // Outside click closes the card; Escape closes it from anywhere and hands
  // focus back to the trigger, which is where a keyboard user left off.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      setOpen(false)
      trigger.current?.focus()
    }
    // A fixed card would be stranded by anything that moves the composer.
    const onMove = () => setOpen(false)
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    window.addEventListener('resize', onMove)
    window.addEventListener('scroll', onMove, true)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
      window.removeEventListener('resize', onMove)
      window.removeEventListener('scroll', onMove, true)
    }
  }, [open])

  // Opening lands on the account that owns the live model, and the model list
  // starts at its top — a card that always opens at row one hides the answer
  // you came for.
  useEffect(() => {
    if (!open) return
    place()
    setPane(liveConn?.id ?? conns[0]?.id ?? null)
    setConnsOpen(false)
    panel.current
      ?.querySelector<HTMLElement>('[role="menuitemradio"][aria-checked="true"]')
      ?.focus({ preventScroll: true })
    panel.current?.querySelector('.model-panel-list')?.scrollTo({ top: 0 })
    // Only on open: `conns` changes identity on every poll.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  /** The rows a keyboard can land on, in visual order: the accounts first,
   *  then the models of the account being shown. Disabled rows are skipped. */
  function menuItems(): HTMLElement[] {
    if (!panel.current) return []
    return Array.from(
      panel.current.querySelectorAll<HTMLElement>(
        '[role="menuitem"]:not(:disabled), [role="menuitemradio"]:not(:disabled)',
      ),
    )
  }

  function onMenuKey(e: KeyboardEvent) {
    const items = menuItems()
    const at = items.indexOf(document.activeElement as HTMLElement)
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      if (items.length === 0) return
      const next = e.key === 'ArrowDown' ? Math.min(at + 1, items.length - 1) : Math.max(at - 1, 0)
      items[next]?.focus()
      return
    }
    if (e.key === 'Home' || e.key === 'End') {
      e.preventDefault()
      items[e.key === 'Home' ? 0 : items.length - 1]?.focus()
      return
    }
    if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
      // Crossing the two panes is one keystroke: the left is accounts, the
      // right is models, and a keyboard user should not have to walk a list
      // to get from one to the other.
      e.preventDefault()
      const target =
        e.key === 'ArrowRight'
          ? panel.current?.querySelector<HTMLElement>('.model-panel-list [role="menuitemradio"]')
          : panel.current?.querySelector<HTMLElement>('.model-panel-conns [role="menuitem"]')
      target?.focus()
      return
    }
    // Tab is the browser's: the card just gets out of its way.
    if (e.key === 'Tab') setOpen(false)
  }

  async function pick(alias: string) {
    if (busy || alias === active?.alias) {
      close(true)
      return
    }
    setBusy(alias)
    try {
      await api.useModel(alias)
      toast(t('sessions.modelSwitched', { alias }))
      setOpen(false)
      trigger.current?.focus()
      // The list is the registry's: the active flag has to move with it.
      setTick((n) => n + 1)
    } catch (err) {
      toastError(err)
    } finally {
      setBusy('')
    }
  }

  /** The account's name, minus a trailing parenthetical. The registry's own
   *  label for a relay reads "自定义 / 中转站 (custom base_url)" — the bracket
   *  is the provider id, and this column is 168px wide. What the bracket was
   *  reaching for (which endpoint) is the line underneath anyway. */
  function connName(c: Conn): string {
    const raw = c.providerLabel || labelOf(data?.providers ?? [], c.provider) || t('models.customProvider')
    const short = raw.replace(/\s*\([^)]*\)\s*$/, '')
    return short || raw
  }

  return (
    <div class="model-select" ref={box}>
      <button
        ref={trigger}
        type="button"
        class={`model-select-btn${open ? ' open' : ''}`}
        /* The tip is suppressed while the card is up: it would sit on top of
           the card it just opened. Same rule as the node picker in the header. */
        data-tip={open ? undefined : t('sessions.modelBtn')}
        aria-label={`${t('sessions.modelBtn')}: ${active?.alias ?? ''}`}
        aria-haspopup="menu"
        aria-expanded={open}
        disabled={props.disabled || busy !== ''}
        onClick={() => (open ? close(false) : setOpen(true))}
      >
        <span class="model-select-content">
          <span class="model-select-name">{active?.alias ?? '—'}</span>
          <Icon name="chevron-down" size={16} class="model-select-arrow" />
        </span>
      </button>
      {open && (
        <div
          ref={panel}
          class="model-panel"
          role="menu"
          aria-label={t('sessions.modelBtn')}
          style={{
            left: `${at?.left ?? 0}px`,
            bottom: `${at?.bottom ?? 0}px`,
            width: at ? `${at.width}px` : undefined,
          }}
          onKeyDown={onMenuKey}
        >
          <p class="model-panel-label">{t('settings.model')}</p>
          <div class="model-panel-list">
            {rows.map((m) => (
              <button
                key={m.alias}
                type="button"
                role="menuitemradio"
                aria-checked={!!m.active}
                class={`model-row${m.active ? ' on' : ''}${busy === m.alias ? ' busy' : ''}`}
                disabled={busy === m.alias}
                onClick={() => void pick(m.alias)}
              >
                <span class="model-row-body">
                  <span class="model-row-alias">{m.alias}</span>
                  {/* What the name points at: an alias is a config, not a
                      model id, and the two routinely differ. When they do
                      not, the line would print the same string twice. */}
                  {m.model && m.model !== m.alias && (
                    <span class="model-row-sub mono">{m.model}</span>
                  )}
                </span>
                {m.active && <Icon name="check" size={16} class="model-row-check" />}
              </button>
            ))}
            {rows.length === 0 && <p class="model-panel-empty">{t('models.empty')}</p>}
          </div>

          {/* One footer band, one rule above it: the account the models belong
              to and the way into the registry are the same kind of row, so
              they share the shape (16px icon column, same inset for the
              label, same trailing edge). Boxed in between two hairlines, the
              account row read as a strip of its own rather than the first of
              the two. */}
          <div class="model-panel-foot">
            {/* The accounts, folded away. Most of the time the card is opened
                to change model *within* the account you are on, so the list of
                accounts is a second question — it stays shut until asked. */}
            <button
              type="button"
              role="menuitem"
              class={`model-conn-toggle${connsOpen ? ' open' : ''}`}
              aria-expanded={connsOpen}
              onClick={() => setConnsOpen((v) => !v)}
            >
              <Icon name="chevron-right" size={16} class="model-conn-toggle-caret" />
              <span class="model-conn-toggle-name">{current ? connName(current) : '—'}</span>
              {current && <span class="model-conn-toggle-sub mono">{hostOf(current.baseURL)}</span>}
            </button>

            {connsOpen && (
              <div class="model-panel-conns" role="group" aria-label={t('models.connections')}>
                {conns.map((c) => {
                  const holdsLive = c.members.some((m) => m.entry.active)
                  const on = c.id === current?.id
                  return (
                    <button
                      key={c.id}
                      type="button"
                      role="menuitem"
                      aria-current={on || undefined}
                      class={`model-conn-row${on ? ' on' : ''}`}
                      onClick={() => {
                        setPane(c.id)
                        setConnsOpen(false)
                      }}
                    >
                      <span class="model-conn-row-body">
                        <span class="model-conn-row-name">{connName(c)}</span>
                        <span class="model-conn-row-sub mono">{hostOf(c.baseURL) || c.apiType}</span>
                      </span>
                      {holdsLive && <Icon name="check" size={14} class="model-row-check" />}
                    </button>
                  )
                })}
              </div>
            )}

            {/* The pick is a switch, not a registry: adding a model, giving
                one a key or testing it lives in the models page, and this is
                the door to it. Codex splits its footer off the same way. */}
            <button
              type="button"
              class="model-foot-btn"
              onClick={() => {
                setOpen(false)
                navigate({ view: 'settings', tab: 'models' })
              }}
            >
              <Icon name="gear" size={16} />
              {t('sessions.modelManage')}
              <Icon name="chevron-right" size={16} class="model-foot-arrow" />
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
