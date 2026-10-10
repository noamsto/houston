import { memo, useMemo } from 'react'
import type { Run } from '../api/runs'
import type { FeedEntry, FeedKind } from '../api/crewFeed'
import { useCrewFeed } from '../hooks/useCrewFeed'
import { runHash } from './routes'
import './fleet.css'

const KIND_LABEL: Record<Exclude<FeedKind, 'status'>, string> = {
  dispatch: 'dispatched',
  resume: 'resumed',
  question: 'question',
  'follow-ups': 'follow-ups',
  reply: 'reply',
  pr: 'PR',
  reap: 'reaped',
}

// The server only emits URLs of this shape; the client does not trust that.
const PR_URL = /^https:\/\/github\.com\/[A-Za-z0-9-]+\/[A-Za-z0-9._-]+\/pull\/[0-9]+$/

function kindLabel(e: FeedEntry): string {
  return e.kind === 'status' ? e.state || 'status' : KIND_LABEL[e.kind]
}

function clock(ts: number): string {
  const d = new Date(ts)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

interface FeedRowProps {
  entry: FeedEntry
  workerId?: string
  workerCodename?: string
}

const FeedRow = memo(function FeedRow({ entry, workerId, workerCodename }: FeedRowProps) {
  const who = entry.codename || workerCodename || entry.branch
  const prUrl = entry.pr && PR_URL.test(entry.pr.url) ? entry.pr.url : undefined
  return (
    <li className="crew-feed-row">
      <span className="crew-feed-time" title={new Date(entry.ts).toLocaleString()}>{clock(entry.ts)}</span>
      <span className="crew-feed-kind">{kindLabel(entry)}</span>
      <span className="crew-feed-text">{entry.text}</span>
      <span className="crew-feed-chips">
        {who &&
          (workerId ? (
            <a className="run-chip crew-feed-link" href={runHash(workerId)}>{who}</a>
          ) : (
            <span className="run-chip">{entry.codename || entry.branch}</span>
          ))}
        {entry.pr && prUrl && (
          <a className="run-chip pr crew-feed-pr" href={prUrl} target="_blank" rel="noreferrer" aria-label={`Pull request #${entry.pr.number}`}>
            #{entry.pr.number}
          </a>
        )}
      </span>
    </li>
  )
})

interface CrewFeedListProps {
  runId: string
  crew: string
  runs: Run[]
}

export function CrewFeedList({ runId, crew, runs }: CrewFeedListProps) {
  const { status, entries, more, loadOlder, retry } = useCrewFeed(runId, true)

  const workers = useMemo(() => {
    const byBranch = new Map<string, Run>()
    for (const r of runs) {
      if (r.role === 'worker' && r.crew?.name === crew && r.branch) byBranch.set(r.branch, r)
    }
    return byBranch
  }, [runs, crew])

  const newestFirst = useMemo(() => [...entries].reverse(), [entries])

  return (
    <section className="crew-feed-section" aria-label="Crew activity">
      <h2 className="crew-feed-title">Activity</h2>
      {status === 'loading' && <div className="crew-feed-note">Loading crew activity…</div>}
      {status === 'unavailable' && <div className="crew-feed-note">Crew activity is unavailable.</div>}
      {status === 'error' && (
        <div className="crew-feed-note" role="alert">
          Could not load crew activity.
          <button type="button" onClick={retry}>Retry</button>
        </div>
      )}
      {status === 'ready' && entries.length === 0 && <div className="crew-feed-note">No crew activity yet.</div>}
      {newestFirst.length > 0 && (
        <ul className="crew-feed">
          {newestFirst.map((e) => {
            const worker = e.branch ? workers.get(e.branch) : undefined
            return <FeedRow key={e.id} entry={e} workerId={worker?.id} workerCodename={worker?.crew?.codename} />
          })}
        </ul>
      )}
      {more && (
        <button type="button" className="crew-feed-more" onClick={() => void loadOlder()}>Load older</button>
      )}
    </section>
  )
}
