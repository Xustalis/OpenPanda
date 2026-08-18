import { useEffect, useRef, useState } from 'preact/hooks'
import { api, type AskResult } from '../api/client'
import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'

interface Turn {
  role: 'user' | 'panda'
  text: string
  result?: AskResult
}

/** The unified-entry chat: one prompt in, answer or task out — the same
 *  pipeline `panda ask` runs. Voice input uses the Web Speech API and
 *  degrades to hidden when unsupported. */
export function AskView({ onTaskCreated }: { onTaskCreated(id: string): void }) {
  useLocaleRerender()
  const [turns, setTurns] = useState<Turn[]>([])
  const [input, setInput] = useState('')
  const [authorize, setAuthorize] = useState(false)
  const [busy, setBusy] = useState(false)
  const [listening, setListening] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)
  const recog = useRef<SpeechRecognitionController | null>(null)

  const speechSupported =
    typeof window !== 'undefined' &&
    ('SpeechRecognition' in window || 'webkitSpeechRecognition' in window)

  useEffect(() => {
    scroller.current?.scrollTo({ top: scroller.current.scrollHeight })
  }, [turns])

  useEffect(() => () => recog.current?.stop(), [])

  function toggleVoice() {
    if (!speechSupported) return
    if (listening) {
      recog.current?.stop()
      setListening(false)
      return
    }
    const Ctor = window.SpeechRecognition ?? window.webkitSpeechRecognition
    if (!Ctor) return
    const r = new Ctor()
    r.lang = navigator.language || 'en-US'
    r.interimResults = false
    r.onresult = (e) => {
      const said = e.results[0]?.[0]?.transcript ?? ''
      setInput((prev) => (prev ? `${prev} ${said}` : said))
    }
    r.onend = () => setListening(false)
    r.onerror = () => setListening(false)
    recog.current = r
    r.start()
    setListening(true)
  }

  async function submit(e: Event) {
    e.preventDefault()
    const prompt = input.trim()
    if (!prompt || busy) return
    setBusy(true)
    setInput('')
    setTurns((ts) => [...ts, { role: 'user', text: prompt }])
    try {
      const result = await api.ask(prompt, authorize)
      setTurns((ts) => [...ts, { role: 'panda', text: renderResult(result), result }])
      if (result.kind === 'task' && result.task_id) onTaskCreated(result.task_id)
    } catch (err) {
      setTurns((ts) => [
        ...ts,
        { role: 'panda', text: `${t('common.error')} ${err instanceof Error ? err.message : ''}` },
      ])
    } finally {
      setBusy(false)
    }
  }

  return (
    <section class="ask">
      <h1 class="page-title">{t('nav.ask')}</h1>
      <p class="page-sub">{t('ask.subtitle')}</p>

      <div class="ask-log" ref={scroller}>
        {turns.length === 0 && <div class="card ask-hint">{t('ask.hint')}</div>}
        {turns.map((turn, i) => (
          <div key={i} class={`bubble ${turn.role}`}>
            {turn.role === 'panda' && turn.result?.kind === 'task' ? (
              <div>
                <p class="bubble-text">{turn.text}</p>
                {turn.result.task_id && (
                  <p class="dim mono">{turn.result.task_id}</p>
                )}
              </div>
            ) : (
              <p class="bubble-text">{turn.text}</p>
            )}
          </div>
        ))}
        {busy && <div class="bubble panda dim">{t('ask.thinking')}</div>}
      </div>

      <form class="ask-composer" onSubmit={submit}>
        <textarea
          class="input ask-input"
          rows={2}
          placeholder={t('ask.placeholder')}
          value={input}
          onInput={(e) => setInput((e.target as HTMLTextAreaElement).value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              submit(e)
            }
          }}
        />
        <div class="ask-actions">
          <label class="ask-authorize">
            <input
              type="checkbox"
              checked={authorize}
              onChange={(e) => setAuthorize((e.target as HTMLInputElement).checked)}
            />
            {t('ask.authorize')}
          </label>
          {speechSupported && (
            <button
              type="button"
              class={`btn${listening ? ' primary' : ''}`}
              onClick={toggleVoice}
              title={t('ask.voice')}
            >
              {listening ? '●' : '🎙'}
            </button>
          )}
          <button class="btn primary" type="submit" disabled={busy || !input.trim()}>
            {t('ask.send')}
          </button>
        </div>
      </form>
    </section>
  )
}

function renderResult(r: AskResult): string {
  if (r.kind === 'answer') return r.answer || ''
  const lines = [t('ask.taskCreated')]
  if (r.stdout) lines.push(r.stdout)
  if (r.stderr) lines.push(r.stderr)
  if (r.exit_code) lines.push(`exit ${r.exit_code}`)
  return lines.join('\n')
}
