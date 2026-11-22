// SPDX-License-Identifier: AGPL-3.0-or-later
import { useEffect, useState } from 'preact/hooks'
import type { IconName } from './components/icons'

export const shortcuts = {
  search: ['palette.trigger','search','Mod+K'],
  guide: ['settings.shortcuts','keyboard','Mod+/'],
  settings: ['nav.settings','gear','Mod+,'],
  notifications: ['notifications.title','bell','Mod+Alt+4'],
  chat: ['sessions.new','square-pen','Mod+Alt+1'],
  workspace: ['nav.workspace','devices','Mod+Alt+2'],
  control: ['nav.control','kanban','Mod+Alt+3'],
  plans: ['nav.plans','list-checks','Mod+Alt+5'],
  projects: ['nav.projects','folder','Mod+Alt+6'],
  memory: ['nav.memory','orbit','Mod+Alt+7'],
  goal: ['sessions.goal.label','target','Mod+Alt+G'],
  dock: ['keys.toggleDock','panel-right','Mod+Alt+B'],
  files: ['dock.files','folder-open','Mod+Alt+P'],
  changes: ['dock.changes','file-diff','Mod+Alt+D'],
  side: ['dock.side','message','Mod+Alt+J'],
} as const satisfies Record<string, readonly [string, IconName, string]>
export type ShortcutID = keyof typeof shortcuts
export const shortcutIds = Object.keys(shortcuts) as ShortcutID[]
const storageKey = 'openpanda.shortcuts.v1'
export type Bindings = Partial<Record<ShortcutID, string>>
const validChord = /^(?:(?:Mod\+)(?:Alt\+)?(?:Shift\+)?|Alt\+(?:Shift\+)?)(?:[A-Z0-9/,.;\-]|Enter|Space|Arrow(?:Left|Right|Up|Down))$/
function readBindings(): Bindings {
  try {
    const data = JSON.parse(localStorage.getItem(storageKey) || '{}')
    return Object.fromEntries(shortcutIds.filter(id => typeof data?.[id] === 'string' && (data[id] === '' || validChord.test(data[id]))).map(id => [id,data[id]]))
  } catch { return {} }
}
export function useBindings() {
  const [bindings,setBindings] = useState(readBindings)
  useEffect(() => {
    const update = () => setBindings(readBindings())
    const sync = (e: StorageEvent) => { if (e.key === null || e.key === storageKey) update() }
    window.addEventListener('storage',sync)
    window.addEventListener('panda-shortcuts',update)
    update()
    return () => { window.removeEventListener('storage',sync); window.removeEventListener('panda-shortcuts',update) }
  },[])
  return bindings
}
export function saveBindings(bindings: Bindings) {
  localStorage.setItem(storageKey,JSON.stringify(bindings))
  window.dispatchEvent(new Event('panda-shortcuts'))
}
export function chordOf(id: ShortcutID, bindings: Bindings) { return bindings[id] ?? shortcuts[id][2] }
function appleKeyboard() {
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } }
  return /mac|iphone|ipad|ipod|ios/i.test(nav.userAgentData?.platform || nav.platform)
}
export function shortcutKeys(chord: string): string[] {
  if (!chord) return []
  const apple = appleKeyboard()
  return chord.split('+').map(k => k === 'Mod' ? (apple ? '⌘' : 'Ctrl') : apple && k === 'Alt' ? '⌥' : apple && k === 'Shift' ? '⇧' : k)
}
export function eventChord(e: KeyboardEvent): string {
  if (e.defaultPrevented || e.repeat || e.isComposing || e.keyCode === 229 || e.getModifierState('AltGraph')) return ''
  const apple = appleKeyboard()
  if (apple ? e.ctrlKey : e.metaKey) return ''
  const mod = apple ? e.metaKey : e.ctrlKey
  if (!mod && !e.altKey) return ''
  const punctuation: Record<string,string> = { Slash:'/', Comma:',', Period:'.', Semicolon:';', Minus:'-', Space:'Space' }
  const key = punctuation[e.code] || (/^(Key[A-Z]|Digit[0-9])$/.test(e.code) ? e.code.slice(-1) : e.key)
  const chord = [mod && 'Mod',e.altKey && 'Alt',e.shiftKey && 'Shift',key].filter(Boolean).join('+')
  return validChord.test(chord) ? chord : ''
}
export function shortcutFor(event: KeyboardEvent, bindings: Bindings) {
  const chord = eventChord(event)
  return chord ? shortcutIds.find(id => chordOf(id,bindings) === chord) : undefined
}
