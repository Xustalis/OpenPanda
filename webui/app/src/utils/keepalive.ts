/**
 * Keeps the browser tab and page execution active during long-running tasks:
 * 1. Requests a screen WakeLock so the display and OS do not sleep.
 * 2. Uses an inaudible Web Audio oscillator (gain 0.00001) to prevent macOS
 *    App Nap and Chrome/Safari background tab throttling/suspension when
 *    switching tabs, minimizing the window, or losing focus.
 * 3. Handles visibilitychange to re-acquire wakeLock when returning to foreground.
 */

export interface KeepAliveHandle {
  release(): void
}

export function acquireKeepAlive(): KeepAliveHandle {
  let released = false
  let wakeLock: any = null
  let audioCtx: AudioContext | null = null
  let osc: OscillatorNode | null = null
  let gain: GainNode | null = null

  const requestWakeLock = async () => {
    if (released) return
    if (typeof navigator !== 'undefined' && 'wakeLock' in navigator) {
      try {
        wakeLock = await (navigator as any).wakeLock.request('screen')
      } catch {
        // Ignored: wake lock not granted or unsupported
      }
    }
  }

  // 1. Screen Wake Lock
  void requestWakeLock()

  const onVisibilityChange = () => {
    if (typeof document !== 'undefined' && document.visibilityState === 'visible' && !released) {
      void requestWakeLock()
    }
  }
  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', onVisibilityChange)
  }

  // 2. Silent Web Audio oscillator:
  // macOS App Nap and Chromium background throttling strictly exempt tabs with an active AudioContext.
  if (typeof window !== 'undefined') {
    try {
      const AudioCtx = window.AudioContext || (window as any).webkitAudioContext
      if (AudioCtx) {
        audioCtx = new AudioCtx()
        gain = audioCtx.createGain()
        gain.gain.value = 0.00001 // inaudible
        gain.connect(audioCtx.destination)
        osc = audioCtx.createOscillator()
        osc.frequency.value = 440
        osc.connect(gain)
        osc.start()
      }
    } catch {
      // Web Audio may not be available or permitted
    }
  }

  return {
    release() {
      if (released) return
      released = true
      if (typeof document !== 'undefined') {
        document.removeEventListener('visibilitychange', onVisibilityChange)
      }
      try {
        wakeLock?.release?.()
      } catch {}
      wakeLock = null

      try {
        osc?.stop?.()
        osc?.disconnect?.()
      } catch {}
      try {
        gain?.disconnect?.()
      } catch {}
      try {
        audioCtx?.close?.()
      } catch {}
      osc = null
      gain = null
      audioCtx = null
    },
  }
}
