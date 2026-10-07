import { useEffect, useMemo, useState } from 'react'
import type { Run } from '../api/runs'
import { fetchDispatchOptions } from '../api/dispatch'
import { isFresh } from './staleness'
import { agoLabel, BLOCKED_FALLBACK } from './format'
import { crewShortId } from './fleetList'
import { countsLabel, crewAttention, dispatchHref, groupCrews, type CrewGroup } from './crewsModel'
import { ReplyComposer } from './ReplyComposer'
import './fleet.css'

interface CrewsViewProps {
  runs: Run[]
  now: number
  onOpen?: (r: Run) => void
}

interface CrewRepo {
  name: string
  path: string
}

function phase(run: Run): string {
  if (run.state === 'blocked') return run.activity.message || BLOCKED_FALLBACK
  const { tool, hint } = run.activity
  if (tool) return hint ? `${tool} · ${hint}` : tool
  if (run.crew?.detail) return run.crew.detail
  return run.state
}

const ATTENTION_LABEL = { 'needs-you': 'needs you', stuck: 'stuck', done: 'done' } as const

function resolveRepo(
  group: CrewGroup,
  byCrew: Map<string, CrewRepo>,
  byName: Map<string, CrewRepo | null>,
): CrewRepo | undefined {
  return byCrew.get(group.name) ?? (group.project ? byName.get(group.project) ?? undefined : undefined)
}

function Member({ run, now, onOpen }: { run: Run; now: number; onOpen?: (r: Run) => void }) {
  const stale = !isFresh(run, now)
  const attention = crewAttention(run)
  const meta = [run.crew?.tier, run.agent, run.crew?.model].filter(Boolean).join(' · ')
  const sessions = run.crew?.sessions ?? 0
  return (
    <div className={`crews-member${attention ? ` ${attention}` : ''}${attention && stale ? ' muted' : ''}`}>
      <button type="button" className="crews-member-main" onClick={() => onOpen?.(run)}>
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

function Crew({
  group,
  repo,
  now,
  onOpen,
}: {
  group: CrewGroup
  repo: CrewRepo | undefined
  now: number
  onOpen?: (r: Run) => void
}) {
  return (
    <section className="crews-crew">
      <div className="crews-head">
        <span className="crews-repo">{repo?.name ?? group.project ?? 'unknown repo'}</span>
        <span className="crews-id" title={group.name}>
          {crewShortId(group.name)}
        </span>
        <span className="crews-counts">{countsLabel(group.counts)}</span>
        {repo && (
          <a className="crews-dispatch" href={dispatchHref(repo.path, group.name)}>
            Dispatch here
          </a>
        )}
      </div>
      {group.members.map((r) => (
        <Member key={r.id} run={r} now={now} onOpen={onOpen} />
      ))}
    </section>
  )
}

export function CrewsView({ runs, now, onOpen }: CrewsViewProps) {
  const { live, finished } = useMemo(() => groupCrews(runs, now), [runs, now])
  const [showFinished, setShowFinished] = useState(false)
  const [repos, setRepos] = useState<Map<string, CrewRepo>>(() => new Map())
  const [byName, setByName] = useState<Map<string, CrewRepo | null>>(() => new Map())

  // Optional enrichment for repo name/path. A failure is tolerated silently, and
  // it is not refetched, so a crew created later shows no repo name until reload.
  useEffect(() => {
    let cancelled = false
    fetchDispatchOptions()
      .then((opts) => {
        if (cancelled) return
        const byCrew = new Map<string, CrewRepo>()
        const names = new Map<string, CrewRepo | null>()
        for (const r of opts.repos) {
          for (const crew of r.home ?? []) byCrew.set(crew, { name: r.name, path: r.path })
          // Two repos sharing a name are ambiguous: better no link than the wrong one.
          names.set(r.name, names.has(r.name) ? null : { name: r.name, path: r.path })
        }
        setRepos(byCrew)
        setByName(names)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  const none = live.length === 0 && finished.length === 0

  return (
    <div className="crews mocha">
      <header className="fleet-nav">
        <h1>Crews</h1>
      </header>

      {none && <div className="fleet-empty">No crews yet — runs appear here once they carry a crew id.</div>}
      {!none && live.length === 0 && <div className="fleet-empty crews-none-live">No live crews.</div>}

      {live.map((g) => (
        <Crew key={g.name} group={g} repo={resolveRepo(g, repos, byName)} now={now} onOpen={onOpen} />
      ))}

      {finished.length > 0 && (
        <>
          <button
            type="button"
            className="crews-finished-toggle"
            aria-expanded={showFinished}
            onClick={() => setShowFinished((v) => !v)}
          >
            Finished ({finished.length})
          </button>
          {showFinished &&
            finished.map((g) => <Crew key={g.name} group={g} repo={resolveRepo(g, repos, byName)} now={now} onOpen={onOpen} />)}
        </>
      )}
    </div>
  )
}
