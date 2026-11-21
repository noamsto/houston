import { useCallback, useState } from 'react'
import { QuestionWizard } from '../fleet/QuestionWizard'
import type { ToolCall } from '../fleet/chatModel'

const noop = () => {}

/** The Chat tab's step-by-step wizard above the terminal composer. Shown only
 *  while the pane reports this call's dialog; collapsing keeps the wizard
 *  mounted so it keeps following the pane. `onInteractive` must be stable. */
export function TerminalQuestionPanel({ runId, call, onInteractive }: {
  runId: string
  call: ToolCall
  onInteractive: (interactive: boolean) => void
}) {
  const [interactive, setInteractive] = useState(false)
  const [open, setOpen] = useState(true)
  const report = useCallback((_id: string, on: boolean) => {
    setInteractive(on)
    onInteractive(on)
  }, [onInteractive])

  return (
    <div hidden={!interactive} className="terminal-question-panel">
      <button
        type="button"
        className="terminal-question-toggle"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {open ? 'Hide question' : 'Show question'}
      </button>
      <div hidden={!open} className="terminal-question-body">
        <QuestionWizard key={call.toolCallId} runId={runId} call={call} fallbackQuestions={[]} onLayout={noop} onInteractive={report} />
      </div>
    </div>
  )
}
