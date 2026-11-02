// SPDX-License-Identifier: AGPL-3.0-or-later

// File references in a transcript, and the surface that opens them.
//
// An agent names places, not documents: "the guard is in internal/bus/route.go",
// "see config.tsx:42". Those mentions are the transcript's links, and until now
// they were plain text — the reader had to copy the path and find the file
// themselves. This module recognises the shape and hands it to whatever viewer
// is mounted; the transcript renderer never learns where the file is shown.

import { createContext } from 'preact'
import { useContext } from 'preact/hooks'

/** A place in the workspace: a path, optionally with a focus line. */
export interface FileRef {
  path: string
  line: number
}

/** Opens a file reference. The console's drawer is the only implementation
 *  today; a future pane can provide its own without touching the renderer. */
export interface FileOpener {
  open(ref: FileRef): void
}

export const FileViewContext = createContext<FileOpener | null>(null)

export function useFileOpener(): FileOpener | null {
  return useContext(FileViewContext)
}

// Extensions specific enough that a bare "name.ext" with no slash is still a
// file. The list is deliberately closed rather than "anything with a dot":
// "fmt.Println", "1.5" and "v2.1.3" all carry dots and none of them is a path.
const FILE_EXTS = new Set([
  'go', 'ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs', 'py', 'rs', 'md', 'markdown',
  'json', 'jsonl', 'yaml', 'yml', 'toml', 'ini', 'cfg', 'conf', 'env',
  'sh', 'bash', 'zsh', 'ps1', 'bat',
  'css', 'scss', 'less', 'html', 'htm', 'svg',
  'sql', 'c', 'h', 'cc', 'cpp', 'hpp', 'cs', 'java', 'kt', 'swift', 'dart',
  'rb', 'php', 'lua', 'pl', 'r', 'scala', 'proto', 'graphql', 'vue', 'svelte',
  'txt', 'log', 'csv', 'mod', 'sum', 'lock',
])

// Characters that never appear in a bare path in prose. A quote, a bracket or a
// pipe means the span is something else (a shell pipeline, a type, a formula).
const NOT_A_PATH = /[`"'<>(){}|*?]/

// A trailing ":<digits>" is a line number — but only when what is left still
// looks like a path, and never for a Windows drive letter ("C:\src\a.go").
const LINE_SUFFIX = /^(.*):(\d+)$/

/**
 * parseFileRef recognises a file mention inside inline code. It answers null
 * for everything it is not sure about: a wrong link is worse than no link,
 * because it turns ordinary prose into something that fails when clicked.
 */
export function parseFileRef(raw: string): FileRef | null {
  const text = raw.trim()
  if (!text || text.length > 260) return null
  if (/\s/.test(text)) return null
  if (NOT_A_PATH.test(text)) return null
  // A URL is a link already; the drawer shows workspace files, not pages.
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(text)) return null

  let path = text
  let line = 0
  const m = LINE_SUFFIX.exec(text)
  if (m && m[1]) {
    path = m[1]
    line = Number(m[2])
  }
  if (!path) return null

  // "C:\src\a.go" — the drive colon is part of the path.
  const hasSlash = path.includes('/') || path.includes('\\')
  const dot = path.lastIndexOf('.')
  const ext = dot >= 0 ? path.slice(dot + 1).toLowerCase() : ''
  const knownExt = ext !== '' && FILE_EXTS.has(ext)
  if (!hasSlash && !knownExt) return null

  // A bare number with a decimal point ("1.5") reads as a version, not a file,
  // and a leading digit only ever shows up in paths that carry a slash.
  if (!hasSlash && /^\d/.test(path)) return null
  // A path that is nothing but a separator is not a target.
  if (/^[./\\]+$/.test(path)) return null

  return { path, line: line > 0 && line < 10_000_000 ? line : 0 }
}
