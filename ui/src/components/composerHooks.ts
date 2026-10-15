import { useCallback, useRef, useState } from 'react'
import { composerMaxHeight } from './composerMaxHeight'

// Web Speech API types (not in TS lib by default)
interface SpeechRecognitionEvent extends Event {
  results: SpeechRecognitionResultList
}
interface SpeechRecognitionResultList {
  readonly length: number
  item(index: number): SpeechRecognitionResult
  [index: number]: SpeechRecognitionResult
}
interface SpeechRecognitionResult {
  readonly length: number
  item(index: number): SpeechRecognitionAlternative
  [index: number]: SpeechRecognitionAlternative
  readonly isFinal: boolean
}
interface SpeechRecognitionAlternative {
  readonly transcript: string
  readonly confidence: number
}

type SpeechRecognitionLike = {
  lang: string
  continuous: boolean
  interimResults: boolean
  start: () => void
  stop: () => void
  onresult: ((e: SpeechRecognitionEvent) => void) | null
  onend: (() => void) | null
}

const SpeechRecognitionCtor = (window as unknown as Record<string, unknown>).SpeechRecognition as
  | (new () => SpeechRecognitionLike)
  | undefined

/** Minimum hit area of a composer control (px). */
export const COMPOSER_TAP_PX = 44

/** Grows a composer field to its content, up to the shared cap; the CSS max-height holds it to about five lines. */
export function autoGrow(el: HTMLTextAreaElement) {
  el.style.height = 'auto'
  const max = composerMaxHeight(window.visualViewport?.height ?? window.innerHeight)
  el.style.height = Math.min(el.scrollHeight, max) + 'px'
  el.style.overflowY = el.scrollHeight > max ? 'auto' : 'hidden'
}

export function useVoiceInput(onTranscript: (text: string) => void) {
  const [listening, setListening] = useState(false)
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)

  const toggle = useCallback(() => {
    if (!SpeechRecognitionCtor) return
    if (listening) {
      recognitionRef.current?.stop()
      setListening(false)
      return
    }
    const rec = new SpeechRecognitionCtor()
    rec.lang = 'en-US'
    rec.continuous = false
    rec.interimResults = false
    rec.onresult = (e: SpeechRecognitionEvent) => onTranscript(e.results[0]?.[0]?.transcript ?? '')
    rec.onend = () => setListening(false)
    recognitionRef.current = rec
    setListening(true)
    rec.start()
  }, [listening, onTranscript])

  return { supported: !!SpeechRecognitionCtor, listening, toggle }
}
