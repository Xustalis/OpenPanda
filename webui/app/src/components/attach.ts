// Pure helpers for the composer's `@file` attachments and `/export`
// transcript serialization — kept apart from the view so node --test can
// hit them without a DOM.

/** The `@path` token under the caret: [start, caret) covers `@token`, where
 *  token may be a partial path the /api/fs/files endpoint filters on. */
export function atQuery(
  text: string,
  caret: number,
): { start: number; token: string } | null {
  const before = text.slice(0, caret)
  const m = /(^|\s)@([^\s@]*)$/.exec(before)
  if (!m || m[2] === undefined) return null
  return { start: caret - m[2].length - 1, token: m[2] }
}

/** Markdown fence language for an @file attachment — mirrors the TUI's
 *  fenceLang so the model sees code, not prose. */
export function fenceLang(path: string): string {
  const ext = path.slice(path.lastIndexOf('.')).toLowerCase()
  const table: Record<string, string> = {
    '.go': 'go', '.py': 'python', '.ts': 'typescript', '.tsx': 'typescript',
    '.js': 'javascript', '.jsx': 'javascript', '.rs': 'rust',
    '.sh': 'bash', '.bash': 'bash', '.zsh': 'bash',
    '.yaml': 'yaml', '.yml': 'yaml', '.json': 'json', '.md': 'markdown',
    '.sql': 'sql', '.c': 'c', '.h': 'c', '.cpp': 'cpp', '.cc': 'cpp',
    '.hpp': 'cpp', '.java': 'java', '.toml': 'toml',
  }
  return table[ext] ?? ''
}

export interface FsReadResult {
  content?: string
  truncated?: boolean
}

/** Expand every `@path` token via `read` into fenced blocks appended after
 *  the prompt — the same transformation the REPL applies before the prompt
 *  leaves the CLI. Unresolvable tokens (emails, decorators) pass through
 *  untouched, matching TUI behavior, and a path mentioned twice expands
 *  once. */
export async function expandFileRefs(
  text: string,
  read: (path: string) => Promise<FsReadResult>,
): Promise<{ prompt: string; attached: string[] }> {
  const blocks: string[] = []
  const attached: string[] = []
  const seen = new Set<string>()
  for (const field of text.split(/\s+/)) {
    if (!field.startsWith('@')) continue
    const raw = field.slice(1).replace(/[.,;:!?)]+$/, '')
    if (!raw || seen.has(raw)) continue
    try {
      const f = await read(raw)
      seen.add(raw)
      attached.push(raw)
      const body = (f.content ?? '').replace(/\n+$/, '') + (f.truncated ? '\n… (truncated)' : '')
      blocks.push(`\`${raw}\`:\n\`\`\`${fenceLang(raw)}\n${body}\n\`\`\``)
    } catch {
      // not a readable path — leave the token alone
    }
  }
  if (blocks.length === 0) return { prompt: text, attached }
  return { prompt: `${text}\n\n${blocks.join('\n\n')}`, attached }
}

export interface ExportMsg {
  role: string
  text: string
  thought?: string
}

/** `/export`: serialize the transcript to Markdown — the web equivalent of
 *  the REPL writing chat-<ts>.md to disk. */
export function exportMarkdown(
  msgs: ExportMsg[],
  meta: { project?: string | null; title?: string | null },
): string {
  const lines: string[] = [
    `# OpenPanda conversation`,
    ``,
    `_${new Date().toISOString()}${meta.project ? ` · project ${meta.project}` : ''}_`,
  ]
  for (const m of msgs) {
    lines.push('', `## ${m.role === 'user' ? 'You' : 'Panda'}`, '', m.text.trim())
    if (m.thought) {
      lines.push('', `<details><summary>thought</summary>`, '', m.thought.trim(), `</details>`)
    }
  }
  return lines.join('\n')
}

/** Filesystem-safe filename for the export download. */
export function exportFilename(title?: string | null): string {
  const name = (title ?? '')
    .replace(/[^\w-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48)
  return `${name || 'chat'}.md`
}
