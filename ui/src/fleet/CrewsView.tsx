import { useMemo } from 'react'
import type { Run } from '../api/runs'
import { needsYou } from './staleness'
import { RunCard } from './RunCard'
import { ReplyComposer } from './ReplyComposer'
import './fleet.css'

interface CrewsViewProps {
  runs: Run[]
  now: number
  onOpen?: (r: Run) => void
}

interface CrewGroup {
  name: string
  members: Run[]
  blocked: number
  lastActive: number
}

// The bus carries no crew-level title, only the id — shorten it for the
// header but keep the full id reachable as the element's `title`.
function shortId(name: string): string {
  return name.length > 12 ? `${name.slice(0, 10)}…` : name
}

export function CrewsView({ runs, now, onOpen }: CrewsViewProps) {
  const groups = useMemo<CrewGroup[]>(() => {
    const byCrew = new Map<string, Run[]>()
    for (const r of runs) {
      // No crew, or a crew with no id, belongs to no group — the Fleet tab
      // already lists every run; this would just be a second fleet list.
      const name = r.crew?.name
      if (!name) continue
      const list = byCrew.get(name)
      if (list) list.push(r)
      else byCrew.set(name, [r])
    }

    const entries = Array.from(byCrew.entries()).map(([name, members]) => {
      const sorted = [...members].sort((a, b) => {
        const an = needsYou(a, now) ? 1 : 0
        const bn = needsYou(b, now) ? 1 : 0
        if (an !== bn) return bn - an
        return b.updated_at - a.updated_at
      })
      const blocked = sorted.filter((m) => needsYou(m, now)).length
      const lastActive = sorted.reduce((max, m) => Math.max(max, m.updated_at), 0)
      return { name, members: sorted, blocked, lastActive }
    })

    entries.sort((a, b) => {
      const ab = a.blocked > 0 ? 1 : 0
      const bb = b.blocked > 0 ? 1 : 0
      if (ab !== bb) return bb - ab
      return b.lastActive - a.lastActive
    })
    return entries
  }, [runs, now])

  return (
    <div className="crews mocha">
      <header className="fleet-nav">
        <h1>Crews</h1>
      </header>

      {groups.length === 0 && (
        <div className="fleet-empty">No crews yet — runs appear here once they carry a crew id.</div>
      )}

      {groups.map((g) => (
        <section key={g.name}>
          <div className="crews-group" title={g.name}>
            <span>{shortId(g.name)}</span>
            <span>
              {g.members.length} member{g.members.length === 1 ? '' : 's'}
              {g.blocked > 0 ? ` · ${g.blocked} blocked` : ''}
            </span>
          </div>
          {g.members.map((r) => (
            <div key={r.id}>
              <RunCard run={r} now={now} onOpen={onOpen} />
              {r.question?.via === 'crew' && <ReplyComposer runId={r.id} />}
            </div>
          ))}
        </section>
      ))}
    </div>
  )
}
