import { useRef, useState } from 'react'
import { replyRun, type ReplyOutcome } from '../api/runs'
import './fleet.css'

export function ReplyComposer({ runId }: { runId: string }) {
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const [outcome, setOutcome] = useState<ReplyOutcome | null>(null)
  // A ref, not just the `sending` state: two taps can both read `sending`
  // before either re-render lands. The ref is set synchronously, before the
  // first await.
  const inFlight = useRef(false)

  async function send() {
    const trimmed = text.trim()
    if (inFlight.current || !trimmed) return
    inFlight.current = true
    setSending(true)
    const result = await replyRun(runId, trimmed)
    inFlight.current = false
    setSending(false)
    setOutcome(result)
    if (result.kind === 'delivered') setText('')
  }

  return (
    <div className="crew-reply">
      <div className="crew-reply-row">
        <input
          type="text"
          value={text}
          disabled={sending}
          placeholder="Reply to the crew…"
          aria-label="Reply to the crew"
          onChange={(e) => setText(e.target.value)}
        />
        <button type="button" disabled={sending || !text.trim()} onClick={send}>
          Send
        </button>
      </div>
      {outcome && (
        <div className={`crew-reply-status ${outcome.kind}`}>
          {outcome.kind === 'delivered' && 'Delivered — waiting to be picked up.'}
          {outcome.kind !== 'delivered' && outcome.reason}
        </div>
      )}
    </div>
  )
}
