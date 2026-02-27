import { useCallback, useRef, useState } from 'react'

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

async function sendText(target: string, text: string) {
  const body = new URLSearchParams({ input: text })
  await fetch(`/api/pane/${target}/send`, {
    method: 'POST',
    body,
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
  })
}

async function sendSpecial(target: string, key: string) {
  const body = new URLSearchParams({ input: key, special: 'true' })
  await fetch(`/api/pane/${target}/send`, {
    method: 'POST',
    body,
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
  })
}

type QuickAction = { label: string; action: 'text' | 'special'; value: string }

// Primary row: always visible
const primaryActions: QuickAction[] = [
  { label: '1', action: 'text', value: '1' },
  { label: '2', action: 'text', value: '2' },
  { label: '3', action: 'text', value: '3' },
  { label: '4', action: 'text', value: '4' },
  { label: '5', action: 'text', value: '5' },
  { label: 'Y', action: 'text', value: 'y' },
  { label: 'N', action: 'text', value: 'n' },
  { label: '^C', action: 'special', value: 'C-c' },
  { label: '⏎', action: 'special', value: 'Enter' },
]

// Expanded rows: shown when toggle is open
const extraActions: QuickAction[] = [
  { label: '↑', action: 'special', value: 'Up' },
  { label: '↓', action: 'special', value: 'Down' },
  { label: 'Esc', action: 'special', value: 'Escape' },
  { label: 'Tab', action: 'special', value: 'Tab' },
  { label: '⇧Tab', action: 'special', value: 'BTab' },
  { label: 'M-p', action: 'special', value: 'M-p' },
  { label: 'C-o', action: 'special', value: 'C-o' },
  { label: 'C-z', action: 'special', value: 'C-z' },
]

const pillStyle: React.CSSProperties = {
  background: 'var(--bg-surface)',
  border: '1px solid var(--border)',
  borderRadius: 12,
  color: 'var(--text-secondary)',
  fontSize: 14,
  fontFamily: 'var(--font-mono)',
  padding: '6px 12px',
  cursor: 'pointer',
  whiteSpace: 'nowrap',
}

export function MobileInputBar({ target, choices }: Props) {
  const [text, setText] = useState('')
  const [listening, setListening] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const handleSend = async () => {
    const line = text.trim()
    if (!line) return
    setText('')
    if (textareaRef.current) textareaRef.current.style.height = 'auto'
    await sendText(target, line)
  }

  const handleChoice = async (choice: string) => {
    await sendText(target, choice)
  }

  const handleQuickAction = useCallback(async (action: 'text' | 'special', value: string) => {
    if (action === 'special') {
      await sendSpecial(target, value)
    } else {
      await sendText(target, value)
    }
  }, [target])

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

  const autoGrow = (el: HTMLTextAreaElement) => {
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, 4 * 24) + 'px'
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
      {/* Agent choice buttons */}
      {choices && choices.length > 0 && (
        <div
          style={{
            display: 'flex',
            gap: 6,
            padding: '6px 8px 0',
            flexWrap: 'wrap',
            animation: 'slide-up 0.18s ease-out',
          }}
        >
          {choices.map((c) => (
            <button
              key={c}
              onClick={() => handleChoice(c)}
              style={{
                background: 'var(--bg-surface)',
                border: '1px solid var(--accent-attention)',
                borderRadius: 4,
                color: 'var(--accent-attention)',
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

      {/* Quick action pills — wrapping grid with expand toggle */}
      <div style={{ display: 'flex', alignItems: 'flex-start', padding: '6px 8px 0' }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, flex: 1 }}>
          {primaryActions.map((qa) => (
            <button
              key={qa.label}
              onClick={() => void handleQuickAction(qa.action, qa.value)}
              style={pillStyle}
            >
              {qa.label}
            </button>
          ))}
          {expanded && extraActions.map((qa) => (
            <button
              key={qa.label}
              onClick={() => void handleQuickAction(qa.action, qa.value)}
              style={pillStyle}
            >
              {qa.label}
            </button>
          ))}
        </div>
        <button
          onClick={() => setExpanded((e) => !e)}
          style={{
            background: 'none',
            border: 'none',
            color: 'var(--text-muted)',
            fontSize: 14,
            cursor: 'pointer',
            padding: '6px 4px',
            flexShrink: 0,
          }}
        >
          {expanded ? '▴' : '▾'}
        </button>
      </div>

      {/* Text input row */}
      <div style={{ display: 'flex', alignItems: 'flex-end', gap: 6, padding: 8 }}>
        <textarea
          ref={textareaRef}
          value={text}
          onChange={(e) => {
            setText(e.target.value)
            autoGrow(e.target)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && e.shiftKey) {
              e.preventDefault()
              void handleSend()
            }
          }}
          placeholder="Send a message..."
          rows={1}
          style={{
            flex: 1,
            background: 'var(--bg-surface)',
            border: '1px solid var(--border)',
            borderRadius: 6,
            padding: '6px 10px',
            color: 'var(--text-primary)',
            fontSize: 16,
            lineHeight: '24px',
            outline: 'none',
            resize: 'none',
            fontFamily: 'inherit',
            overflow: 'hidden',
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
            background: text.trim() ? 'var(--accent-working)' : 'transparent',
            border: 'none',
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
