// Minimal Web Speech API typings — the DOM lib doesn't ship them. Only what
// the ask view uses; unsupported browsers never see the feature.

interface SpeechRecognitionResultItem {
  readonly transcript: string
}

interface SpeechRecognitionResult {
  readonly [index: number]: SpeechRecognitionResultItem
  readonly length: number
}

interface SpeechRecognitionEventLike extends Event {
  readonly results: {
    readonly [index: number]: SpeechRecognitionResult
    readonly length: number
  }
}

interface SpeechRecognitionController extends EventTarget {
  lang: string
  interimResults: boolean
  onresult: ((event: SpeechRecognitionEventLike) => void) | null
  onend: ((event: Event) => void) | null
  onerror: ((event: Event) => void) | null
  start(): void
  stop(): void
}

interface Window {
  SpeechRecognition?: new () => SpeechRecognitionController
  webkitSpeechRecognition?: new () => SpeechRecognitionController
}
