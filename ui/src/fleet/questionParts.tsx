import { useEffect, useRef } from 'react'
import type { ToolCall } from './chatModel'
import type { Stage } from './questionStage'
import { runHash } from './routes'
import { onAppLink, switchRunTab } from './nav'

export interface QuestionOption {
  label: string
  description?: string
}

export interface Question {
  question: string
  header?: string
  multiSelect: boolean
  options: QuestionOption[]
  // Claude Code renders a single-select question with option previews in a
  // layout that has no Other row.
  preview: boolean
}

export function TerminalLink({ runId, children = 'Terminal tab' }: { runId: string; children?: string }) {
  return (
    <a className="chat-question-terminal" href={runHash(runId, 'terminal')} onClick={onAppLink(() => switchRunTab(runId, 'terminal'))}>
      {children}
    </a>
  )
}

export function QuestionBlock({ q, chosen }: { q: Question; chosen?: string[] }) {
  const labels = new Set(q.options.map((o) => o.label))
  const other = chosen?.filter((a) => !labels.has(a)) ?? []
  return (
    <div className="chat-question-block">
      {q.header && <span className="chat-question-header">{q.header}</span>}
      <div className="chat-question-text">{q.question}</div>
      {q.multiSelect && <div className="chat-question-hint">Select all that apply</div>}
      <ul className="chat-question-options">
        {q.options.map((o) => {
          const picked = chosen?.includes(o.label) ?? false
          return (
            <li key={o.label} className={`chat-question-option${picked ? ' chosen' : ''}`}>
              <span className="chat-question-marker" aria-hidden="true">
                {q.multiSelect ? (picked ? '☑' : '☐') : picked ? '●' : '○'}
              </span>
              <span className="chat-question-option-body">
                <span className="chat-question-option-label">{o.label}</span>
                {o.description && <span className="chat-question-option-desc">{o.description}</span>}
              </span>
              {picked && <span className="chat-question-sr">chosen</span>}
            </li>
          )
        })}
      </ul>
      {other.length > 0 && <div className="chat-question-other">Other: {other.join(', ')}</div>}
    </div>
  )
}

/** `showHeader` is off where the header is already shown elsewhere (the
 *  wizard's step chips). */
export function PickerBlock({ q, stage, disabled, showHeader = true, onOption, onOther, onText }: {
  q: Question
  stage: Stage
  disabled: boolean
  showHeader?: boolean
  onOption: (index: number) => void
  onOther: () => void
  onText: (text: string) => void
}) {
  const role = q.multiSelect ? 'checkbox' : 'radio'
  const marker = (on: boolean) => (q.multiSelect ? (on ? '☑' : '☐') : on ? '●' : '○')
  const otherIndex = q.options.length
  const checked = stage.otherOn ? otherIndex : (stage.options[0] ?? 0)
  // Single-select is a radiogroup: one tab stop, arrows move it (WAI-ARIA).
  const tabIndex = (i: number) => (q.multiSelect || i === checked ? 0 : -1)
  const select = (i: number) => (i === otherIndex ? onOther() : onOption(i))

  const otherRef = useRef<HTMLInputElement>(null)
  // Only a tap or click on Other moves focus into its field; arrowing onto it
  // keeps focus on the radio.
  const focusOther = useRef(false)
  useEffect(() => {
    const input = otherRef.current
    if (!stage.otherOn || !input) return
    if (focusOther.current) {
      focusOther.current = false
      input.focus()
    }
    const viewport = window.visualViewport
    if (!viewport) return
    // The on-screen keyboard shrinks the viewport after focus already scrolled.
    const reveal = () => { if (document.activeElement === input) input.scrollIntoView?.({ block: 'nearest' }) }
    viewport.addEventListener('resize', reveal)
    return () => viewport.removeEventListener('resize', reveal)
  }, [stage.otherOn])

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (q.multiSelect || (e.target as HTMLElement).getAttribute('role') !== 'radio') return
    const step = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? 1 : e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? -1 : 0
    if (!step) return
    e.preventDefault()
    const radios = e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]')
    const next = (Array.from(radios).indexOf(e.target as HTMLButtonElement) + step + radios.length) % radios.length
    select(next)
    radios[next].focus()
  }

  return (
    <div className="chat-question-block">
      {showHeader && q.header && <span className="chat-question-header">{q.header}</span>}
      <div className="chat-question-text">{q.question}</div>
      {q.multiSelect && <div className="chat-question-hint">Select all that apply</div>}
      <div className="chat-question-picks" role={q.multiSelect ? 'group' : 'radiogroup'} aria-label={q.question} onKeyDown={onKeyDown}>
        {q.options.map((o, i) => {
          const on = stage.options.includes(i)
          return (
            <button
              key={o.label}
              type="button"
              role={role}
              aria-checked={on}
              tabIndex={tabIndex(i)}
              disabled={disabled}
              className={`chat-question-pick${on ? ' on' : ''}`}
              onClick={() => onOption(i)}
            >
              <span className="chat-question-marker" aria-hidden="true">{marker(on)}</span>
              <span className="chat-question-option-body">
                <span className="chat-question-option-label">{o.label}</span>
                {o.description && <span className="chat-question-option-desc">{o.description}</span>}
              </span>
            </button>
          )
        })}
        {!q.preview && (
          <button
            type="button"
            role={role}
            aria-checked={stage.otherOn}
            tabIndex={tabIndex(otherIndex)}
            disabled={disabled}
            className={`chat-question-pick${stage.otherOn ? ' on' : ''}`}
            onClick={() => {
              focusOther.current = !stage.otherOn
              onOther()
            }}
          >
            <span className="chat-question-marker" aria-hidden="true">{marker(stage.otherOn)}</span>
            <span className="chat-question-option-body">
              <span className="chat-question-option-label">Other</span>
            </span>
          </button>
        )}
        {!q.preview && stage.otherOn && (
          <input
            ref={otherRef}
            type="text"
            className="chat-question-other-input"
            aria-label={`Other answer: ${q.question}`}
            maxLength={500}
            value={stage.text}
            disabled={disabled}
            onChange={(e) => onText(e.target.value)}
            onFocus={(e) => e.currentTarget.scrollIntoView?.({ block: 'nearest' })}
          />
        )}
      </div>
    </div>
  )
}

/** `terminalRunId`: the question is still open and can't be shown here, so
 *  point to that run's Terminal tab, where it can be answered. */
export function FallbackRow({ call, terminalRunId }: { call: ToolCall; terminalRunId?: string }) {
  return (
    <div className="chat-question-fallback" data-testid="question-fallback">
      <div className="chat-tool-call-row">
        <span className="chat-tool-name">{call.tool}</span>
        {call.title && <span className="chat-tool-title">{call.title}</span>}
      </div>
      {terminalRunId && (
        <div className="chat-question-pending">
          Can't show this question here — answer it in the Terminal tab
          <TerminalLink runId={terminalRunId}>Open Terminal</TerminalLink>
        </div>
      )}
    </div>
  )
}
