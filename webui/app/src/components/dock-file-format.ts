// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure formatting helpers for the dock's read-only file viewer.
//
// Two jobs live here, both deliberately free of DOM and Preact so `node --test`
// can exercise them:
//
//  1. Preparing a Markdown document for the shared renderer. That renderer was
//     built for chat replies, where a relative link has nowhere to go. In a
//     file it has somewhere to go: the file beside it. Rather than fork the
//     parser, relative links and images are rewritten to `#file:` / `#image:`
//     anchors (the only href shapes the chat renderer already accepts) and the
//     viewer handles the click.
//  2. A small, stateful line colouriser. A source window is at most 600 lines;
//     a full grammar would be a dependency and a maintenance tax. This one
//     recognises comments, strings, numbers, keywords, keys, tags and Markdown
//     markers — enough that a wall of monochrome text reads as code.

export type SourceKind = 'code' | 'markup' | 'style' | 'data' | 'markdown' | 'plain'

export type TokenKind =
  | 'comment'
  | 'string'
  | 'number'
  | 'keyword'
  | 'type'
  | 'tag'
  | 'attr'
  | 'key'
  | 'marker'
  | 'plain'

export interface Token {
  text: string
  kind: TokenKind
}

export interface SourceLang {
  kind: SourceKind
  /** `#` starts a line comment (Python, shell, YAML, TOML…). */
  hashComments: boolean
  /** `--` starts a line comment (SQL, Lua). */
  dashComments: boolean
}

const MARKUP_EXTS = new Set(['html', 'htm', 'xml', 'svg', 'vue', 'svelte', 'xhtml'])
const STYLE_EXTS = new Set(['css', 'scss', 'sass', 'less'])
const DATA_EXTS = new Set(['json', 'jsonl', 'yaml', 'yml', 'toml', 'ini', 'cfg', 'conf', 'env', 'csv', 'properties'])
const HASH_COMMENT_EXTS = new Set([
  'py', 'pyw', 'rb', 'sh', 'bash', 'zsh', 'ps1', 'yaml', 'yml', 'toml', 'ini',
  'cfg', 'conf', 'env', 'r', 'pl', 'mk', 'makefile', 'dockerfile',
])
const DASH_COMMENT_EXTS = new Set(['sql', 'lua', 'hs'])
// Anything with a known code extension gets the code grammar. Unknown text
// formats stay plain: colouring a .log as if it were Go is worse than no colour.
const CODE_EXTS = new Set([
  'go', 'ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs', 'rs', 'java', 'kt', 'kts',
  'swift', 'dart', 'c', 'h', 'cc', 'cpp', 'hpp', 'cs', 'fs', 'php', 'scala',
  'groovy', 'gradle', 'proto', 'graphql', 'gql', 'ex', 'exs', 'erl', 'clj',
  'vim', 'bat', 'cmd', 'tex', 'asm', 's', 'mod', 'sum',
])

/** Lower-cased extension without the dot, or '' when the name has none. */
export function extOf(path: string): string {
  const name = baseName(path)
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : ''
}

/** Last segment of either a POSIX or a Windows path. */
export function baseName(path: string): string {
  const parts = path.split(/[\\/]/)
  return parts[parts.length - 1] || path
}

export function isDocFile(path: string): boolean {
  const ext = extOf(path)
  return ext === 'md' || ext === 'markdown'
}

export function sourceLangOf(path: string): SourceLang {
  const ext = extOf(path)
  const lang: SourceLang = {
    kind: 'plain',
    hashComments: false,
    dashComments: false,
  }
  if (ext === 'md' || ext === 'markdown') lang.kind = 'markdown'
  else if (MARKUP_EXTS.has(ext)) lang.kind = 'markup'
  else if (STYLE_EXTS.has(ext)) lang.kind = 'style'
  else if (DATA_EXTS.has(ext)) lang.kind = 'data'
  else if (CODE_EXTS.has(ext) || HASH_COMMENT_EXTS.has(ext) || DASH_COMMENT_EXTS.has(ext)) lang.kind = 'code'
  lang.hashComments = HASH_COMMENT_EXTS.has(ext)
  lang.dashComments = DASH_COMMENT_EXTS.has(ext)
  return lang
}

/* ---- Markdown documents ------------------------------------------------- */

export interface DocHeading {
  level: number
  text: string
}

const RE_ATX = /^(#{1,6})\s+(.+?)\s*#*\s*$/
const RE_FENCE = /^\s*(`{3,}|~{3,})/
const RE_LINK = /(!?)\[([^\]\n]*)\]\(([^)\s]+)(?:\s+[^)]*)?\)/g

/** Strip the inline decoration from a heading so the outline reads as text. */
function plainInline(s: string): string {
  return s
    .replace(RE_LINK, (_all, _bang, label: string) => label)
    .replace(/[`*_~]/g, '')
    .trim()
}

/** Headings outside fenced code, in document order. The renderer emits one
 *  heading element per ATX line, so an index into this list is also an index
 *  into the rendered headings — that is all the outline needs to scroll. */
export function docHeadings(src: string, limit = 60): DocHeading[] {
  const out: DocHeading[] = []
  let fence = ''
  for (const raw of src.split('\n')) {
    const line = raw.replace(/\r$/, '')
    const f = RE_FENCE.exec(line)
    if (f) {
      const mark = f[1]?.[0] ?? '`'
      if (!fence) fence = mark
      else if (fence === mark) fence = ''
      continue
    }
    if (fence) continue
    const h = RE_ATX.exec(line)
    if (h) out.push({ level: (h[1] ?? '#').length, text: plainInline(h[2] ?? '') })
    if (out.length >= limit) break
  }
  return out
}

/** GitHub-style anchor text, kept loose so CJK headings still match. */
export function slugOf(text: string): string {
  return text
    .toLowerCase()
    .replace(/[`*_~\[\]()#.,!?:;'"/\\|]/g, '')
    .trim()
    .replace(/\s+/g, '-')
}

/** Rewrite links so the chat renderer can carry them.
 *
 *  Absolute URLs, mailto and in-document `#anchors` are left alone. A relative
 *  target becomes `#file:<encoded>`; `![alt](a.png)` becomes
 *  `#image:<encoded>`. The viewer decodes the target and resolves it against
 *  the file's own directory. Fenced code and inline code spans are untouched. */
export function prepareDocText(src: string): string {
  let fence = ''
  return src
    .split('\n')
    .map((raw) => {
      const line = raw.replace(/\r$/, '')
      const f = RE_FENCE.exec(line)
      if (f) {
        const mark = f[1]?.[0] ?? '`'
        if (!fence) fence = mark
        else if (fence === mark) fence = ''
        return raw
      }
      if (fence) return raw
      return rewriteLinks(line)
    })
    .join('\n')
}

function rewriteLinks(line: string): string {
  let out = ''
  let i = 0
  while (i < line.length) {
    if (line[i] === '`') {
      let run = 1
      while (line[i + run] === '`') run++
      const marker = '`'.repeat(run)
      const close = line.indexOf(marker, i + run)
      if (close === -1) {
        out += line.slice(i)
        break
      }
      out += line.slice(i, close + run)
      i = close + run
      continue
    }
    let j = i
    while (j < line.length && line[j] !== '`') j++
    out += line.slice(i, j).replace(RE_LINK, (all, bang: string, label: string, target: string) => {
      // Already a real destination (or a link we rewrote on an earlier pass).
      if (/^(?:https?:\/\/|mailto:|#)/i.test(target)) return all
      const prefix = bang ? '#image:' : '#file:'
      const text = label || (bang ? baseName(target) : target)
      return `[${text}](${prefix}${encodeURIComponent(target)})`
    })
    i = j
  }
  return out
}

/** Resolve a link target against the file that owns it. Returns a path the
 *  /api/fs/file endpoint accepts (absolute where the viewer had one, relative
 *  to the session worktree otherwise) plus an optional `:line` focus. */
export function resolveDocPath(basePath: string, href: string): { path: string; line: number } | null {
  let target = href.trim()
  if (!target) return null
  let line = 0
  const m = /^(.*):(\d{1,7})$/.exec(target)
  if (m && m[1]) {
    target = m[1]
    line = Number(m[2])
  }
  // A URL is somebody else's document; an empty target is a pure #anchor.
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(target)) return null
  target = target.split('#')[0] ?? ''
  if (!target) return null

  const base = basePath.replace(/\\/g, '/')
  const absolute = target.startsWith('/') || /^[a-zA-Z]:\//.test(target)
  const dir = base.slice(0, base.lastIndexOf('/') + 1)
  const combined = absolute ? target : dir + target
  const parts: string[] = []
  for (const part of combined.split('/')) {
    if (!part || part === '.') continue
    if (part === '..') {
      // Never walk above a Windows drive letter.
      if (parts.length > 1 || (parts.length === 1 && !/^[a-zA-Z]:$/.test(parts[0] ?? ''))) parts.pop()
      continue
    }
    parts.push(part)
  }
  const path = (combined.startsWith('/') && !/^[a-zA-Z]:\//.test(combined) ? '/' : '') + parts.join('/')
  return path ? { path, line } : null
}

/* ---- Source colouring --------------------------------------------------- */

const KEYWORDS = new Set([
  // Go / C family / JS / TS / Rust / Java / Kotlin / Swift / Dart
  'as', 'async', 'await', 'break', 'case', 'catch', 'class', 'const', 'continue',
  'default', 'defer', 'delete', 'do', 'else', 'enum', 'export', 'extends',
  'extern', 'final', 'finally', 'fn', 'for', 'foreach', 'from', 'func',
  'function', 'go', 'goto', 'if', 'implements', 'import', 'in', 'instanceof',
  'interface', 'is', 'lambda', 'let', 'match', 'mut', 'namespace', 'new',
  'operator', 'package', 'override', 'private', 'protected', 'pub', 'public',
  'raise', 'readonly', 'ref', 'return', 'select', 'sizeof', 'static', 'struct',
  'switch', 'throw', 'throws', 'trait', 'try', 'type', 'typedef', 'typeof',
  'union', 'unless', 'unsafe', 'use', 'using', 'var', 'virtual', 'void',
  'volatile', 'while', 'with', 'yield',
  // Python / Ruby / shell / SQL
  'and', 'assert', 'begin', 'def', 'del', 'elif', 'end', 'except', 'exec',
  'fi', 'global', 'local', 'nonlocal', 'not', 'or', 'pass', 'print', 'require',
  'then', 'until', 'when', 'INSERT', 'SELECT', 'UPDATE', 'DELETE', 'FROM',
  'WHERE', 'JOIN', 'GROUP', 'ORDER', 'BY', 'LIMIT', 'CREATE', 'TABLE', 'NULL',
])
const LITERALS = new Set([
  'true', 'false', 'null', 'nil', 'none', 'undefined', 'nan', 'infinity',
  'self', 'this', 'super', 'it', 'void',
])

function tokenSink(): { out: Token[]; push(text: string, kind: TokenKind): void } {
  const out: Token[] = []
  return {
    out,
    push(text, kind) {
      if (!text) return
      const last = out[out.length - 1]
      if (last && last.kind === kind) last.text += text
      else out.push({ text, kind })
    },
  }
}

/** Colour a window of source lines. Block-comment state carries across lines;
 *  a Markdown fence carries across lines too. Every token keeps
 *  its original text, so joining them reproduces the file byte for byte. */
export function highlightLines(lines: string[], lang: SourceLang): Token[][] {
  let inBlock = false
  let fence = ''
  const blockStart = lang.kind === 'markup' ? '<!--' : '/*'
  const blockEnd = lang.kind === 'markup' ? '-->' : '*/'

  return lines.map((raw) => {
    const line = raw.replace(/\r$/, '')
    if (lang.kind === 'plain') return [{ text: raw, kind: 'plain' }]

    if (lang.kind === 'markdown') {
      const f = RE_FENCE.exec(line)
      if (f) {
        const mark = f[1]?.[0] ?? '`'
        if (!fence) fence = mark
        else if (fence === mark) fence = ''
        return [{ text: raw, kind: 'marker' }]
      }
      if (fence) return [{ text: raw, kind: 'string' }]
      return markdownLine(raw)
    }

    const { out, push } = tokenSink()
    let i = 0
    let inTag = false
    let expectTag = false
    while (i < line.length) {
      if (inBlock) {
        const end = line.indexOf(blockEnd, i)
        if (end === -1) return out.concat([{ text: line.slice(i), kind: 'comment' }])
        push(line.slice(i, end + blockEnd.length), 'comment')
        i = end + blockEnd.length
        inBlock = false
        continue
      }
      const c = line[i] ?? ''
      if (line.startsWith(blockStart, i) && (lang.kind !== 'data' || lang.hashComments)) {
        const end = line.indexOf(blockEnd, i + blockStart.length)
        if (end === -1) {
          push(line.slice(i), 'comment')
          inBlock = true
          return out
        }
        push(line.slice(i, end + blockEnd.length), 'comment')
        i = end + blockEnd.length
        continue
      }
      if (lang.kind !== 'markup' && c === '/' && line[i + 1] === '/') {
        push(line.slice(i), 'comment')
        return out
      }
      if (lang.hashComments && c === '#') {
        push(line.slice(i), 'comment')
        return out
      }
      if (lang.dashComments && c === '-' && line[i + 1] === '-') {
        push(line.slice(i), 'comment')
        return out
      }

      if (c === '"' || c === "'" || c === '`') {
        const end = stringEnd(line, i, c)
        const text = line.slice(i, end)
        // A quoted key in JSON/YAML is a label, not a value.
        const after = /^\s*/.exec(line.slice(end))?.[0] ?? ''
        const isKey = lang.kind === 'data' && (line[end + after.length] ?? '') === ':'
        push(text, isKey ? 'key' : 'string')
        i = end
        continue
      }

      if (lang.kind === 'style' && c === '#') {
        const m = /^#[0-9a-fA-F]{3,8}\b/.exec(line.slice(i))
        if (m) {
          push(m[0] ?? '', 'number')
          i += (m[0] ?? '').length
          continue
        }
      }
      if (/\d/.test(c) && !/[\w$]/.test(line[i - 1] ?? '')) {
        const m = /^(?:0[xXbBoO][0-9a-fA-F_]+|\d[\d_]*(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(line.slice(i))
        if (m) {
          push(m[0] ?? '', 'number')
          i += (m[0] ?? '').length
          continue
        }
      }

      const id = /^[A-Za-z_$@][\w$-]*/.exec(line.slice(i))
      if (id) {
        const word = id[0] ?? ''
        const rest = line.slice(i + word.length)
        const spaces = /^\s*/.exec(rest)?.[0] ?? ''
        const next = rest[spaces.length] ?? ''
        let kind: TokenKind = 'plain'
        // Structure wins over vocabulary: `class` is both a JS keyword and an
        // HTML attribute, and `type` is both a Go keyword and a YAML key.
        if (lang.kind === 'markup' && inTag) {
          if (expectTag) {
            kind = 'tag'
            expectTag = false
          } else if (next === '=') kind = 'attr'
        } else if ((lang.kind === 'data' || lang.kind === 'style') && (next === ':' || next === '=')) {
          kind = 'key'
        } else if (LITERALS.has(word.toLowerCase())) kind = 'number'
        else if (KEYWORDS.has(word) || KEYWORDS.has(word.toUpperCase())) kind = 'keyword'
        else if (lang.kind === 'code' && /^[A-Z]/.test(word) && word.length > 1) {
          kind = 'type'
        }
        push(word, kind)
        i += word.length
        continue
      }

      if (lang.kind === 'markup' && c === '<') {
        inTag = true
        expectTag = true
        push(c, 'marker')
        i++
        continue
      }
      if (lang.kind === 'markup' && c === '>') {
        inTag = false
        push(c, 'marker')
        i++
        continue
      }
      push(c, 'plain')
      i++
    }
    return out
  })
}

function stringEnd(line: string, start: number, quote: string): number {
  let i = start + 1
  while (i < line.length) {
    const c = line[i] ?? ''
    if (c === '\\') {
      i += 2
      continue
    }
    if (c === quote) return i + 1
    i++
  }
  return line.length
}

/** Markdown source view: structural markers first, then inline decoration. */
function markdownLine(raw: string): Token[] {
  const line = raw.replace(/\r$/, '')
  const { out, push } = tokenSink()
  const h = /^(\s*#{1,6})(\s+)(.*)$/.exec(line)
  if (h) {
    push(h[1] ?? '', 'marker')
    push(h[2] ?? '', 'plain')
    push(h[3] ?? '', 'tag')
    return out
  }
  if (/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
    push(line, 'marker')
    return out
  }
  const quote = /^(\s*>\s?)(.*)$/.exec(line)
  if (quote) {
    push(quote[1] ?? '', 'marker')
    return inlineTokens(quote[2] ?? '', 'plain', out, push)
  }
  const item = /^(\s*(?:[-*+]|\d{1,9}[.)])\s+)(.*)$/.exec(line)
  if (item) {
    push(item[1] ?? '', 'marker')
    return inlineTokens(item[2] ?? '', 'plain', out, push)
  }
  if (/^\s*\|/.test(line)) {
    // Pipes are structure; the cells keep their inline decoration.
    const cells = line.split('|')
    for (let i = 0; i < cells.length; i++) {
      if (i > 0) push('|', 'marker')
      inlineTokens(cells[i] ?? '', 'plain', out, push)
    }
    return out
  }
  return inlineTokens(line, 'plain', out, push)
}

type Push = (text: string, kind: TokenKind) => void

function inlineTokens(text: string, base: TokenKind, out: Token[], push: Push): Token[] {
  let i = 0
  while (i < text.length) {
    const c = text[i] ?? ''
    if (c === '`') {
      const close = text.indexOf('`', i + 1)
      const end = close === -1 ? text.length : close + 1
      push(text.slice(i, end), 'string')
      i = end
      continue
    }
    if (c === '!' && text[i + 1] === '[') {
      const end = linkEnd(text, i + 1)
      if (end !== -1) {
        push('!', 'marker')
        pushLink(text.slice(i + 1, end), push)
        i = end
        continue
      }
    }
    if (c === '[') {
      const end = linkEnd(text, i)
      if (end !== -1) {
        pushLink(text.slice(i, end), push)
        i = end
        continue
      }
    }
    const mark = text.startsWith('**', i) ? '**' : text.startsWith('~~', i) ? '~~' : c === '*' || c === '_' ? c : ''
    if (mark) {
      const close = text.indexOf(mark, i + mark.length)
      if (close > i + mark.length) {
        push(mark, 'marker')
        push(text.slice(i + mark.length, close), 'keyword')
        push(mark, 'marker')
        i = close + mark.length
        continue
      }
    }
    push(c, base)
    i++
  }
  return out
}

function linkEnd(text: string, open: number): number {
  const close = text.indexOf(']', open + 1)
  if (close === -1 || text[close + 1] !== '(') return -1
  const end = text.indexOf(')', close + 2)
  return end === -1 ? -1 : end + 1
}

function pushLink(span: string, push: Push): void {
  const close = span.indexOf(']')
  push('[', 'marker')
  push(span.slice(1, close), 'key')
  push(']', 'marker')
  push('(', 'marker')
  push(span.slice(close + 2, -1), 'comment')
  push(')', 'marker')
}
