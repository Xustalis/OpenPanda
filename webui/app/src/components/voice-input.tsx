// SPDX-License-Identifier: AGPL-3.0-or-later

// The composer's dictation button: a microphone between the model switch and
// Send.
//
// Two recognisers, because neither one covers the installs this console runs
// on:
//
//  1. The browser's own (`SpeechRecognition` / `webkitSpeechRecognition`).
//     Zero setup, live, and it needs no round-trip — but only Chromium ships
//     it, and in a lot of networks the service behind it is simply not
//     reachable, which surfaces as an `error` of `network` and nothing else.
//  2. Record here, transcribe on the node (`/api/voice/transcribe`, an
//     OpenAI-compatible Whisper endpoint). Works in every browser that can
//     open a microphone, and depends on the asr section in config.yaml.
//
// The button prefers (1) and drops to (2) the first time (1) says `network` or
// `service-not-allowed` — the failure a user would otherwise read as "the mic
// is broken". That verdict is remembered in localStorage, so a blocked
// recogniser costs one failed press ever, not one per page load.
//
// (2) streams. Uploading the clip only after the take ends made the whole
// feature feel broken: you spoke a sentence, watched nothing happen, pressed
// stop, and waited again. The microphone is now tapped as raw PCM and the
// audio *so far* is re-transcribed every second or so, so words appear while
// you are still talking. Each pass sends the cumulative audio (not a window),
// because a window would need the overlap stitched back together — and a
// transcript that grows over itself is both simpler and more accurate than one
// assembled from pieces. The pass is self-clocked: the next request waits for
// the previous answer, so a slow vendor produces fewer requests instead of a
// pile of them. Pressing stop sends the complete take once more for the final
// text.
//
// PCM capture rather than MediaRecorder, for one reason: a webm/opus chunk cut
// out of the middle of a stream has no header and no decoder will take it, so
// it cannot be transcribed piecewise. Raw samples can. The tap is a
// ScriptProcessorNode rather than an AudioWorklet because the panel's CSP pins
// `script-src`/`worker-src` to 'self' — a worklet has to be loaded from a URL,
// and the only URL available without adding a file to the build is a blob:,
// which that policy refuses.

import { useEffect, useRef, useState } from 'preact/hooks'
import { api, ApiError } from '../api/client'
import { locale, t } from '../i18n'
import { Icon } from './icons'
import { toast } from './toast'
import { elapsedLabel, encodeWav, mergeChunks, resample, TARGET_RATE } from './voice-pcm'

// ---- The Web Speech API's shape -------------------------------------------
//
// `SpeechRecognition` is not in lib.dom (it never became a standard), so the
// pieces used here are declared locally rather than pulled in as `any`.

interface SpeechAlternative {
  transcript: string
}
interface SpeechResult {
  readonly length: number
  isFinal: boolean
  [index: number]: SpeechAlternative
}
interface SpeechResultList {
  readonly length: number
  [index: number]: SpeechResult
}
interface SpeechResultEvent extends Event {
  resultIndex: number
  results: SpeechResultList
}
interface SpeechErrorEvent extends Event {
  error?: string
}
interface SpeechRecognitionLike extends EventTarget {
  lang: string
  continuous: boolean
  interimResults: boolean
  maxAlternatives: number
  start(): void
  stop(): void
  abort(): void
  onresult: ((e: SpeechResultEvent) => void) | null
  onerror: ((e: SpeechErrorEvent) => void) | null
  onend: (() => void) | null
}
type SpeechCtor = new () => SpeechRecognitionLike

function speechCtor(): SpeechCtor | null {
  if (typeof window === 'undefined') return null
  const w = window as unknown as {
    SpeechRecognition?: SpeechCtor
    webkitSpeechRecognition?: SpeechCtor
  }
  return w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null
}

/** The longest take one press will record. A microphone left open by accident
 *  must not become a long upload; a minute is well past any single prompt. */
const MAX_CLIP_MS = 60_000

/** How much new audio earns another transcription pass. Below ~0.8s the vendor
 *  spends more time on fixed overhead than on the words. */
const PUMP_STEP_S = 0.9

/** Cumulative audio past this stops the live passes. Each pass re-sends
 *  everything, so the cost grows with the square of the take; past half a
 *  minute the live text is not worth it and the final pass covers the rest. */
const PUMP_CAP_S = 30

/** Remembered so a browser whose recogniser is blocked pays for finding out
 *  once, not once per page load. */
const NO_BROWSER_STT = 'openpanda.voice.noBrowserStt'

type Phase = 'idle' | 'listening' | 'transcribing'

/** One line for the toast: the server's sentence when there is one (it names
 *  the vendor's real reason), the exception's message otherwise. */
function errText(err: unknown): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}

function readNoBrowserStt(): boolean {
  try {
    return localStorage.getItem(NO_BROWSER_STT) === '1'
  } catch {
    return false
  }
}

function rememberNoBrowserStt(): void {
  try {
    localStorage.setItem(NO_BROWSER_STT, '1')
  } catch {
    // storage unavailable — the flag just lives for this page
  }
}

/** The live microphone, held together in one object so teardown cannot miss a
 *  piece of it and leave a track open. */
interface Take {
  ctx: AudioContext
  src: MediaStreamAudioSourceNode
  proc: ScriptProcessorNode
  mute: GainNode
  stream: MediaStream
}

export function VoiceInput(props: {
  /** A finished transcript, to be inserted at the caret. */
  onText(text: string, final: boolean): void
  disabled?: boolean
}) {
  const [phase, setPhase] = useState<Phase>('idle')
  const [live, setLive] = useState('')
  const [startedAt, setStartedAt] = useState(0)
  const [now, setNow] = useState(0)
  const browser = useRef<SpeechRecognitionLike | null>(null)
  const take = useRef<Take | null>(null)
  // Which recogniser is live right now. `null` = nothing running, and it is the
  // only truth the callbacks consult: an `onend` that fires after a deliberate
  // stop must not drag the button back to life.
  const mode = useRef<'browser' | 'recorder' | null>(null)
  // The raw microphone samples, at the context's own rate.
  const pcm = useRef<Float32Array[]>([])
  const pcmLen = useRef(0)
  // Audio already covered by a live pass, in seconds — the next pass only goes
  // out once there is more than PUMP_STEP_S of new speech after it.
  const sentSec = useRef(0)
  // The newest interim transcript, kept so a failed final pass still has
  // something to commit instead of throwing the take away.
  const lastText = useRef('')
  // Set once the browser's recogniser has proven unreachable.
  const browserBroken = useRef(readNoBrowserStt())
  // Whether the current browser take produced any final text.
  const heard = useRef(false)
  // True while the permission prompt is still open — `getUserMedia` is async
  // and `mode` is not set until it resolves, so a second press in that window
  // would start a second capture.
  const pending = useRef(false)
  // Invalidates an in-flight `getUserMedia` or transcription pass when the take
  // is cancelled or replaced.
  const gen = useRef(0)
  const pumpTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const capTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const inflight = useRef(false)
  // One failure toast per take: a broken endpoint must say so once, not once
  // per second.
  const warned = useRef(false)

  useEffect(() => stopAll, [])

  // The live card's clock. Only this component re-renders on the tick, not the
  // thread behind it.
  useEffect(() => {
    if (phase !== 'listening') return
    const id = setInterval(() => setNow(Date.now()), 250)
    return () => clearInterval(id)
  }, [phase])

  // Escape abandons the take. Capture phase so it wins over anything else
  // listening on the document, and only while something is actually running.
  useEffect(() => {
    if (phase === 'idle') return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      cancel()
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [phase])

  function reset(): void {
    setLive('')
    setStartedAt(0)
    setNow(0)
    setPhase('idle')
  }

  /** Tear everything down without committing. Safe in any state; the unmount
   *  cleanup uses it, and it must not leave a microphone track live. */
  function stopAll(): void {
    gen.current++
    mode.current = null
    pending.current = false
    if (pumpTimer.current) clearTimeout(pumpTimer.current)
    pumpTimer.current = null
    if (capTimer.current) clearTimeout(capTimer.current)
    capTimer.current = null
    inflight.current = false
    lastText.current = ''
    pcm.current = []
    pcmLen.current = 0
    sentSec.current = 0

    const b = browser.current
    browser.current = null
    if (b) {
      b.onresult = null
      b.onerror = null
      b.onend = null
      try {
        b.abort()
      } catch {
        // already stopped
      }
    }
    const t0 = take.current
    take.current = null
    if (t0) {
      t0.proc.onaudioprocess = null
      try {
        t0.src.disconnect()
        t0.proc.disconnect()
        t0.mute.disconnect()
      } catch {
        // already disconnected
      }
      t0.stream.getTracks().forEach((tr) => tr.stop())
      void t0.ctx.close()
    }
    reset()
  }

  function cancel(): void {
    stopAll()
  }

  function toggle(): void {
    if (mode.current === 'browser') {
      // A second press ends the take and keeps what was heard: the recogniser
      // has already delivered every final phrase, so stopping loses nothing.
      const got = heard.current
      stopAll()
      if (!got) toast(t('sessions.voiceEmpty'))
      return
    }
    if (mode.current === 'recorder') {
      void commit()
      return
    }
    // Still waiting on the permission prompt: this press is a cancel.
    if (pending.current) {
      stopAll()
      return
    }
    const ctor = speechCtor()
    if (ctor && !browserBroken.current) startBrowser(ctor)
    else void startRecorder()
  }

  // ---- (1) the browser's recogniser ---------------------------------------

  function startBrowser(ctor: SpeechCtor): void {
    let rec: SpeechRecognitionLike
    try {
      rec = new ctor()
    } catch {
      markBrowserBroken()
      void startRecorder()
      return
    }
    rec.lang = locale()
    rec.continuous = true
    rec.interimResults = true
    rec.maxAlternatives = 1
    heard.current = false
    setLive('')
    setStartedAt(Date.now())

    rec.onresult = (e) => {
      let interim = ''
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const res = e.results[i]
        if (!res) continue
        const text = (res[0]?.transcript ?? '').trim()
        if (!text) continue
        if (res.isFinal) {
          // Final phrases commit straight into the composer, like typing.
          heard.current = true
          props.onText(text, true)
        } else {
          interim += (interim ? ' ' : '') + text
        }
      }
      setLive(interim)
    }
    rec.onerror = (e) => {
      const code = e.error ?? ''
      // Silence is not a failure — the engine just heard nothing this round.
      if (code === 'no-speech' || code === 'aborted') return
      if (code === 'network' || code === 'service-not-allowed' || code === 'language-not-supported') {
        // The recogniser is unreachable (blocked service, or a page that is
        // not a secure context). Fall through to recording rather than
        // reporting a broken microphone the user does not have.
        markBrowserBroken()
        mode.current = null
        const b = browser.current
        browser.current = null
        if (b) {
          b.onresult = null
          b.onerror = null
          b.onend = null
          try {
            b.abort()
          } catch {
            // already stopped
          }
        }
        toast(t('sessions.voiceFallback'))
        void startRecorder()
        return
      }
      stopAll()
      toast(t('sessions.voiceError', { err: code || 'error' }))
    }
    rec.onend = () => {
      // Only an *unrequested* end means the engine gave up; a stop() we asked
      // for has already cleared `mode`.
      if (mode.current !== 'browser') return
      const got = heard.current
      stopAll()
      // Chrome ends a continuous session on its own after a long silence. If
      // nothing was heard, that is the same silent nothing the stop button
      // guards against.
      if (!got) toast(t('sessions.voiceEmpty'))
    }

    try {
      rec.start()
    } catch {
      // A second start() on the same instance throws; treat it as unavailable
      // and let the recorder take over.
      markBrowserBroken()
      void startRecorder()
      return
    }
    browser.current = rec
    mode.current = 'browser'
    setPhase('listening')
  }

  function markBrowserBroken(): void {
    browserBroken.current = true
    rememberNoBrowserStt()
  }

  // ---- (2) tap the microphone, transcribe as it goes ----------------------

  async function startRecorder(): Promise<void> {
    if (typeof navigator === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
      // getUserMedia is gated on a secure context: plain http on a LAN address
      // has no microphone at all, and the message has to say why.
      toast(t('sessions.voiceInsecure'))
      return
    }
    const mine = ++gen.current
    pending.current = true
    setLive('')
    setStartedAt(Date.now())
    setPhase('listening')
    let media: MediaStream
    try {
      media = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: true, noiseSuppression: true },
      })
    } catch (err) {
      if (mine !== gen.current) return
      pending.current = false
      reset()
      toast(t('sessions.voiceNoMic', { err: errText(err) }))
      return
    }
    // Cancelled (or unmounted) while the prompt was open: hand the device
    // straight back instead of recording into a take nobody will send.
    if (mine !== gen.current) {
      media.getTracks().forEach((tr) => tr.stop())
      return
    }
    pending.current = false

    const Ctor =
      window.AudioContext ??
      (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
    if (!Ctor) {
      media.getTracks().forEach((tr) => tr.stop())
      reset()
      toast(t('sessions.voiceNoMic', { err: 'AudioContext unavailable' }))
      return
    }
    let ctx: AudioContext
    try {
      // Ask for the target rate so no resampling is needed at all; a browser
      // that refuses falls back to its own rate and `resample` covers it.
      ctx = new Ctor({ sampleRate: TARGET_RATE })
    } catch {
      ctx = new Ctor()
    }
    if (ctx.state === 'suspended') {
      try {
        await ctx.resume()
      } catch {
        // stays suspended; the graph simply produces nothing and the final
        // pass reports an empty take
      }
    }

    pcm.current = []
    pcmLen.current = 0
    sentSec.current = 0
    lastText.current = ''
    warned.current = false
    inflight.current = false

    const src = ctx.createMediaStreamSource(media)
    const proc = ctx.createScriptProcessor(4096, 1, 1)
    proc.onaudioprocess = (e) => {
      if (mine !== gen.current) return
      const ch = e.inputBuffer.getChannelData(0)
      pcm.current.push(new Float32Array(ch))
      pcmLen.current += ch.length
    }
    // A ScriptProcessor only runs while it reaches a destination; a zero gain
    // keeps that connection silent instead of playing the microphone back.
    const mute = ctx.createGain()
    mute.gain.value = 0
    src.connect(proc)
    proc.connect(mute)
    mute.connect(ctx.destination)

    take.current = { ctx, src, proc, mute, stream: media }
    mode.current = 'recorder'
    schedulePump(450)
    // The cap is a guard, not a feature: a forgotten microphone closes itself.
    capTimer.current = setTimeout(() => {
      if (mode.current === 'recorder') void commit()
    }, MAX_CLIP_MS)
  }

  /** The take so far, resampled to the encode rate. */
  function samplesSoFar(): Float32Array {
    const rate = take.current?.ctx.sampleRate ?? TARGET_RATE
    return resample(mergeChunks(pcm.current, pcmLen.current), rate, TARGET_RATE)
  }

  function clipSoFar(): Blob {
    return new Blob([encodeWav(samplesSoFar(), TARGET_RATE)], { type: 'audio/wav' })
  }

  function schedulePump(delay: number): void {
    if (pumpTimer.current) clearTimeout(pumpTimer.current)
    pumpTimer.current = setTimeout(() => void pump(), delay)
  }

  /** One live pass: send everything recorded so far, show what came back. The
   *  next pass is scheduled *after* this one answers, so a slow vendor yields
   *  fewer requests rather than a queue of them. */
  async function pump(): Promise<void> {
    pumpTimer.current = null
    if (mode.current !== 'recorder' || inflight.current) return
    const rate = take.current?.ctx.sampleRate ?? TARGET_RATE
    const secs = pcmLen.current / rate
    if (secs - sentSec.current < PUMP_STEP_S) {
      schedulePump(400)
      return
    }
    // Past the cap the live text stops improving for the price it costs; the
    // final pass still sends the whole take.
    if (secs > PUMP_CAP_S) return

    const mine = gen.current
    inflight.current = true
    let failed = false
    try {
      const { text } = await api.transcribeAudio(clipSoFar())
      if (mine !== gen.current) return
      sentSec.current = secs
      const trimmed = (text ?? '').trim()
      if (trimmed) {
        lastText.current = trimmed
        setLive(trimmed)
      }
    } catch (err) {
      if (mine !== gen.current) return
      failed = true
      if (!warned.current) {
        warned.current = true
        toast(t('sessions.voiceError', { err: errText(err) }))
      }
    }
    inflight.current = false
    if (!failed && mine === gen.current && mode.current === 'recorder') schedulePump(220)
  }

  /** End the take and send it once more, whole: the live passes race the
   *  microphone, so only a pass over the finished audio is the real answer. */
  async function commit(): Promise<void> {
    const samples = samplesSoFar()
    const fallback = lastText.current
    teardownCapture()
    gen.current++
    if (samples.length === 0) {
      reset()
      if (!fallback) toast(t('sessions.voiceEmpty'))
      else props.onText(fallback, true)
      return
    }
    setPhase('transcribing')
    try {
      const { text } = await api.transcribeAudio(
        new Blob([encodeWav(samples, TARGET_RATE)], { type: 'audio/wav' }),
      )
      const trimmed = (text ?? '').trim()
      if (trimmed) props.onText(trimmed, true)
      else if (fallback) props.onText(fallback, true)
      else toast(t('sessions.voiceEmpty'))
    } catch (err) {
      // The live pass already produced something readable — committing that is
      // better than throwing the take away over a failed last request.
      if (fallback) props.onText(fallback, true)
      else toast(t('sessions.voiceError', { err: errText(err) }))
    } finally {
      reset()
    }
  }

  /** Close the microphone and drop the capture graph, but keep the samples:
   *  `commit` reads them right after this. */
  function teardownCapture(): void {
    if (pumpTimer.current) clearTimeout(pumpTimer.current)
    pumpTimer.current = null
    if (capTimer.current) clearTimeout(capTimer.current)
    capTimer.current = null
    mode.current = null
    inflight.current = false
    const t0 = take.current
    take.current = null
    if (!t0) return
    t0.proc.onaudioprocess = null
    try {
      t0.src.disconnect()
      t0.proc.disconnect()
      t0.mute.disconnect()
    } catch {
      // already disconnected
    }
    t0.stream.getTracks().forEach((tr) => tr.stop())
    void t0.ctx.close()
  }

  const listening = phase === 'listening'
  const working = phase === 'transcribing'
  const tip = working
    ? t('sessions.voiceTranscribing')
    : listening
      ? t('sessions.voiceListening')
      : t('sessions.voiceBtn')
  // The card shows the tail: a dictation that runs past the card's height is
  // still readable, and the newest words are the ones worth the space. ~130
  // characters is about the six lines the card allows at its width.
  const shown = live.length > 130 ? `…${live.slice(-130)}` : live
  // The clock only runs while the microphone does; the transcribing state says
  // what it is doing in words instead.
  const clock = listening ? elapsedLabel(now - startedAt) : ''

  return (
    <div class="voice">
      <button
        type="button"
        class={`voice-input${listening ? ' on' : ''}${working ? ' working' : ''}`}
        /* The tip is suppressed while the live card is up: the card says the
           same sentence, in the same place the tip would appear. */
        data-tip={phase === 'idle' ? tip : undefined}
        aria-label={tip}
        aria-pressed={listening}
        disabled={props.disabled || working}
        onClick={toggle}
      >
        {working ? <span class="voice-spin" aria-hidden="true" /> : <Icon name="mic" size={16} />}
      </button>
      {phase !== 'idle' && (
        // A live card, not a spinner: the take is streaming, and the one thing
        // that makes that legible is the words arriving plus a clock that
        // proves the microphone is running before the first word does.
        <div class="voice-live" role="status" aria-live="polite">
          <div class="voice-live-head">
            <span class={`voice-live-dot${listening ? ' on' : ''}`} aria-hidden="true" />
            {clock && <span class="voice-live-clock">{clock}</span>}
            <span class="voice-live-hint">
              {working ? t('sessions.voiceTranscribing') : t('sessions.voiceListening')}
            </span>
          </div>
          <p class={`voice-live-text${shown ? '' : ' dim'}`}>
            {shown || t('sessions.voiceWaiting')}
          </p>
        </div>
      )}
    </div>
  )
}
