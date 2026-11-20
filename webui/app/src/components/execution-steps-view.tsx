// SPDX-License-Identifier: AGPL-3.0-or-later
import { useEffect, useId, useRef, useState } from 'preact/hooks'
import type { OperationActivity } from '../api/client'
import { Markdown } from '../md/render'
import { t } from '../i18n'
import { Icon, type IconName } from './icons'
import { ImagePreview } from './image-preview'
import { MessageCopyButton } from './copy'
import { useModalFocus } from './modal-focus'
import { operationSteps, rowExpanded, type ExecutionStep } from './execution-steps'
import { expansion, rememberExpansion } from './execution-expansion'
import { readableOutput } from './output-text'

interface ExecutionStepsProps {
 steps: ExecutionStep[]
 className?: string
 scope?: string
 compact?: boolean
 hideTitle?: boolean
}

export function ReplyActivity({ thought, activities, scope }: {
 thought?: string; activities?: OperationActivity[]; scope: string
}) {
 const steps = operationSteps(activities ?? [])
 if (thought) steps.unshift({ id: 'reasoning', title: t('sessions.thought'), status: 'info', ts: 0, thought: true, output: thought })
 return <ExecutionSteps steps={steps} scope={scope} compact />
}

export function ExecutionSteps({ steps, className = '', scope, compact = false, hideTitle }: ExecutionStepsProps) {
 const [expanded, setExpanded] = useState(false)
 const contentId = useId()
 if (steps.length === 0) return null
 if (!compact) return <ExecutionList steps={steps} scope={scope} className={className} hideTitle={hideTitle} />

 const failed = steps.filter(step => step.status === 'failed' || step.status === 'refused').length
 return <div class={'execution-fold ' + className}>
  <button type="button" class="execution-fold-toggle" aria-expanded={expanded} aria-controls={contentId} onClick={() => setExpanded(value => !value)}>
   <Icon name="activity" size={15} />
   <span>{t('sessions.processTitle')}</span>
   <span class="execution-count">{steps.length}</span>
   {failed > 0 && <span class="execution-failures">{t('activity.failedCount', { n: String(failed) })}</span>}
   <Icon name={expanded ? 'chevron-down' : 'chevron-right'} size={14} class="execution-fold-caret" />
  </button>
  <div id={contentId} hidden={!expanded}>
   {expanded && <ExecutionList steps={steps} scope={scope} hideTitle />}
  </div>
 </div>
}

function ExecutionList({ steps, className = '', scope, hideTitle = false }: ExecutionStepsProps) {
 const [toggle, setToggle] = useState(() => expansion(scope))
 useEffect(() => { setToggle(expansion(scope)) }, [scope])
 return <section class={'execution-steps ' + className} aria-label={t('sessions.processTitle')}>
  {(!hideTitle || steps.length > 1) && <div class="execution-toolbar">
   {!hideTitle && <span>{t('sessions.processTitle')} <span class="execution-count">{steps.length}</span></span>}
   {steps.length > 1 && <div class="execution-toolbar-actions">
    <button type="button" onClick={() => setToggle(rememberExpansion(scope, { open: true, rows: {} }))}>{t('activity.expandAll')}</button>
    <button type="button" onClick={() => setToggle(rememberExpansion(scope, { open: false, rows: {} }))}>{t('activity.collapseAll')}</button>
   </div>}
  </div>}
  <ol class="execution-list">{steps.map(step => <ExecutionRow key={step.id} step={step} scope={scope} open={rowExpanded(step, toggle)}
    onToggle={open => setToggle(v => rememberExpansion(scope, { ...v, rows: { ...v.rows, [step.id]: open } }))} />)}</ol>
 </section>
}

function ExecutionRow({ step, open, scope, onToggle }: { step: ExecutionStep; open: boolean; scope?: string; onToggle(open: boolean): void }) {
 const [large, setLarge] = useState(false)
 // The chain of thought keeps its own latch: it is a note about the run, not
 // a step of it, so the batch toggle and the remembered expansion never carry
 // it open. It starts folded in every scope and only a click on this row
 // opens it — the same rule panda's reply and an agent's chain both follow.
 const [thoughtOpen, setThoughtOpen] = useState(false)
 useEffect(() => { setThoughtOpen(false) }, [scope])
 const expanded = step.thought ? thoughtOpen : open
 const enlargeButton = useRef<HTMLButtonElement>(null)
 const icon: IconName = step.status === 'running' ? 'refresh' : step.status === 'success' ? 'check' :
  step.status === 'failed' || step.status === 'refused' ? 'alert' : step.status === 'cancelled' ? 'x' :
  step.thought ? 'lightbulb' : step.prose ? 'message' : 'code'
 const label = step.status === 'info' ? '' : t('activity.status.' + step.status)
 return <li class={'execution-row is-' + step.status}>
  <button type="button" class="execution-heading" aria-expanded={expanded} onClick={() => step.thought ? setThoughtOpen(v => !v) : onToggle(!open)}>
   <Icon name={icon} size={15} class="execution-status-icon" />
   <span class="execution-title">{step.title}</span>
   <span class="execution-row-meta">
    {label && <span class="execution-status">{label}</span>}
    {step.executor && <span class="execution-node" title={step.executor}>{step.executor}</span>}
    {step.ts > 0 && <time class="execution-time">{new Date(step.ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</time>}
   </span>
   <Icon name={expanded ? 'chevron-down' : 'chevron-right'} size={13} class="execution-row-caret" />
  </button>
  {expanded && <div class="execution-body">
   <StepContent step={step} />
   <div class="execution-controls">
    <MessageCopyButton text={[step.title, step.input ? printable(step.input) : '', step.output].filter(Boolean).join('\n\n')} />
    <button ref={enlargeButton} type="button" class="execution-enlarge" onClick={() => setLarge(true)}><Icon name="external" size={13} />{t('activity.enlarge')}</button>
    {step.raw && <details class="execution-raw"><summary>{t('events.rawJson')}</summary><pre>{step.raw}</pre></details>}
   </div>
  </div>}
  {large && <EnlargedStep step={step} onClose={() => {
   setLarge(false)
   requestAnimationFrame(() => enlargeButton.current?.focus())
  }} />}
 </li>
}

function printable(value: unknown): string {
 return typeof value === 'string' ? value : JSON.stringify(value, null, 2) ?? String(value)
}

export function ToolInput({ input }: { input: unknown }) {
 if (input === null || input === undefined) return null
 if (typeof input !== 'object' || Array.isArray(input)) return <pre class="execution-command">{printable(input)}</pre>
 return <dl class="execution-input">{Object.entries(input as Record<string, unknown>).map(([key, value]) =>
  <div key={key}><dt>{key}</dt><dd><pre class={key === 'command' || key === 'cmd' ? 'execution-command' : ''}>{printable(value)}</pre></dd></div>)}</dl>
}

export function StepContent({ step }: { step: ExecutionStep }) {
 const output = readableOutput(step.output || '')
 return <>
  <ToolInput input={step.input} />
  {!step.image?.dataUrl && output.text && (step.thought || step.prose ? <div class="execution-thought"><Markdown text={output.text} /></div> : <pre class="execution-output" tabIndex={0}>{output.text}</pre>)}
  {output.dropped > 0 && <div class="output-note">{t('events.outputOmitted', { n: String(output.dropped) })}</div>}
  {step.image && <ImagePreview path={step.image.path} dataUrl={step.image.dataUrl} />}
  {step.truncated && <p class="execution-truncated" role="status">{t(step.unavailable ? 'work.unavailable' : 'events.transcriptTruncatedNote')}</p>}
 </>
}

function EnlargedStep({ step, onClose }: { step: ExecutionStep; onClose(): void }) {
 const dialog = useRef<HTMLDialogElement>(null)
 const body = useRef<HTMLDivElement>(null)
 useModalFocus(body, true)
 useEffect(() => { dialog.current?.showModal() }, [])
 return <dialog ref={dialog} class="execution-dialog" aria-label={step.title} onCancel={e => { e.preventDefault(); onClose() }}>
  <div ref={body}><div class="execution-dialog-head"><strong>{step.title}</strong><button type="button" aria-label={t('common.close')} onClick={onClose}><Icon name="x" /></button></div>
   <StepContent step={step} />
  </div>
 </dialog>
}

