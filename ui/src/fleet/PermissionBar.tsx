import { useEffect, useRef, useState } from 'react'
import { answerChoice, fetchPrompt } from '../api/answer'
import type { Prompt } from '../api/answer'
import type { Run } from '../api/runs'
import { isClaudeAgent } from '../components/quickCommands'

const POLL_MS = 2000

function PromptDialog({ runId, prompt, onAnswered, onMoved }: {
  runId: string
  prompt: Prompt
  onAnswered: () => void
  onMoved: () => void
}) {
  const [expanded, setExpanded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState('')

  const answer = async (ordinal: number) => {
    setBusy(true)
    setStatus('')
    const res = await answerChoice(runId, ordinal, prompt.frame)
    if ('ok' in res) return onAnswered()
    setBusy(false)
    if ('error' in res) return setStatus(res.error)
    setStatus('The session moved on — refresh')
    onMoved()
  }

  return (
    <div className="permission-bar" role="group" aria-label="Permission prompt">
      <span className="permission-bar-state">needs you</span>
      <div className="permission-bar-question">{prompt.question}</div>
      {prompt.detail && (
        <button
          type="button"
          className={`permission-bar-detail${expanded ? ' expanded' : ''}`}
          aria-expanded={expanded}
          onClick={() => setExpanded((e) => !e)}
        >
          {prompt.detail}
        </button>
      )}
      <div className="permission-bar-choices">
        {prompt.choices.map((label, i) => (
          <button key={i} type="button" className="permission-bar-choice" disabled={busy} onClick={() => answer(i + 1)}>
            {i + 1}. {label}
          </button>
        ))}
      </div>
      <div className="permission-bar-status" role="status">{status}</div>
    </div>
  )
}

/** Pinned buttons for the pane's open permission dialog, polled while a Claude
 *  run could be showing one. `idle` counts: an `idle_prompt` demotes a still-open
 *  dialog to idle. The prompt route stays the authority on whether one exists. */
export function PermissionBar({ run, onPrompt }: { run: Run; onPrompt: (shown: boolean) => void }) {
  const active = isClaudeAgent(run.agent) && run.caps.terminal && (run.state === 'blocked' || run.state === 'idle')
  const [prompt, setPrompt] = useState<Prompt | null>(null)
  const [answered, setAnswered] = useState<string | null>(null)
  const refetch = useRef<() => void>(() => {})

  useEffect(() => {
    if (!active) return
    let cancelled = false
    const load = async () => {
      try {
        const next = await fetchPrompt(run.id)
        if (cancelled) return
        setPrompt(next)
        if (next === null) setAnswered(null)
      } catch {
        // keep the last value
      }
    }
    refetch.current = load
    void load()
    const timer = setInterval(load, POLL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
      setPrompt(null)
    }
  }, [run.id, active])

  const shown = active && prompt !== null && prompt.frame !== answered
  useEffect(() => { onPrompt(shown) }, [shown, onPrompt])

  if (!shown) return null
  return (
    <PromptDialog
      key={prompt.frame}
      runId={run.id}
      prompt={prompt}
      onAnswered={() => setAnswered(prompt.frame)}
      onMoved={() => refetch.current()}
    />
  )
}
