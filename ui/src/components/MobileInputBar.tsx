import { useRef, useState } from 'react'

interface Props {
  target: string
  choices?: string[]
}

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

async function sendLine(target: string, text: string) {
  const body = new URLSearchParams({ input: text })
  await fetch(`/api/pane/${target}/send`, {
    method: 'POST',
    body,
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
  })
}

export function MobileInputBar({ target, choices }: Props) {
  const [text, setText] = useState('')
  const [listening, setListening] = useState(false)
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)

  const handleSend = async () => {
    const line = text.trim()
    if (!line) return
    setText('')
    await sendLine(target, line)
  }

  const handleChoice = async (choice: string) => {
    await sendLine(target, choice)
  }

  const handleVoice = () => {
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

    rec.onresult = (e: SpeechRecognitionEvent) => {
      const transcript = e.results[0]?.[0]?.transcript ?? ''
      setText(transcript)
    }

    rec.onend = () => setListening(false)

    recognitionRef.current = rec
    setListening(true)
    rec.start()
  }

  const hasSpeech = !!SpeechRecognitionCtor

  return (
    <div
      style={{
        borderTop: '1px solid var(--border)',
        background: 'var(--bg-header)',
        flexShrink: 0,
      }}
    >
      {choices && choices.length > 0 && (
        <div
          style={{
            display: 'flex',
            gap: 6,
            padding: '6px 8px 0',
            flexWrap: 'wrap',
          }}
        >
          {choices.map((c) => (
            <button
              key={c}
              onClick={() => handleChoice(c)}
              style={{
                background: 'var(--bg-surface)',
                border: '1px solid var(--border)',
                borderRadius: 4,
                color: 'var(--text-primary)',
                fontSize: 12,
                padding: '4px 10px',
                cursor: 'pointer',
              }}
            >
              {c}
            </button>
          ))}
        </div>
      )}

      <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: 8 }}>
        <input
          type="text"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              void handleSend()
            }
          }}
          placeholder="Send a message..."
          style={{
            flex: 1,
            background: 'var(--bg-surface)',
            border: '1px solid var(--border)',
            borderRadius: 6,
            padding: '6px 10px',
            color: 'var(--text-primary)',
            fontSize: 14,
            outline: 'none',
          }}
        />

        {hasSpeech && (
          <button
            onClick={handleVoice}
            style={{
              background: listening ? 'var(--accent-attention)' : 'var(--bg-surface)',
              border: '1px solid var(--border)',
              borderRadius: 6,
              color: listening ? '#000' : 'var(--text-secondary)',
              cursor: 'pointer',
              fontSize: 16,
              width: 36,
              height: 36,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              flexShrink: 0,
            }}
            title={listening ? 'Stop recording' : 'Voice input'}
          >
            🎤
          </button>
        )}

        <button
          onClick={() => void handleSend()}
          disabled={!text.trim()}
          style={{
            background: text.trim() ? 'var(--accent-working)' : 'var(--bg-surface)',
            border: '1px solid var(--border)',
            borderRadius: 6,
            color: text.trim() ? '#fff' : 'var(--text-muted)',
            cursor: text.trim() ? 'pointer' : 'default',
            fontSize: 16,
            width: 36,
            height: 36,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            flexShrink: 0,
          }}
        >
          ↵
        </button>
      </div>
    </div>
  )
}
