// SPDX-License-Identifier: AGPL-3.0-or-later

/** The argument of a `/goal` draft, or null when the line is not one. Kept
 *  beside the parser it feeds so the composer's intent logic can be tested
 *  without dragging the panel's API layer in. */
export function goalArgument(input: string): string | null {
  const match = /^\/goal(?:\s+([\s\S]*))?$/i.exec(input.trim())
  return match ? (match[1] ?? '').trim() : null
}

/** Only the explicit command flag sets a budget. Numbers in an objective do not. */
export function goalBudget(argument: string): { text: string; tokens?: number; invalid?: boolean } {
  if (!/^--tokens(?:\s|$)/i.test(argument)) return { text: argument }
  const match = /^--tokens\s+(\d+)\s+([\s\S]+)$/i.exec(argument)
  const tokens = Number(match?.[1])
  const text = match?.[2]?.trim()
  if (!text || !Number.isSafeInteger(tokens) || tokens <= 0) return { text: argument, invalid: true }
  return { text, tokens }
}

export function goalCommand(argument: string) {
  const text = argument.trim()
  if (/^--(?:\s|$)/.test(text)) return { action: 'create' as const, objective: text.slice(2).trim() }
  switch (text.toLowerCase()) {
    case 'pause': return { action: 'pause' as const }
    case 'resume': return { action: 'resume' as const }
    case 'clear': return { action: 'clear' as const }
    case 'stop': return { action: 'stop' as const }
  }
  const edit = /^edit(?:\s+([\s\S]*))?$/i.exec(text)
  if (edit) return { action: 'edit' as const, objective: edit[1]?.trim() ?? '' }
  return { action: 'create' as const, objective: text }
}
