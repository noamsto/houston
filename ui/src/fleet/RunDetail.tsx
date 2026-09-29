import { useState } from 'react'
import type { Run } from '../api/runs'
import { agoLabel, nameLabel } from './format'
import { projectOf } from './fleetList'
import { TerminalPane } from '../components/TerminalPane'
import { useTerminalLifecycle } from './useTerminalLifecycle'
import { ChatTab } from './ChatTab'
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
  onBack?: () => void
  backLabel?: string
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
export function RunDetail({ runs, hasSnapshot, streamConnected, now, id, tab, onBack = goToFleet, backLabel = 'Fleet' }: RunDetailProps) {
  // `hasSnapshot` distinguishes "haven't heard from the stream yet" (loading)
  // from "heard from it, this id isn't in it" (really not found) — without
  // it, every cold deep link would flash "not found" for the one tick before
  // the initial SSE snapshot lands.
  const run = hasSnapshot ? runs.find((r) => r.id === id) : undefined

  return (
    <div className="run-detail">
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
        <RunDetailBody run={run} tab={tab} streamConnected={streamConnected} now={now} onBack={onBack} backLabel={backLabel} />
      )}
    </div>
  )
}

function RunDetailBody({ run, tab, streamConnected, now, onBack, backLabel }: { run: Run; tab?: DetailTab; streamConnected: boolean; now: number; onBack: () => void; backLabel: string }) {
  const capable = run.caps.terminal && Boolean(run.tmux)
  const lifecycle = useTerminalLifecycle(run.id, capable, tab === 'terminal', streamConnected)
  const chatOffered = Boolean(run.caps.chat)

  // A deep link to `.../terminal` for a run that has never actually gone live
  // (never had, or already lost, terminal capability before the view ever
  // mounted it) isn't an error — it degrades to Chat (or the status card when
  // Chat isn't offered), the same as if the Terminal tab were never offered.
  // Once it *has* gone live, a later capability loss is shown explicitly
  // instead (see the terminal branch below) rather than silently falling back
  // here.
  const effectiveTab: DetailTab = tab === 'terminal' && (run.caps.terminal || lifecycle.everLive) ? 'terminal' : 'chat'

  return (
    <>
      <nav className="run-detail-tabs">
        <button type="button" className="run-detail-tabs-back" onClick={onBack} aria-label={`Back to ${backLabel}`}>
          <span aria-hidden>‹</span> {backLabel}
        </button>
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
        {effectiveTab === 'chat' ? (
          chatOffered ? <ChatTab key={run.id} run={run} now={now} /> : <RunStatusCard key={run.id} run={run} now={now} />
        ) : (
          <>
            {!chatOffered && <RunStatusCard run={run} now={now} />}
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
                />
              </>
            )}
          </>
        )}
      </div>
    </>
  )
}

function RunStatusCard({ run, now }: { run: Run; now: number }) {
  const [messageOpen, setMessageOpen] = useState(false)
  const message = run.activity.message
  return (
    <div className="run-status-card">
      <RunStatusStrip run={run} now={now} />
      <RunQuestion run={run} />
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
