// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useState } from 'preact/hooks'
import type { Task } from '../api/client'
import { t } from '../i18n'
import { prettifyJson } from '../format/json'
import { Markdown } from '../md/render'
import { TaskPanel } from './task-panel'
import { parseResult, text } from './task-sections'
import { deliveryText } from './task-delivery'
import { ArtifactSection } from './artifact'

const OPEN_STATES = ['submitted', 'queued', 'dispatched', 'running', 'waiting_context']

/** Preview height: about seven lines of the result's own type size. */
const OUTPUT_CLAMP_PX = 150
/** Expanded, the body scrolls past this — the page keeps its shape. */
const OUTPUT_OPEN_PX = 520

export function OutputPanel({ task }: { task: Task }) {
  const [rawOpen, setRawOpen] = useState(false)
  const raw = task.result ?? ''
  const r = parseResult(raw)
  const artifact = task.plan_meta?.output_artifact ? <ArtifactSection key={task.plan_meta.output_artifact} task={task} /> : null

  // A result that is not a JSON object is shown verbatim — a half-understood
  // blob beats a blank card. It has no chips to lift out, so the card is the
  // text and a copy button.
  if (!r) {
    const body = raw.trim()
    if (!body) return <EmptyOutput task={task} footer={artifact} />
    return (
      <TaskPanel
        title={t('sessions.round.report')}
        wide
        hint={t('detail.result.chars', { n: body.length.toLocaleString() })}
        actions={<CopyButton text={body} />}
        clamp={OUTPUT_CLAMP_PX}
        openMax={OUTPUT_OPEN_PX}
        foldable
        footer={artifact}
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
  const report = deliveryText(raw)
  const stderr = text(r.stderr).trim()
  const diagnostic = stderr === report ? '' : stderr
  const changed = task.files_changed ?? r.files_changed
  const files = Array.isArray(changed)
    ? changed
        .filter((f): f is string => typeof f === 'string' && f !== '')
        .filter((f) => !FILE_NOISE.test(f))
    : []

  // Process exit is metadata, not proof the deliverable was verified.
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

  // A change set of thirty-six files is a fact, not a list: the card's face
  // names the first few and counts the rest, and the full list closes the body
  // (opening it, the paths filled the whole preview and the prose — the thing
  // the card exists for — never showed).
  const filesOnFace = files.slice(0, FILE_FACE_CAP)
  const filesBehind = files.length - filesOnFace.length

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
      title={t('sessions.round.report')}
      wide
      hint={report ? t('detail.result.chars', { n: report.length.toLocaleString() }) : undefined}
      actions={
        <>
          {report ? <CopyButton text={report} /> : null}
          <button
            type="button"
            class="dp-action"
            aria-expanded={rawOpen}
            onClick={() => setRawOpen((v) => !v)}
          >
            {t('detail.result.raw')}
          </button>
        </>
      }
      lead={leadLine}
      clamp={OUTPUT_CLAMP_PX}
      openMax={OUTPUT_OPEN_PX}
      empty={t(OPEN_STATES.includes(task.state) ? 'detail.output.pending' : 'detail.output.empty')}
      footer={<>{artifact}{rawOpen && <pre class="detail-raw detail-raw-capped" tabIndex={0}>{prettifyJson(raw)}</pre>}</>}
      foldable
    >
      {report ? <Markdown text={report} /> : null}
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
      {diagnostic ? (
        <div class="dp-sub">
          <span class="dp-sub-label">{t('detail.result.stderr')}</span>
          <pre class="result-err">{diagnostic}</pre>
        </div>
      ) : null}
    </TaskPanel>
  )
}

function EmptyOutput({ task, footer }: { task: Task; footer?: ComponentChildren }) {
  const running = OPEN_STATES.includes(task.state)
  return (
    <TaskPanel
      title={t('sessions.round.report')}
      wide
      empty={running ? t('detail.output.pending') : t('detail.output.empty')}
      footer={footer}
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
