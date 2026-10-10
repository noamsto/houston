import { useMemo } from 'react'
import type { Run } from '../api/runs'
import { agoLabel, BLOCKED_FALLBACK } from './format'
import { crewShortId } from './fleetList'
import { bucket, countsLabel, dispatchHref, type CrewCounts } from './crewsModel'
import { CrewFeedList } from './CrewFeedList'
import { ReplyComposer } from './ReplyComposer'
import { runHash } from './routes'
import { isFresh, isHistory, needsYou } from './staleness'
import { resolveCrewRepo, useCrewRepos } from './useCrewRepos'
import './fleet.css'

interface CrewTabProps {
  run: Run
  runs: Run[]
  now: number
}

function phase(run: Run): string {
  if (run.state === 'blocked') return run.activity.message || BLOCKED_FALLBACK
  const { tool, hint } = run.activity
  // A tool lingers after a dismissed permission dialog, so it only counts while working.
  const working = run.state === 'running' || run.state === 'thinking' || run.state === 'compacting'
  if (working && tool) return hint ? `${tool} · ${hint}` : tool
  if (run.crew?.detail) return run.crew.detail
  return run.state
}

const ATTENTION_LABEL = { 'needs-you': 'needs you', stuck: 'stuck', done: 'done' } as const

function openRun(r: Run): void {
  window.location.hash = runHash(r.id)
}

function Member({ run, now }: { run: Run; now: number }) {
  const stale = !isFresh(run, now)
  const attention = run.attention
  const meta = [run.crew?.tier, run.agent, run.crew?.model].filter(Boolean).join(' · ')
  const sessions = run.crew?.sessions ?? 0
  return (
    <div className={`crews-member${attention ? ` ${attention}` : ''}${attention && stale ? ' muted' : ''}`}>
      <button type="button" className="crews-member-main" onClick={() => openRun(run)}>
        <span className="crews-member-head">
          <span className="crews-swatch" style={{ background: run.crew?.color || 'var(--text-faint)' }} />
          <span className="crews-codename">{run.crew?.codename || 'worker'}</span>
          <span className="crews-when">
            {!attention && run.state === 'idle' && <span className="run-chip crews-idle">idle</span>}
            {attention && <span className={`run-chip crews-attention ${attention}`}>{ATTENTION_LABEL[attention]}</span>}
            <span className={`run-age${stale ? ' stale' : ''}`}>{agoLabel(run.updated_at, now)}</span>
          </span>
        </span>
        <span className="crews-title">{run.crew?.title || run.issue?.title || run.branch}</span>
        <span className="crews-meta">{meta}</span>
        {!(run.question && phase(run) === run.question.text) && <span className="run-sub crews-phase">{phase(run)}</span>}
        {run.question && <span className="run-question crews-question">{run.question.text}</span>}
        {attention === 'stuck' && run.attention_note && <span className="crews-note">{run.attention_note}</span>}
      </button>
      <span className="run-chips crews-chips">
        {run.pr &&
          (run.pr.url ? (
            <a
              className={`run-chip pr crews-pr${run.pr.check_state === 'failure' ? ' failing' : ''}`}
              href={run.pr.url}
              target="_blank"
              rel="noreferrer"
              aria-label={`Pull request #${run.pr.number}`}
            >
              #{run.pr.number}
            </a>
          ) : (
            <span className={`run-chip pr${run.pr.check_state === 'failure' ? ' failing' : ''}`}>#{run.pr.number}</span>
          ))}
        {sessions > 1 && <span className="run-chip crews-sessions">{sessions} sessions</span>}
        {run.stale === true && <span className="run-chip stale">stale</span>}
      </span>
      {run.question?.via === 'crew' && <ReplyComposer runId={run.id} />}
      {run.question?.via === 'pane' && <div className="crews-hint">Answer in the terminal — tap to open.</div>}
    </div>
  )
}

function rank(run: Run, now: number): number {
  if (needsYou(run, now)) return 2
  return isHistory(run, now) ? 0 : 1
}

export function CrewTab({ run, runs, now }: CrewTabProps) {
  const crew = run.crew?.name ?? ''
  const host = run.host || 'local'
  const repo = resolveCrewRepo(crew, run.project, useCrewRepos(true))

  const members = useMemo(
    () =>
      runs
        .filter((r) => r.role === 'worker' && r.crew?.name === crew && (r.host || 'local') === host)
        .sort((a, b) => rank(b, now) - rank(a, now) || b.updated_at - a.updated_at),
    [runs, crew, host, now],
  )
  const counts = useMemo(() => {
    const c: CrewCounts = { working: 0, needsYou: 0, stuck: 0, done: 0, idle: 0, ended: 0 }
    for (const m of members) c[bucket(m, now)]++
    return c
  }, [members, now])

  return (
    <div className="crew-tab">
      <div className="crews-head">
        <span className="crews-repo">{repo?.name ?? run.project ?? 'unknown repo'}</span>
        <span className="crews-id" title={crew}>
          {crewShortId(crew)}
        </span>
        <span className="crews-counts">{countsLabel(counts)}</span>
        {repo && (
          <a className="crews-dispatch" href={dispatchHref(repo.path, crew)}>
            Dispatch here
          </a>
        )}
      </div>
      {members.map((m) => (
        <Member key={m.id} run={m} now={now} />
      ))}
      <CrewFeedList runId={run.id} crew={crew} runs={runs} />
    </div>
  )
}
