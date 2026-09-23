import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { Run } from '../api/runs'
import { collapseTrail } from './activityTimeline'
import { agoLabel, nameLabel, subtitle } from './format'
import { projectOf } from './fleetList'
import { TerminalPane } from '../components/TerminalPane'
import { useTerminalLifecycle } from './useTerminalLifecycle'
import { ReplyComposer } from './ReplyComposer'
import './fleet.css'

type Tab = 'activity' | 'terminal'

interface RunDetailProps {
  runs: Run[]
  hasSnapshot: boolean
  streamConnected: boolean
  now: number
  id: string
  tab: Tab
  onBack?: () => void
  backLabel?: string
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

function RunDetailBody({ run, tab, streamConnected, now, onBack, backLabel }: { run: Run; tab: Tab; streamConnected: boolean; now: number; onBack: () => void; backLabel: string }) {
  const capable = run.caps.terminal && Boolean(run.tmux)
  const lifecycle = useTerminalLifecycle(run.id, capable, tab === 'terminal', streamConnected)

  // A deep link to `.../terminal` for a run that has never actually gone live
  // (never had, or already lost, terminal capability before the view ever
  // mounted it) isn't an error — it degrades to Activity, the same as if the
  // Terminal tab were never offered. Once it *has* gone live, a later
  // capability loss is shown explicitly instead (see the terminal branch
  // below) rather than silently falling back here.
  const effectiveTab: Tab = tab === 'terminal' && !run.caps.terminal && !lifecycle.everLive ? 'activity' : tab

  return (
    <>
      <nav className="run-detail-tabs">
        <button type="button" className="run-detail-tabs-back" onClick={onBack} aria-label={`Back to ${backLabel}`}>
          <span aria-hidden>‹</span> {backLabel}
        </button>
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
      <div className={`run-detail-body${effectiveTab === 'terminal' ? ' terminal' : ''}`}>
        {effectiveTab === 'activity' ? (
          <ActivityTab key={run.id} run={run} now={now} />
        ) : !run.tmux ? (
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
      </div>
    </>
  )
}

const TRAIL_WINDOW = 10
const STICK_SLOP_PX = 24

function ActivityTab({ run, now }: { run: Run; now: number }) {
  const a = run.activity
  const rows = useMemo(() => collapseTrail(a.trail), [a.trail])
  const [visible, setVisible] = useState(TRAIL_WINDOW)
  const [messageOpen, setMessageOpen] = useState(false)
  const [previewOpen, setPreviewOpen] = useState(false)
  const [stuck, setStuck] = useState(true)
  const scroller = useRef<HTMLDivElement>(null)
  const stuckRef = useRef(true)
  const anchor = useRef<{ height: number; top: number } | null>(null)

  const setStick = (v: boolean) => {
    stuckRef.current = v
    setStuck(v)
  }

  const onScroll = () => {
    const el = scroller.current
    if (!el) return
    setStick(el.scrollHeight - el.scrollTop - el.clientHeight < STICK_SLOP_PX)
  }

  const showEarlier = () => {
    const el = scroller.current
    if (el) anchor.current = { height: el.scrollHeight, top: el.scrollTop }
    setStick(false)
    setVisible((v) => v + TRAIL_WINDOW)
  }

  const scrollToLatest = () => {
    const el = scroller.current
    if (el) el.scrollTop = el.scrollHeight
    setStick(true)
  }

  useLayoutEffect(() => {
    const el = scroller.current
    if (!el || !anchor.current) return
    el.scrollTop = anchor.current.top + (el.scrollHeight - anchor.current.height)
    anchor.current = null
  }, [visible])

  useLayoutEffect(() => {
    const el = scroller.current
    if (el && stuckRef.current) el.scrollTop = el.scrollHeight
  }, [rows, a.message, messageOpen, previewOpen, visible, run.question])

  const shown = rows.slice(-visible)
  const hidden = rows.length - shown.length

  return (
    <div className="activity">
      <div className="activity-glance">
        <span className="run-dot" style={{ background: `var(--state-${run.state}, var(--text-faint))` }} />
        {!(run.question && subtitle(run) === run.question.text) && (
          <span className="activity-status">{subtitle(run)}</span>
        )}
        {typeof a.turn === 'number' && <span className="activity-meta">Turn {a.turn}</span>}
        <span className="activity-meta">{agoLabel(run.updated_at, now)}</span>
      </div>

      <div className="activity-timeline-wrap">
        <div className="activity-timeline" ref={scroller} onScroll={onScroll}>
          {hidden > 0 && (
            <button type="button" className="activity-earlier" onClick={showEarlier}>
              Show earlier ({hidden})
            </button>
          )}

          {shown.map((row, i) => (
            <div
              key={`${row.tool}-${hidden + i}`}
              className={`activity-row${row.done ? ' done' : ''}${row.error ? ' error' : ''}`}
            >
              <span className="activity-tool">{row.tool}</span>
              {row.hint && <span className="activity-hint">{row.hint}</span>}
              {row.count > 1 && <span className="activity-count">×{row.count}</span>}
            </div>
          ))}

          {a.message && !(run.question && a.message === run.question.text) && (
            <button
              type="button"
              className={`activity-message${messageOpen ? ' open' : ''}`}
              aria-expanded={messageOpen}
              onClick={() => setMessageOpen((o) => !o)}
            >
              <span className="activity-message-text">{a.message}</span>
            </button>
          )}

          {a.preview && (
            <>
              <button
                type="button"
                className="activity-preview-toggle"
                aria-expanded={previewOpen}
                onClick={() => setPreviewOpen((o) => !o)}
              >
                Terminal excerpt {previewOpen ? '▾' : '▸'}
              </button>
              {previewOpen && <pre className="activity-preview">{a.preview}</pre>}
            </>
          )}
        </div>

        {!stuck && (
          <button type="button" className="activity-latest" onClick={scrollToLatest}>
            ↓ Latest
          </button>
        )}
      </div>

      {run.question && <div className="activity-question">{run.question.text}</div>}
      {run.question && run.state === 'blocked' && run.question.via === 'crew' && <ReplyComposer runId={run.id} />}
      {run.question && run.state === 'blocked' && run.question.via === 'pane' && run.caps.terminal && (
        <button type="button" className="activity-question-reply" onClick={() => goToTab(run.id, 'terminal')}>
          Reply in Terminal
        </button>
      )}
    </div>
  )
}
