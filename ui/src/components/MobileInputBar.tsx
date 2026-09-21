import { useCallback, useRef, useState } from 'react'

interface Props {
  target: string
  choices?: string[]
  inputText?: string
  agent?: string
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

type QuickAction = { label: string; action: 'text' | 'special'; value: string; title?: string }

// One scrollable row. Everything but /copy is a keystroke (special route: no
// implicit Enter), so a digit answers a numbered prompt without a stray Enter;
// the composer's Send is what appends Enter.
const quickActions: QuickAction[] = [
  { label: 'Esc', action: 'special', value: 'Escape' },
  { label: '^C', action: 'special', value: 'C-c', title: 'Ctrl+C' },
  { label: '↵', action: 'special', value: 'Enter', title: 'Enter' },
  { label: 'Tab', action: 'special', value: 'Tab' },
  { label: '⇧Tab', action: 'special', value: 'BTab', title: 'Shift+Tab' },
  { label: '↑', action: 'special', value: 'Up', title: 'Up' },
  { label: '↓', action: 'special', value: 'Down', title: 'Down' },
  { label: '1', action: 'special', value: '1' },
  { label: '2', action: 'special', value: '2' },
  { label: '3', action: 'special', value: '3' },
  { label: '4', action: 'special', value: '4' },
  { label: '5', action: 'special', value: '5' },
  { label: 'Y', action: 'special', value: 'y' },
  { label: 'N', action: 'special', value: 'n' },
  { label: 'A-p', action: 'special', value: 'M-p', title: 'Alt+P' },
  { label: '^O', action: 'special', value: 'C-o', title: 'Ctrl+O' },
  { label: '^Z', action: 'special', value: 'C-z', title: 'Ctrl+Z' },
  { label: '/copy', action: 'text', value: '/copy' },
]

const pillStyle: React.CSSProperties = {
  background: 'var(--bg-surface)',
  border: '1px solid var(--border)',
  borderRadius: 12,
  color: 'var(--text-secondary)',
  fontSize: 14,
  fontFamily: 'var(--font-mono)',
  padding: '0 14px',
  minHeight: 40,
  cursor: 'pointer',
  whiteSpace: 'nowrap',
  flexShrink: 0,
}

export function MobileInputBar({ target, choices, inputText, agent }: Props) {
  const [text, setText] = useState('')
  const [listening, setListening] = useState(false)
  const [uploading, setUploading] = useState(false)
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleSend = async () => {
    const line = text.trim()
    if (!line) {
      // Empty input: send Enter key to the terminal
      await sendSpecial(target, 'Enter')
      return
    }
    setText('')
    if (textareaRef.current) textareaRef.current.style.height = 'auto'
    await sendText(target, line)
  }

  // Claude's prompts are numbered menus answered by their ordinal key. Other
  // agents' choices aren't (Amp reorders the selected item first and selects by
  // cursor), so an ordinal would be wrong there — they keep label + Enter.
  const handleChoice = async (index: number, label: string) => {
    if (agent === 'claude-code') await sendSpecial(target, String(index + 1))
    else await sendText(target, label)
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

  const handleFileAttach = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return

    setUploading(true)
    try {
      const base64 = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => {
          const result = reader.result as string
          resolve(result.replace(/^data:[^;]+;base64,/, ''))
        }
        reader.onerror = reject
        reader.readAsDataURL(file)
      })

      await fetch(`/api/pane/${target}/send-with-images`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          text: text.trim(),
          images: [{ name: file.name, type: file.type, data: base64 }],
        }),
      })

      setText('')
      if (textareaRef.current) textareaRef.current.style.height = 'auto'
    } finally {
      setUploading(false)
      // Reset so the same file can be selected again
      if (fileInputRef.current) fileInputRef.current.value = ''
    }
  }

  const hasSpeech = !!SpeechRecognitionCtor

  return (
    <div
      className="mobile-input-bar"
      style={{
        borderTop: '1px solid var(--border)',
        background: 'var(--bg-header)',
        flexShrink: 0,
        // Clear the home indicator / notch on edge-to-edge phones.
        paddingBottom: 'env(safe-area-inset-bottom)',
        paddingLeft: 'env(safe-area-inset-left)',
        paddingRight: 'env(safe-area-inset-right)',
      }}
    >
      {/* Agent choice buttons */}
      {choices && choices.length > 0 && (
        <div
          data-testid="choices"
          style={{
            display: 'flex',
            gap: 8,
            padding: '8px 8px 0',
            flexWrap: 'wrap',
            maxHeight: 104,
            overflowY: 'auto',
            animation: 'slide-up 0.18s ease-out',
          }}
        >
          {choices.map((c, i) => (
            <button
              key={`${i}-${c}`}
              onClick={() => void handleChoice(i, c)}
              style={{
                background: 'var(--bg-surface)',
                border: '1px solid var(--accent-attention)',
                borderRadius: 6,
                color: 'var(--accent-attention)',
                fontSize: 14,
                minHeight: 40,
                padding: '0 12px',
                cursor: 'pointer',
              }}
            >
              {agent === 'claude-code' ? `${i + 1}. ${c}` : c}
            </button>
          ))}
        </div>
      )}

      {/* Quick keys — one horizontally scrollable row */}
      <div
        data-testid="quick-keys"
        style={{ display: 'flex', gap: 6, padding: '8px 8px 0', overflowX: 'auto' }}
      >
        {quickActions.map((qa) => (
          <button
            key={qa.label}
            className="pill-btn"
            title={qa.title}
            aria-label={qa.title}
            onClick={() => void handleQuickAction(qa.action, qa.value)}
            style={pillStyle}
          >
            {qa.label}
          </button>
        ))}
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
            if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
              e.preventDefault()
              void handleSend()
            }
          }}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          placeholder={inputText || 'Send a message...'}
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

        <input
          ref={fileInputRef}
          type="file"
          onChange={(e) => void handleFileAttach(e)}
          style={{ display: 'none' }}
        />
        <button
          onClick={() => fileInputRef.current?.click()}
          disabled={uploading}
          style={{
            background: uploading ? 'var(--accent-working)' : 'var(--bg-surface)',
            border: '1px solid var(--border)',
            borderRadius: 6,
            color: uploading ? '#fff' : 'var(--text-secondary)',
            cursor: uploading ? 'default' : 'pointer',
            fontSize: 16,
            width: 44,
            height: 44,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            flexShrink: 0,
            opacity: uploading ? 0.7 : 1,
          }}
          title="Attach file"
        >
          {uploading ? '...' : '📎'}
        </button>

        <button
          onClick={() => void handleSend()}
          title="Send"
          aria-label="Send"
          style={{
            background: text.trim() ? 'var(--accent-working)' : 'var(--bg-surface)',
            border: text.trim() ? 'none' : '1px solid var(--border)',
            borderRadius: 6,
            color: text.trim() ? '#fff' : 'var(--text-secondary)',
            cursor: 'pointer',
            fontSize: 16,
            width: 44,
            height: 44,
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
