import { useCallback, useEffect, useRef, useState } from 'react'
import { sendImage, sendKey, sendText, terminalKey, type TerminalAddress } from '../api/terminal'
import { composerMaxHeight } from './composerMaxHeight'
import { StagedImageChip } from './StagedImageChip'
import { useComposerDraft } from './useComposerDraft'
import { readBase64, useStagedImage } from './useStagedImage'

interface Props {
  address: TerminalAddress
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

const errorStyle: React.CSSProperties = {
  color: 'var(--accent-error)',
  fontSize: 13,
  padding: '6px 10px 0',
}

export function MobileInputBar({ address, choices, inputText, agent }: Props) {
  const [text, setText, clearIf] = useComposerDraft(terminalKey(address))
  const { staged, stage, clear: clearStaged } = useStagedImage()
  const [listening, setListening] = useState(false)
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<string | null>(null)
  const [keyError, setKeyError] = useState<string | null>(null)
  const sendingRef = useRef(false)
  const keyErrorTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => () => clearTimeout(keyErrorTimer.current), [])
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleSend = async () => {
    if (sendingRef.current) return
    const sent = text
    const line = sent.trim()
    sendingRef.current = true
    setSending(true)
    setSendError(null)
    const err = staged
      ? await readBase64(staged.file).then(
          (data) => sendImage(address, line, { name: staged.file.name, type: staged.file.type, data }),
          () => 'could not read file',
        )
      : line
        ? await sendText(address, line)
        : await sendKey(address, 'Enter')
    sendingRef.current = false
    setSending(false)
    if (err) {
      setSendError(`${err} — tap Send to retry`)
      return
    }
    // Keep anything typed while the request was in flight.
    clearIf(sent)
    clearStaged()
    if (textareaRef.current) textareaRef.current.style.height = 'auto'
  }

  const reportKeyError = useCallback((err: string | null) => {
    if (!err) return
    setKeyError(err)
    clearTimeout(keyErrorTimer.current)
    keyErrorTimer.current = setTimeout(() => setKeyError(null), 3000)
  }, [])

  // Claude's prompts are numbered menus answered by their ordinal key. Other
  // agents' choices aren't (Amp reorders the selected item first and selects by
  // cursor), so an ordinal would be wrong there — they keep label + Enter.
  const handleChoice = async (index: number, label: string) => {
    reportKeyError(
      agent === 'claude-code' ? await sendKey(address, String(index + 1)) : await sendText(address, label),
    )
  }

  const handleQuickAction = useCallback(async (action: 'text' | 'special', value: string) => {
    reportKeyError(action === 'special' ? await sendKey(address, value) : await sendText(address, value))
  }, [address, reportKeyError])

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
      setSendError(null)
    }

    rec.onend = () => setListening(false)

    recognitionRef.current = rec
    setListening(true)
    rec.start()
  }

  const autoGrow = (el: HTMLTextAreaElement) => {
    el.style.height = 'auto'
    const max = composerMaxHeight(window.visualViewport?.height ?? window.innerHeight)
    el.style.height = Math.min(el.scrollHeight, max) + 'px'
    el.style.overflowY = el.scrollHeight > max ? 'auto' : 'hidden'
  }

  const handleFileAttach = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    // Reset so the same file can be selected again
    e.target.value = ''
    if (file) stage(file)
  }

  const hasSpeech = !!SpeechRecognitionCtor
  const canSend = !!text.trim() || !!staged

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

      {keyError && (
        <div role="alert" data-testid="key-error" style={errorStyle}>
          Key not sent: {keyError}
        </div>
      )}

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

      {sendError && (
        <div role="alert" data-testid="send-error" style={errorStyle}>
          Not sent: {sendError}
        </div>
      )}

      {staged && (
        <div style={{ padding: '6px 8px 0' }}>
          <StagedImageChip staged={staged} onRemove={clearStaged} disabled={sending} />
        </div>
      )}

      {/* Text input row */}
      <div style={{ display: 'flex', alignItems: 'flex-end', gap: 6, padding: 8 }}>
        <textarea
          ref={textareaRef}
          value={text}
          onChange={(e) => {
            setText(e.target.value)
            setSendError(null)
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
            overflowY: 'hidden',
            // Native touch scrolling inside the field; never chain to the terminal.
            touchAction: 'pan-y',
            overscrollBehavior: 'contain',
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
          onChange={handleFileAttach}
          style={{ display: 'none' }}
        />
        <button
          onClick={() => fileInputRef.current?.click()}
          disabled={sending}
          style={{
            background: 'var(--bg-surface)',
            border: '1px solid var(--border)',
            borderRadius: 6,
            color: 'var(--text-secondary)',
            cursor: sending ? 'default' : 'pointer',
            fontSize: 16,
            width: 44,
            height: 44,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            flexShrink: 0,
            opacity: sending ? 0.6 : 1,
          }}
          title="Attach file"
        >
          📎
        </button>

        <button
          onClick={() => void handleSend()}
          disabled={sending}
          title="Send"
          aria-label="Send"
          style={{
            background: canSend ? 'var(--accent-working)' : 'var(--bg-surface)',
            border: canSend ? 'none' : '1px solid var(--border)',
            borderRadius: 6,
            color: canSend ? '#fff' : 'var(--text-secondary)',
            cursor: sending ? 'default' : 'pointer',
            opacity: sending ? 0.6 : 1,
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
