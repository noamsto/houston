import { useEffect, useMemo, useState } from 'react'
import { useRuns } from '../hooks/useRuns'
import { needsYou } from './staleness'
import { FleetView } from './FleetView'
import './fleet.css'

type Tab = 'fleet' | 'crews' | 'workspace' | 'dispatch'

const PLACEHOLDER: Record<Exclude<Tab, 'fleet'>, string> = {
  crews: 'Crews arrives with the dispatcher milestone — crew grouping, tier and PR badges, and answering a blocked worker from here.',
  workspace: 'Workspace arrives next — the tmux tree across every host, including panes that are not agents.',
  dispatch: 'Dispatch arrives with the dispatcher milestone — pick a repo, task, tier and engine, and start work from your phone.',
}

export function Shell() {
  const [tab, setTab] = useState<Tab>('fleet')
  const { runs } = useRuns()
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(id)
  }, [])
  const attention = useMemo(() => runs.some((r) => needsYou(r, now)), [runs, now])

  return (
    <div className="shell mocha">
      <div className="shell-body">
        {tab === 'fleet' ? <FleetView /> : <div className="shell-placeholder">{PLACEHOLDER[tab]}</div>}
      </div>

      <nav className="shell-tabs" aria-label="sections">
        <button className={tab === 'fleet' ? 'on' : ''} onClick={() => setTab('fleet')}>
          <span className="glyph" aria-hidden>▤</span>
          Fleet
          {attention && tab !== 'fleet' && <span className="dot" />}
        </button>
        <button className={tab === 'crews' ? 'on' : ''} onClick={() => setTab('crews')}>
          <span className="glyph" aria-hidden>◆</span>Crews
        </button>
        <button className={tab === 'workspace' ? 'on' : ''} onClick={() => setTab('workspace')}>
          <span className="glyph" aria-hidden>▣</span>Workspace
        </button>
        <button className={tab === 'dispatch' ? 'on' : ''} onClick={() => setTab('dispatch')}>
          <span className="glyph" aria-hidden>✦</span>Dispatch
        </button>
      </nav>
    </div>
  )
}
