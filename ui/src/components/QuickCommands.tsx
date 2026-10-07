import { useEffect, useState } from 'react'
import { sendText, type TerminalAddress } from '../api/terminal'
import { commandsDisabledReason, isClaudeAgent } from './quickCommands'

interface Props {
  address: TerminalAddress
  agent?: string
  state?: string
  suggestion?: string | null
  /** Returns false when the composer refused (it holds a draft). */
  onPrefill: (text: string) => boolean
}

type Feedback = { kind: 'sent' | 'accepted' | 'stalled' | 'error' | 'note'; text: string }

const STALL_MS = 10_000

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

const noteStyle: React.CSSProperties = { fontSize: 13, color: 'var(--text-secondary)', padding: '6px 8px 0' }

const COMMANDS = ['/compact', '/context']

// Only /compact reliably moves the run out of idle; the others may finish
// without any state change, so silence there is not a failure signal.
function stalledText(cmd: string): string {
  return cmd.split(/\s/)[0] === '/compact'
    ? 'No reaction seen yet — check the terminal.'
    : 'Sent — this command may not change the run state; check the terminal.'
}

export function QuickCommands({ address, agent, state, suggestion, onPrefill }: Props) {
  const [open, setOpen] = useState(false)
  const [confirmClear, setConfirmClear] = useState(false)
  const [sheet, setSheet] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [dismissed, setDismissed] = useState<string | null>(null)
  const [feedback, setFeedback] = useState<Feedback | null>(null)
  const reason = commandsDisabledReason(state)
  const disabled = reason !== null
  if (feedback?.kind === 'sent' && state !== 'idle') {
    setFeedback({ kind: 'accepted', text: `${feedback.text} accepted` })
  }
  // Once the suggestion moves on, a later identical one must show again.
  if (dismissed !== null && suggestion !== dismissed) setDismissed(null)

  useEffect(() => {
    if (feedback?.kind !== 'sent') return
    const text = stalledText(feedback.text)
    const t = setTimeout(() => setFeedback({ kind: 'stalled', text }), STALL_MS)
    return () => clearTimeout(t)
  }, [feedback])

  if (!isClaudeAgent(agent)) return null

  const send = async (cmd: string): Promise<boolean> => {
    setBusy(true)
    const err = await sendText(address, cmd)
    setBusy(false)
    if (err) {
      setFeedback({ kind: 'error', text: `Not sent: ${err}` })
      return false
    }
    setFeedback({ kind: 'sent', text: cmd })
    return true
  }

  const sendSheet = async () => {
    if (sheet === null) return
    const cmd = sheet.trim()
    if (!cmd) return
    const suggestedAtSend = suggestion ?? null
    if (!(await send(cmd))) return
    setSheet(null)
    setDismissed(suggestedAtSend)
  }

  const btnDisabled = disabled || busy
  const pill = (label: string, onClick: () => void, extra?: Partial<React.CSSProperties>) => (
    <button
      key={label}
      type="button"
      className="pill-btn"
      disabled={btnDisabled}
      title={reason ?? undefined}
      onClick={onClick}
      style={{ ...pillStyle, ...(btnDisabled && { opacity: 0.5, cursor: 'default' }), ...extra }}
    >
      {label}
    </button>
  )

  return (
    <div data-testid="quick-commands">
      <div style={{ display: 'flex', gap: 6, padding: '8px 8px 0', overflowX: 'auto' }}>
        <button
          type="button"
          className="pill-btn"
          aria-label="Commands"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
          style={{ ...pillStyle, ...(open && { color: 'var(--text-primary)' }) }}
        >
          /
        </button>
        {open && (
          <>
            {COMMANDS.map((c) => pill(c, () => void send(c)))}
            {/* Prefill goes through the composer's normal Send, so it is not idle-guarded like the direct pills. */}
            {pill('/compact…', () => {
              if (!onPrefill('/compact ')) setFeedback({ kind: 'note', text: 'Clear the draft and attachment first, then tap /compact…' })
            })}
            {pill('/clear', () => setConfirmClear(true))}
          </>
        )}
        {suggestion && suggestion !== dismissed && pill('↳ suggested', () => setSheet(suggestion))}
      </div>

      {open && reason && <div style={noteStyle}>Commands unavailable: {reason}</div>}

      {confirmClear && (
        <div role="group" aria-label="Confirm /clear" style={{ ...noteStyle, display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          <span>Send /clear? This wipes the session context.</span>
          {pill('Confirm', () => {
            setConfirmClear(false)
            void send('/clear')
          })}
          <button type="button" className="pill-btn" style={pillStyle} onClick={() => setConfirmClear(false)}>
            Cancel
          </button>
        </div>
      )}

      {sheet !== null && (
        <div style={{ padding: '8px 8px 0', display: 'flex', flexDirection: 'column', gap: 6 }}>
          <label htmlFor="quick-suggested" style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
            Suggested command
          </label>
          <textarea
            id="quick-suggested"
            value={sheet}
            onChange={(e) => setSheet(e.target.value)}
            rows={3}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            style={{
              background: 'var(--bg-surface)',
              border: '1px solid var(--border)',
              borderRadius: 6,
              padding: '6px 10px',
              color: 'var(--text-primary)',
              fontSize: 16,
              fontFamily: 'var(--font-mono)',
              resize: 'none',
            }}
          />
          <div style={{ display: 'flex', gap: 6 }}>
            {pill('Send command', () => void sendSheet())}
            <button type="button" className="pill-btn" style={pillStyle} onClick={() => setSheet(null)}>
              Cancel
            </button>
          </div>
        </div>
      )}

      {feedback &&
        (feedback.kind === 'error' ? (
          <div role="alert" style={{ ...noteStyle, color: 'var(--accent-error)' }}>{feedback.text}</div>
        ) : (
          <div role="status" style={noteStyle}>
            {feedback.kind === 'sent' ? `Sent ${feedback.text} — waiting for the agent…` : feedback.text}
          </div>
        ))}
    </div>
  )
}
