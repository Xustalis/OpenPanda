// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { Icon } from './icons'

/** One heading in the list, with the ids under it and their position in the
 *  flattened order the keyboard walks. */
type ModelGroup = { vendor: string; items: { id: string; at: number }[] }

/** The vendor a row belongs to.
 *
 *  The provider's own `owned_by` wins. That is the contract CC Switch's picker
 *  is built on (its ModelDropdown groups by `ownedBy`, falling back to "Other"
 *  when a row has none), and it is the only vendor signal a catalogue carries.
 *
 *  Anthropic- and Google-shaped catalogues ship no vendor at all, and a relay
 *  that copies neither would put all 463 ids under one heading — the flat list
 *  this picker exists to break up. So a row without `owned_by` falls back to
 *  the head of its own id: `claude-3-5-sonnet` and `claude-opus` belong
 *  together whether or not the endpoint says so. A head that is a bare version
 *  (`4.0Ultra`) names no family and lands in the trailing group. */
function vendorOf(id: string, owners?: Record<string, string>): string {
  const owned = owners?.[id]?.trim()
  if (owned) return owned
  const head = id.split(/[-_/.\s]/)[0] ?? ''
  return head.length >= 2 && /^[a-z]/i.test(head) ? head.toLowerCase() : ''
}

/** A model-id field with the fetched list built into it — one control, not two.
 *
 *  The input owns the value, so typing a model the provider does not list
 *  still works; a caret inside the field opens the ids the provider just
 *  returned. This is the affordance `<datalist>` was reaching for, except the
 *  browser filters a datalist by whatever the input already holds — so a
 *  prefilled catalogue default left the list empty and the caret dead. Here
 *  the list is ours: it always shows every fetched id, and a pick writes
 *  straight back into the field.
 *
 *  A large roster is a search problem before it is a scroll problem: 463 ids
 *  in one column is a list nobody reads to the end. So the list carries its own
 *  search field (ids and vendors, every space-separated term must hit) and
 *  groups rows under vendor headings that stay pinned while the list scrolls.
 *  Arrow keys walk the filtered order, Enter picks, Escape closes. */
export function ModelPicker(props: {
  value: string
  onValue(v: string): void
  options: string[]
  /** Vendor per model id, when the provider reports one. Sparse by design:
   *  a catalogue without `owned_by` simply has no entries. */
  owners?: Record<string, string>
  placeholder?: string
  required?: boolean
  id?: string
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  // Fixed, not absolute: the field lives inside the add-model sheet, whose
  // body scrolls and clips. An absolutely positioned list was cut off at the
  // sheet's footer, so a 21-model roster showed its first two ids and the rest
  // were unreachable. Same contract as components/select.tsx: anchor to the
  // field's viewport rect, flip above when the room below is short, and close
  // when anything moves underneath it.
  const [box, setBox] = useState<{ left: number; top: number; width: number; maxHeight: number } | null>(
    null,
  )
  const boxRef = useRef<HTMLDivElement>(null)
  const fieldRef = useRef<HTMLInputElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const hasOptions = props.options.length > 0

  // Every space-separated term has to hit the id or its vendor, so
  // "claude sonnet" narrows instead of matching nothing.
  const groups = useMemo<ModelGroup[]>(() => {
    const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
    const buckets = new Map<string, string[]>()
    for (const id of props.options) {
      const vendor = vendorOf(id, props.owners)
      if (terms.length > 0) {
        const hay = `${id} ${vendor}`.toLowerCase()
        if (!terms.every((w) => hay.includes(w))) continue
      }
      const list = buckets.get(vendor)
      if (list) list.push(id)
      else buckets.set(vendor, [id])
    }
    // A heading over a single row is noise: it takes the same vertical space
    // as the row it introduces and reads as a second copy of it — "advanced"
    // above "advanced custom", "chatgpt" above "chatgpt-4o-latest". A group
    // earns its heading at two members; a lone id falls through to the
    // trailing group, which is where a reader already looks for strays. A
    // roster of twenty-two ids ends up with a handful of real headings
    // instead of eleven one-row captions.
    const strays: { id: string; pos: number }[] = []
    const kept: { vendor: string; ids: string[] }[] = []
    // First position wins: a roster can list the same id twice (relays do),
    // and both copies must keep the order the provider used.
    const pos = new Map<string, number>()
    props.options.forEach((id, i) => {
      if (!pos.has(id)) pos.set(id, i)
    })
    for (const [vendor, ids] of buckets) {
      if (vendor !== '' && ids.length >= 2) kept.push({ vendor, ids })
      else ids.forEach((id) => strays.push({ id, pos: pos.get(id) ?? 0 }))
    }
    kept.sort((a, b) => a.vendor.localeCompare(b.vendor))
    if (strays.length > 0) {
      // Original order, so the trailing group reads in the same sequence the
      // provider listed it rather than in bucket-creation order.
      strays.sort((a, b) => a.pos - b.pos)
      kept.push({ vendor: '', ids: strays.map((s) => s.id) })
    }
    // Number the rows here, after the sort, so `at` indexes the same flat
    // order the arrow keys walk. Numbering them while bucketing would count in
    // the provider's order, and the cursor would land on the wrong row.
    let at = 0
    return kept.map(({ vendor, ids }) => ({
      vendor,
      items: ids.map((id) => ({ id, at: at++ })),
    }))
  }, [props.options, props.owners, query])

  const flat = useMemo(() => groups.flatMap((g) => g.items.map((r) => r.id)), [groups])

  const openList = () => {
    const r = boxRef.current?.getBoundingClientRect()
    if (r) setBox({ left: r.left, top: r.bottom + 4, width: r.width, maxHeight: 224 })
    setQuery('')
    setOpen(true)
  }

  // Measured after the list exists but before paint: the first frame lands it
  // under the field, this pass flips it up when the space below cannot hold
  // it and keeps it inside the viewport. The search field is part of the
  // panel's height, so the room it needs is the whole panel's, not the
  // scrolling body's.
  useLayoutEffect(() => {
    if (!open) return
    const anchor = boxRef.current?.getBoundingClientRect()
    const panel = panelRef.current
    const scroll = scrollRef.current
    const search = searchRef.current
    if (!anchor || !panel || !scroll || !search) return
    const gap = 4
    const roomBelow = window.innerHeight - anchor.bottom - 8
    const roomAbove = anchor.top - 8
    // The list wants its content height, capped at 360. Flip only when the
    // room below cannot hold that — and in either direction the cap is the
    // room actually available, or the list grows past the window (it did:
    // flipping up gave it the whole 648px above and it ran off the bottom).
    const want = Math.min(search.offsetHeight + scroll.scrollHeight + 8, 360)
    const up = want > roomBelow && roomAbove > roomBelow
    const cap = Math.max(140, Math.min(want, up ? roomAbove : roomBelow))
    // Wider than the field: model ids are long, and the field is often the
    // narrow column of a table. Capped so a full-width settings field does not
    // turn the list into a two-metre banner.
    const width = Math.min(Math.max(anchor.width, 300), 420)
    setBox({
      left: Math.max(8, Math.min(anchor.left, window.innerWidth - 8 - width)),
      top: up ? Math.max(8, anchor.top - gap - cap) : anchor.bottom + gap,
      width: Math.min(width, window.innerWidth - 16),
      maxHeight: cap,
    })
    const at = flat.indexOf(props.value)
    setActive(at >= 0 ? at : 0)
    searchRef.current?.focus()
    // flat is derived from options/owners/query, all of which the effect below
    // already re-runs on; depending on it here would re-measure every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  // A new query is a new list: the cursor belongs at its top, not at whatever
  // row happened to hold the same index before. Opening is not a query change
  // — the layout pass above has just put the cursor on the current value, and
  // resetting it here would drag it back to the first row. Reopening after a
  // search does clear the query, so that reset has to be recognised as the
  // opening, not as typing.
  const lastQuery = useRef('')
  const wasOpen = useRef(false)
  useEffect(() => {
    const justOpened = open && !wasOpen.current
    wasOpen.current = open
    if (justOpened || lastQuery.current === query) {
      lastQuery.current = query
      return
    }
    lastQuery.current = query
    setActive(0)
  }, [open, query])

  // Close on an outside click, on any scroll outside the list, on resize, and
  // on Escape without letting the dialog's own Escape handler see it — one
  // Escape closes the list, the next the modal.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false)
    }
    const onScroll = (e: Event) => {
      if (e.target instanceof Node && scrollRef.current?.contains(e.target)) return
      setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      // The IME owns this key while composing; the surrounding form must
      // not interpret it as a request to close either.
      if (e.isComposing) return
      e.preventDefault()
      setOpen(false)
      fieldRef.current?.focus()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onScroll)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onScroll)
    }
  }, [open])

  // Keep the keyboard position visible when the filtered list is taller than
  // its box.
  useEffect(() => {
    if (!open) return
    scrollRef.current
      ?.querySelector<HTMLElement>('.model-picker-item.cursor')
      ?.scrollIntoView({ block: 'nearest' })
  }, [open, active])

  const pick = (id: string | undefined) => {
    if (!id) return
    props.onValue(id)
    setOpen(false)
    fieldRef.current?.focus()
  }

  const onKey = (e: KeyboardEvent) => {
    // A composing IME uses Enter to commit the candidate, not to pick a row.
    if (e.isComposing) return
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        if (flat.length > 0) setActive((i) => (i + 1) % flat.length)
        break
      case 'ArrowUp':
        e.preventDefault()
        if (flat.length > 0) setActive((i) => (i - 1 + flat.length) % flat.length)
        break
      // Home/End stay with the text field: the search box is a text field
      // first, and moving its caret to an end is the more common intent than
      // jumping to the last of 463 rows.
      case 'Enter':
        e.preventDefault()
        pick(flat[active])
        break
      case 'Tab':
        setOpen(false)
        break
    }
  }

  return (
    <div class="model-picker" ref={boxRef}>
      <input
        id={props.id}
        ref={fieldRef}
        class="input mono"
        type="text"
        value={props.value}
        placeholder={props.placeholder}
        required={props.required}
        disabled={props.disabled}
        onInput={(e) => props.onValue((e.target as HTMLInputElement).value)}
      />
      {hasOptions && (
        <button
          type="button"
          class="model-picker-toggle"
          aria-label={t('models.pickFromList')}
          title={t('models.pickFromList')}
          aria-haspopup="menu"
          aria-expanded={open}
          disabled={props.disabled}
          onClick={() => (open ? setOpen(false) : openList())}
        >
          <Icon name="chevron-down" size={14} />
        </button>
      )}
      {open && hasOptions && (
        <div
          ref={panelRef}
          class="model-picker-list"
          role="listbox"
          aria-label={t('models.pickFromList')}
          style={{
            left: `${box?.left ?? 0}px`,
            top: `${box?.top ?? 0}px`,
            width: box ? `${box.width}px` : undefined,
            maxHeight: box ? `${box.maxHeight}px` : undefined,
          }}
          onKeyDown={onKey}
        >
          <div class="model-picker-search">
            <Icon name="search" size={13} />
            <input
              ref={searchRef}
              type="text"
              value={query}
              placeholder={t('models.searchPlaceholder')}
              aria-label={t('models.searchPlaceholder')}
              autocomplete="off"
              spellcheck={false}
              onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
            />
            {query !== '' && (
              <button
                type="button"
                class="model-picker-search-clear"
                aria-label={t('models.searchClear')}
                title={t('models.searchClear')}
                onClick={() => {
                  setQuery('')
                  searchRef.current?.focus()
                }}
              >
                <Icon name="x" size={12} />
              </button>
            )}
          </div>
          <div ref={scrollRef} class="model-picker-scroll">
            {flat.length === 0 ? (
              <p class="model-picker-empty">{t('models.searchEmpty')}</p>
            ) : (
              groups.map((g) => (
                <div class="model-picker-group" key={g.vendor}>
                  {/* One group is not a grouping: with nothing to divide, the
                      heading would just be a caption on the whole list. */}
                  {groups.length > 1 && (
                    <div class="model-picker-group-head">
                      {g.vendor || t('models.groupOther')}
                    </div>
                  )}
                  {g.items.map((row) => (
                    <button
                      key={`${row.id}-${row.at}`}
                      type="button"
                      role="option"
                      aria-selected={row.id === props.value}
                      title={row.id}
                      class={`model-picker-item${row.id === props.value ? ' on' : ''}${
                        row.at === active ? ' cursor' : ''
                      }`}
                      onMouseEnter={() => setActive(row.at)}
                      onClick={() => pick(row.id)}
                    >
                      {row.id}
                    </button>
                  ))}
                </div>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  )
}
