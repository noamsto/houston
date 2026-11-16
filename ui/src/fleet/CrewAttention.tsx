import { useState } from 'react'
import type { CrewAttention as Item } from '../api/runs'
import { runCrewAction, type CrewActionOutcome } from '../api/crewAction'

type Mode = { kind: 'idle' } | { kind: 'input' } | { kind: 'confirm'; prompt: string; pr?: number }

export function CrewAttention({ runId, items }: { runId: string; items: Item[] }) {
  if (items.length === 0) return null
  return (
    <ul className="crew-attention">
      {items.map((it) => (
        <AttentionRow key={it.id} runId={runId} item={it} />
      ))}
    </ul>
  )
}

function AttentionRow({ runId, item }: { runId: string; item: Item }) {
  const { action } = item
  const [mode, setMode] = useState<Mode>({ kind: 'idle' })
  const [text, setText] = useState('')
  const [pending, setPending] = useState(false)
  const [outcome, setOutcome] = useState<CrewActionOutcome | null>(null)

  async function run(opts?: { text?: string; confirm?: boolean; pr?: number }) {
    setPending(true)
    try {
      const res = opts ? await runCrewAction(runId, item.id, opts) : await runCrewAction(runId, item.id)
      if (!res.ok && (res.status === 428 || res.status === 409) && res.confirm) {
        setMode({ kind: 'confirm', prompt: res.confirm, pr: res.pr })
        setOutcome(null)
        return
      }
      if (res.ok || !opts?.text) setMode({ kind: 'idle' })
      if (res.ok) setText('')
      setOutcome(res)
    } finally {
      setPending(false)
    }
  }

  function start() {
    if (!action) return
    if (action.needs_input) setMode({ kind: 'input' })
    else if (action.verb === 'merge') setMode({ kind: 'confirm', prompt: `Squash-merge PR #${action.pr}?`, pr: action.pr })
    else void run()
  }

  return (
    <li className="crew-attention-item">
      <div className="crew-attention-head">
        <span className={`crew-attention-sev sev-${item.severity}`}>{item.kind.replace(/_/g, ' ')}</span>
        <span className="crew-attention-msg">{item.message}</span>
      </div>
      {action && mode.kind === 'idle' && (
        <button type="button" className="crew-attention-btn" title={action.command} disabled={pending} onClick={start}>
          {action.label}
        </button>
      )}
      {mode.kind === 'input' && (
        <form
          className="crew-attention-form"
          onSubmit={(e) => {
            e.preventDefault()
            if (text.trim()) void run({ text })
          }}
        >
          <input type="text" value={text} disabled={pending} onChange={(e) => setText(e.target.value)} aria-label="Reply text" />
          <button type="submit" className="crew-attention-btn" disabled={pending}>Send</button>
          <button type="button" className="crew-attention-btn" disabled={pending} onClick={() => setMode({ kind: 'idle' })}>Cancel</button>
        </form>
      )}
      {mode.kind === 'confirm' && (
        <div className="crew-attention-form">
          <span>{mode.prompt}</span>
          <button type="button" className="crew-attention-btn" disabled={pending} onClick={() => void run(mode.pr === undefined ? { confirm: true } : { confirm: true, pr: mode.pr })}>Confirm</button>
          <button type="button" className="crew-attention-btn" disabled={pending} onClick={() => setMode({ kind: 'idle' })}>Cancel</button>
        </div>
      )}
      {!action && item.command && <code className="crew-attention-cmd">{item.command}</code>}
      {outcome?.ok && outcome.output && <pre className="crew-attention-output">{outcome.output}</pre>}
      {outcome && !outcome.ok && <div role="alert" className="crew-attention-error">{outcome.message}</div>}
    </li>
  )
}
