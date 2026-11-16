// SPDX-License-Identifier: AGPL-3.0-or-later
import { useEffect, useRef, useState } from 'preact/hooks'
import { Markdown } from '../md/render'
import { t } from '../i18n'
import { Icon, type IconName } from './icons'
import { ImagePreview } from './image-preview'
import { MessageCopyButton } from './copy'
import { useModalFocus } from './modal-focus'
import type { ExecutionStep } from './execution-steps'
import { expansion, rememberExpansion } from './execution-expansion'

export function ExecutionSteps({ steps, className = '', scope }: { steps: ExecutionStep[]; className?: string; scope?: string }) {
 const [toggle, setToggle] = useState(() => expansion(scope))
 useEffect(() => { setToggle(expansion(scope)) }, [scope])
 if (!steps.length) return null
 return <section class={'execution-steps ' + className} aria-label={t('sessions.processTitle')}>
  <div class="execution-toolbar">
   <span>{t('sessions.processTitle')} <span class="dim">{steps.length}</span></span>
   <span class="grow" />
   <button type="button" onClick={() => setToggle(rememberExpansion(scope, { open: true, rows: {} }))}>{t('activity.expandAll')}</button>
   <button type="button" onClick={() => setToggle(rememberExpansion(scope, { open: false, rows: {} }))}>{t('activity.collapseAll')}</button>
  </div>
  <ol class="execution-list">{steps.map(step => <ExecutionRow key={step.id} step={step} open={toggle.rows[step.id] ?? toggle.open}
    onToggle={open => setToggle(v => rememberExpansion(scope, { ...v, rows: { ...v.rows, [step.id]: open } }))} />)}</ol>
 </section>
}

function ExecutionRow({ step, open, onToggle }: { step: ExecutionStep; open: boolean; onToggle(open: boolean): void }) {
 const [large, setLarge] = useState(false)
 const enlargeButton = useRef<HTMLButtonElement>(null)
 const icon: IconName = step.status === 'running' ? 'refresh' : step.status === 'success' ? 'check' :
  step.status === 'failed' || step.status === 'refused' ? 'alert' : step.status === 'cancelled' ? 'x' : step.thought ? 'lightbulb' : 'code'
 const label = step.status === 'info' ? '' : t('activity.status.' + step.status)
 return <li class={'execution-row is-' + step.status}>
  <button type="button" class="execution-heading" aria-expanded={open} onClick={() => onToggle(!open)}>
   <Icon name={icon} size={15} class="execution-status-icon" />
   <span class="execution-title">{step.title}</span>
   {label && <span class="execution-status">{label}</span>}
   {step.executor && <span class="execution-node">{step.executor}</span>}
   {step.ts > 0 && <time class="execution-time">{new Date(step.ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</time>}
   <Icon name={open ? 'chevron-down' : 'chevron-right'} size={13} />
  </button>
  {open && <div class="execution-body">
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

function StepContent({ step }: { step: ExecutionStep }) {
 return <>
  <ToolInput input={step.input} />
  {step.output && (step.thought || step.prose ? <div class="execution-thought"><Markdown text={step.output} /></div> : <pre class="execution-output" tabIndex={0}>{step.output}</pre>)}
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

