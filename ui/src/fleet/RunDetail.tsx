import type { Run } from '../api/runs'
import { paneWsTarget } from '../api/runs'
import { agoLabel, nameLabel, subtitle } from './format'
import { TerminalPane } from '../components/TerminalPane'
import './fleet.css'

type Tab = 'activity' | 'terminal'

interface RunDetailProps {
  runs: Run[]
  hasSnapshot: boolean
  now: number
  id: string
  tab: Tab
}

function goToFleet(): void {
  window.location.hash = '#/fleet'
}

function goToTab(id: string, tab: Tab): void {
  window.location.hash = `#/fleet/${id}/${tab}`
}

/**
 * A deep-linkable run-detail overlay nested under `#/fleet`. Rendered as a
 * sibling layer over `.fleet` (see Shell.tsx) rather than in place of it, so
 * `.fleet`'s own scroll position survives a visit here and back.
 */
export function RunDetail({ runs, hasSnapshot, now, id, tab }: RunDetailProps) {
  // `hasSnapshot` distinguishes "haven't heard from the stream yet" (loading)
  // from "heard from it, this id isn't in it" (really not found) — without
  // it, every cold deep link would flash "not found" for the one tick before
  // the initial SSE snapshot lands.
  const run = hasSnapshot ? runs.find((r) => r.id === id) : undefined

  return (
    <div className="run-detail">
      <header className="run-detail-header">
        <button type="button" className="run-detail-back" onClick={goToFleet} aria-label="Back to Fleet">
          <span aria-hidden>‹</span> Fleet
        </button>
        {run && (
          <div className="run-detail-heading">
            <span className="run-dot" style={{ background: `var(--state-${run.state}, var(--text-faint))` }} />
            <span className="run-detail-name">{nameLabel(run)}</span>
            <span className="run-age">{agoLabel(run.updated_at, now)}</span>
          </div>
        )}
      </header>

      {!hasSnapshot ? (
        <div className="run-detail-empty">Loading run…</div>
      ) : !run ? (
        <div className="run-detail-empty">
          <p>This run is no longer available.</p>
          <button type="button" className="run-detail-back-cta" onClick={goToFleet}>Back to Fleet</button>
        </div>
      ) : (
        <RunDetailBody run={run} tab={tab} />
      )}
    </div>
  )
}

function RunDetailBody({ run, tab }: { run: Run; tab: Tab }) {
  // A deep link to `.../terminal` for a run that has since lost (or never
  // had) terminal capability isn't an error — it degrades to Activity, the
  // same as if the Terminal tab were never offered.
  const effectiveTab: Tab = tab === 'terminal' && !run.caps.terminal ? 'activity' : tab

  return (
    <>
      <nav className="run-detail-tabs">
        <button
          type="button"
          className={effectiveTab === 'activity' ? 'on' : ''}
          aria-pressed={effectiveTab === 'activity'}
          onClick={() => goToTab(run.id, 'activity')}
        >
          Activity
        </button>
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
      <div className="run-detail-body">
        {effectiveTab === 'activity' ? (
          <ActivityTab run={run} />
        ) : run.tmux ? (
          <TerminalPane
            pane={{ id: run.id, target: paneWsTarget(run.tmux.pane_id) }}
            isFocused
            readOnly
            onFocus={() => {}}
            onClose={goToFleet}
          />
        ) : (
          <div className="run-detail-empty">Terminal — coming soon.</div>
        )}
      </div>
    </>
  )
}

function ActivityTab({ run }: { run: Run }) {
  const a = run.activity
  return (
    <div className="run-detail-activity">
      <div className="run-detail-line">{subtitle(run)}</div>

      {run.question && <div className="run-question">{run.question.text}</div>}

      {a.trail && a.trail.length > 0 && (
        <div className="run-detail-trail">
          {a.trail.map((t, i) => (
            <span key={`${t.tool}-${i}`} className={`run-chip trail${t.done ? ' done' : ''}${t.error ? ' error' : ''}`}>
              {t.hint ? `${t.tool} · ${t.hint}` : t.tool}
            </span>
          ))}
        </div>
      )}

      {a.preview && <pre className="run-detail-preview">{a.preview}</pre>}

      {a.message && <div className="run-detail-message">{a.message}</div>}

      {typeof a.turn === 'number' && <div className="run-detail-turn">Turn {a.turn}</div>}
    </div>
  )
}
