// SPDX-License-Identifier: AGPL-3.0-or-later

// The task page's 输出 card: what the run actually produced.
//
// This used to live in `views/detail.tsx` as a full-width section that grew to
// whatever the agent wrote — 2,000 characters of markdown was 2,000 characters
// of page, and everything after it (the brief, the ids, the log) started below
// the fold. It is a card now: the facts that answer "did it work" sit on a
// always-visible line, the prose is clipped to a preview behind a fade, and
// expanding it scrolls inside the card instead of growing the page.

import { useState } from 'preact/hooks'
import type { Task } from '../api/client'
import { t } from '../i18n'
import { prettifyJson } from '../format/json'
import { Markdown } from '../md/render'
import { TaskPanel } from './task-panel'
import { parseResult, plainLine, text } from './task-sections'

const OPEN_STATES = ['submitted', 'queued', 'dispatched', 'running', 'waiting_context']

/** Preview height: about seven lines of the result's own type size. */
const OUTPUT_CLAMP_PX = 150
/** Expanded, the body scrolls past this — the page keeps its shape. */
const OUTPUT_OPEN_PX = 520

export function OutputPanel({ task, lead }: { task: Task; lead?: string }) {
  const [rawOpen, setRawOpen] = useState(false)
  const raw = task.result ?? ''
  const r = parseResult(raw)

  // A result that is not a JSON object is shown verbatim — a half-understood
  // blob beats a blank card. It has no chips to lift out, so the card is the
  // text and a copy button.
  if (!r) {
    const body = raw.trim()
    if (!body) return <EmptyOutput task={task} />
    return (
      <TaskPanel
        title={t('detail.result.stdout')}
        wide
        hint={t('detail.result.chars', { n: body.length.toLocaleString() })}
        actions={<CopyButton text={body} />}
        clamp={OUTPUT_CLAMP_PX}
        openMax={OUTPUT_OPEN_PX}
        foldable
      >
        <pre class="detail-raw">{body}</pre>
      </TaskPanel>
    )
  }

  const exit = typeof r.exit_code === 'number' ? r.exit_code : null
  const agent = text(r.agent)
  const model = text(r.model)
  const executor = text(r.executor)
  const injected = r.injected === true
  // The verdict block above already reads out the run's opening sentence (a
  // done run's verdict *is* the first line of its output). Dropping that one
  // line here is what stops the page saying the same sentence twice, ten
  // pixels apart.
  const stdout = stripLead(text(r.stdout), lead)
  const stderr = text(r.stderr)
  const files = Array.isArray(r.files_changed)
    ? r.files_changed
        .filter((f): f is string => typeof f === 'string' && f !== '')
        .filter((f) => !FILE_NOISE.test(f))
    : []

  // Chips rather than a sentence: a chip names its own field, and the exit
  // code carries the tone — a zero is the one fact that answers "did it work"
  // before anything is read.
  const chips: Array<{ text: string; tone?: 'ok' | 'bad'; mono?: boolean; agent?: boolean }> = []
  if (exit !== null) {
    chips.push({
      text: t('detail.result.exit', { n: String(exit) }),
      tone: exit === 0 ? 'ok' : 'bad',
      mono: true,
    })
  }
  if (agent) chips.push({ text: t('detail.result.agent', { agent }), agent: true })
  if (model) chips.push({ text: t('detail.result.model', { model }), mono: true })
  if (injected) chips.push({ text: t('detail.result.injected') })
  if (executor) chips.push({ text: t('detail.result.node', { node: executor }), mono: true })

  const bodyText = stdout + stderr
  // A result that carried nothing but its failure has already been read out
  // in the verdict block; a card titled 输出 with one grey line under it reads
  // as a broken panel.
  if (chips.length === 0 && files.length === 0 && !bodyText) return <EmptyOutput task={task} />

  // A change set of thirty-six files is a fact, not a list: the card's face
  // names the first few and counts the rest, and the full list closes the body
  // (opening it, the paths filled the whole preview and the prose — the thing
  // the card exists for — never showed).
  const filesOnFace = files.slice(0, FILE_FACE_CAP)
  const filesBehind = files.length - filesOnFace.length
  const copyText = stdout || stderr

  const leadLine = (
    <>
      {chips.length > 0 && (
        <div class="result-chips">
          {chips.map((c) => (
            <span
              key={c.text}
              class={`result-chip${c.tone ? ` is-${c.tone}` : ''}${c.mono ? ' mono' : ''}${c.agent ? ' is-agent' : ''}`}
              title={c.text}
            >
              {c.text}
            </span>
          ))}
        </div>
      )}
      {files.length > 0 && (
        <div class="result-files">
          <span class="dim result-files-label">
            {t('detail.result.files', { n: String(files.length) })}
          </span>
          <div class="result-file-list">
            {filesOnFace.map((f, i) => (
              <code key={i} class="result-file-chip" title={f}>
                {f}
              </code>
            ))}
            {filesBehind > 0 && (
              <span class="result-file-more">
                {t('detail.result.filesMore', { n: String(filesBehind) })}
              </span>
            )}
          </div>
        </div>
      )}
    </>
  )

  return (
    <TaskPanel
      title={t('detail.result.stdout')}
      wide
      hint={bodyText ? t('detail.result.chars', { n: bodyText.length.toLocaleString() }) : undefined}
      actions={
        <>
          {copyText ? <CopyButton text={copyText} /> : null}
          <button
            type="button"
            class="dp-action"
            aria-expanded={rawOpen}
            onClick={() => setRawOpen((v) => !v)}
          >
            {rawOpen ? t('events.collapse') : t('detail.result.raw')}
          </button>
        </>
      }
      lead={leadLine}
      clamp={OUTPUT_CLAMP_PX}
      openMax={OUTPUT_OPEN_PX}
      foldable
    >
      {stdout ? <Markdown text={stdout} /> : null}
      {/* The full list closes the body rather than opening it: at the top it
          filled the whole preview with paths, and the prose — the thing the
          card exists for — never showed. */}
      {filesBehind > 0 && (
        <div class="dp-sub">
          <span class="dp-sub-label">
            {t('detail.result.files', { n: String(files.length) })}
          </span>
          <FileChips files={files} />
        </div>
      )}
      {stderr ? (
        <div class="dp-sub">
          <span class="dp-sub-label">{t('detail.result.stderr')}</span>
          <pre class="result-err">{stderr}</pre>
        </div>
      ) : null}
      {rawOpen ? <pre class="detail-raw">{prettifyJson(raw)}</pre> : null}
    </TaskPanel>
  )
}

/** The card a run that has not produced anything yet gets. It stays on the
 *  page — an 输出 card that disappears leaves the reader hunting for where the
 *  answer would have been. */
function EmptyOutput({ task }: { task: Task }) {
  const running = OPEN_STATES.includes(task.state)
  return (
    <TaskPanel
      title={t('detail.result.stdout')}
      wide
      empty={running ? t('detail.output.pending') : t('detail.output.empty')}
    />
  )
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      // Clipboard denied (insecure origin, permission): the text is selectable
      // and a failed copy needs no error of its own.
    }
  }
  return (
    <button type="button" class="dp-action" onClick={copy} title={t('md.copy')}>
      {copied ? `✓ ${t('md.copied')}` : t('md.copy')}
    </button>
  )
}

/** Drops a leading sentence the page has already printed elsewhere — the
 *  verdict block quotes a done run's opening line, and the output card then
 *  opened on the same words. Markdown decoration is ignored on both sides
 *  (the verdict quotes the line without its `##`), exact prefix only, and only
 *  when a real body remains: trimming a 12-character remainder would leave the
 *  card emptier than it started. */
function stripLead(body: string, lead?: string): string {
  if (!lead) return body
  const trimmed = plainLine(body.trimStart())
  if (!trimmed.startsWith(lead)) return body
  const rest = trimmed.slice(lead.length).replace(/^[\s。.!！]+/, '')
  return rest.length > 40 ? rest : body
}

/** VCS internals, dependency trees, build output and agent-session scratch
 *  files a task's file list picks up along the way — real on disk, noise on
 *  the card. `.claude` is the adapter's own transcript/snapshot directory:
 *  one run listed thirteen of its files as "changed", none of them the
 *  user's. `.codebuddy` and `.codex` are the same thing under the other two
 *  CLIs — one task's "4 files changed" was three of their logs plus a
 *  switcher's sqlite. */
const FILE_NOISE =
  /(?:^|\/|\\)(?:\.git|\.svn|\.hg|node_modules|dist|build|__pycache__|\.next|\.cache|\.claude|\.codebuddy|\.codex)(?:\/|\\|$)|\.DS_Store$/

const FILE_CHIP_CAP = 12
/** How many file names the card's face carries before it counts the rest. */
const FILE_FACE_CAP = 3

/** Changed-file chips: capped so a large change set reads as a summary
 *  instead of a wall, with the rest one click away. */
function FileChips({ files }: { files: string[] }) {
  const [open, setOpen] = useState(false)
  const shown = open ? files : files.slice(0, FILE_CHIP_CAP)
  const hidden = files.length - shown.length
  return (
    <div class="result-file-list">
      {shown.map((f, i) => (
        <code key={i} class="result-file-chip" title={f}>
          {f}
        </code>
      ))}
      {hidden > 0 && (
        <button type="button" class="result-file-more" onClick={() => setOpen(true)}>
          {t('detail.result.filesMore', { n: String(hidden) })}
        </button>
      )}
    </div>
  )
}
