import { useMemo, useRef, useState } from 'react'
import type { Run } from '../api/runs'
import type { Mode } from '../api/mode'
import { agoLabel, nameLabel } from './format'
import { projectOf } from './fleetList'
import { TerminalPane } from '../components/TerminalPane'
import { isClaudeAgent, suggestedCommand } from '../components/quickCommands'
import { useRunChat } from '../hooks/useRunChat'
import { useEdgeSwipeBack } from '../hooks/useEdgeSwipeBack'
import { useTerminalLifecycle } from './useTerminalLifecycle'
import { ChatTab } from './ChatTab'
import { CrewTab } from './CrewTab'
import { RunQuestion, RunStatusStrip } from './RunStatusStrip'
import { runHash } from './routes'
import type { DetailTab } from './routes'
import './fleet.css'

interface RunDetailProps {
  runs: Run[]
  hasSnapshot: boolean
  streamConnected: boolean
  now: number
  id: string
  tab?: DetailTab
  mode: Mode | null
  onBack?: () => void
  backLabel?: string
  /** Mobile overlay only: a left-edge rightward swipe calls `onBack`. */
  edgeSwipeBack?: boolean
}

function goToFleet(): void {
  window.location.hash = '#/fleet'
}

function goToTab(id: string, tab: DetailTab): void {
  window.location.hash = runHash(id, tab)
}

/**
 * A deep-linkable run-detail overlay nested under `#/fleet`. Rendered as a
 * sibling layer over `.fleet` (see Shell.tsx) rather than in place of it, so
 * `.fleet`'s own scroll position survives a visit here and back.
 */
export function RunDetail({ runs, hasSnapshot, streamConnected, now, id, tab, mode, onBack = goToFleet, backLabel = 'Fleet', edgeSwipeBack = false }: RunDetailProps) {
  const rootRef = useRef<HTMLDivElement>(null)
  useEdgeSwipeBack(rootRef, onBack, edgeSwipeBack)

  // `hasSnapshot` distinguishes "haven't heard from the stream yet" (loading)
  // from "heard from it, this id isn't in it" (really not found) — without
  // it, every cold deep link would flash "not found" for the one tick before
  // the initial SSE snapshot lands.
  const run = hasSnapshot ? runs.find((r) => r.id === id) : undefined

  return (
    <div className="run-detail" ref={rootRef}>
      <header className="run-detail-header">
        <button type="button" className="run-detail-back" onClick={onBack} aria-label={`Back to ${backLabel}`}>
          <span aria-hidden>‹</span> {backLabel}
        </button>
        {run && (
          <div className="run-detail-heading">
            <span className="run-dot" style={{ background: `var(--state-${run.state}, var(--text-faint))` }} />
            <span className="run-detail-project">{projectOf(run)}</span>
            <span className="run-detail-name">{run.branch || nameLabel(run)}</span>
            <span className="run-chip">{run.agent}</span>
            <span className="run-age">{agoLabel(run.updated_at, now)}</span>
          </div>
        )}
      </header>

      {!hasSnapshot ? (
        <div className="run-detail-empty">Loading run…</div>
      ) : !run ? (
        <div className="run-detail-empty">
          <p>This run is no longer available.</p>
          <button type="button" className="run-detail-back-cta" onClick={onBack}>Back to {backLabel}</button>
        </div>
      ) : (
        <RunDetailBody run={run} runs={runs} tab={tab} mode={mode} streamConnected={streamConnected} now={now} onBack={onBack} backLabel={backLabel} />
      )}
    </div>
  )
}

function RunDetailBody({ run, runs, tab, mode, streamConnected, now, onBack, backLabel }: { run: Run; runs: Run[]; tab?: DetailTab; mode: Mode | null; streamConnected: boolean; now: number; onBack: () => void; backLabel: string }) {
  const capable = run.caps.terminal && Boolean(run.tmux)
  const lifecycle = useTerminalLifecycle(run.id, capable, tab === 'terminal', streamConnected)
  const chatOffered = Boolean(run.caps.chat)
  const crewOffered = mode === 'dispatcher' && run.role === 'dispatcher' && Boolean(run.crew?.name)

  // A deep link to `.../terminal` for a run that has never actually gone live
  // (never had, or already lost, terminal capability before the view ever
  // mounted it) isn't an error — it degrades to Chat (or the status card when
  // Chat isn't offered), the same as if the Terminal tab were never offered.
  // Once it *has* gone live, a later capability loss is shown explicitly
  // instead (see the terminal branch below) rather than silently falling back
  // here.
  // A dispatcher's own run opens on its Crew tab unless the route names Chat or
  // Terminal; a crew route on any other run degrades like an unknown tab.
  const effectiveTab: DetailTab =
    crewOffered && (tab === undefined || tab === 'crew')
      ? 'crew'
      : tab === 'terminal' && (run.caps.terminal || lifecycle.everLive)
        ? 'terminal'
        : 'chat'
  // useRunChat keeps its last updates once disabled, so gate the result too.
  const chatEnabled = effectiveTab === 'terminal' && chatOffered && isClaudeAgent(run.agent)
  const { updates } = useRunChat(run.id, chatEnabled)
  const suggestion = useMemo(() => (chatEnabled ? suggestedCommand(updates) : null), [chatEnabled, updates])

  return (
    <>
      <nav className="run-detail-tabs">
        <button type="button" className="run-detail-tabs-back" onClick={onBack} aria-label={`Back to ${backLabel}`}>
          <span aria-hidden>‹</span> {backLabel}
        </button>
        {crewOffered && (
          <button
            type="button"
            className={effectiveTab === 'crew' ? 'on' : ''}
            aria-pressed={effectiveTab === 'crew'}
            onClick={() => goToTab(run.id, 'crew')}
          >
            Crew
          </button>
        )}
        {chatOffered && (
          <button
            type="button"
            className={effectiveTab === 'chat' ? 'on' : ''}
            aria-pressed={effectiveTab === 'chat'}
            onClick={() => goToTab(run.id, 'chat')}
          >
            Chat
          </button>
        )}
        {run.caps.terminal && (
          <button
            type="button"
            className={effectiveTab === 'terminal' ? 'on' : ''}
            aria-pressed={effectiveTab === 'terminal'}
            onClick={() => goToTab(run.id, 'terminal')}
          >
            Terminal
          </button>
        )}
      </nav>
      <div className={`run-detail-body${effectiveTab === 'terminal' ? ' terminal' : ''}`}>
        {effectiveTab === 'crew' ? (
          <CrewTab key={run.id} run={run} runs={runs} now={now} />
        ) : effectiveTab === 'chat' ? (
          chatOffered ? <ChatTab key={run.id} run={run} now={now} /> : <RunStatusCard key={run.id} run={run} now={now} />
        ) : (
          <>
            {!chatOffered && <RunStatusCard run={run} now={now} onTerminal />}
            {!run.tmux ? (
              <div className="run-detail-empty">Terminal — coming soon.</div>
            ) : lifecycle.state === 'ended' ? (
              <div className="run-detail-empty">
                <p>Terminal session ended.</p>
                {lifecycle.endedReason && <p>{lifecycle.endedReason}</p>}
                <button type="button" className="run-detail-back-cta" onClick={lifecycle.reconnect}>Reconnect</button>
              </div>
            ) : (
              <>
                {lifecycle.reconnecting && <div className="run-detail-reconnecting" role="status">Reconnecting…</div>}
                <TerminalPane
                  key={`${run.id}-${lifecycle.attempt}`}
                  address={{ kind: 'run', id: run.id }}
                  isFocused
                  hideHeader
                  onFocus={() => {}}
                  onClose={onBack}
                  onConnectionChange={lifecycle.onConnectionChange}
                  onEnded={lifecycle.end}
                  runAgent={run.agent}
                  runState={run.state}
                  suggestion={suggestion}
                  draftKey={run.draft_key}
                />
              </>
            )}
          </>
        )}
      </div>
    </>
  )
}

function RunStatusCard({ run, now, onTerminal = false }: { run: Run; now: number; onTerminal?: boolean }) {
  const [messageOpen, setMessageOpen] = useState(false)
  const message = run.activity.message
  return (
    <div className="run-status-card">
      <RunStatusStrip run={run} now={now} />
      <RunQuestion run={run} onTerminal={onTerminal} />
      {message && message !== run.question?.text && (
        <button
          type="button"
          className={`run-status-message${messageOpen ? ' open' : ''}`}
          aria-expanded={messageOpen}
          onClick={() => setMessageOpen((o) => !o)}
        >
          <span className="run-status-message-text">{message}</span>
        </button>
      )}
    </div>
  )
}
