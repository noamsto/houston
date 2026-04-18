import { useMemo, useState } from 'react'
import type { AgentState, SessionView } from '../../api/types'
import { useAgentsStream } from '../../hooks/useAgentsStream'
import { AgentCard } from './AgentCard'
import './agents.css'

type Filter = 'all' | 'running' | 'waiting' | 'thinking' | 'idle'

/** Which AgentStates count as each filter bucket. */
const bucket: Record<Exclude<Filter, 'all'>, (s: AgentState) => boolean> = {
  running: (s) => s === 'tool-running',
  waiting: (s) => s === 'waiting' || s === 'waiting:permission',
  thinking: (s) => s === 'thinking' || s === 'starting',
  idle: (s) => s === 'ended' || s === 'compacting',
}

export function AgentsView() {
  const { list, connected } = useAgentsStream()
  const [filter, setFilter] = useState<Filter>('all')

  const counts = useMemo(() => {
    const c = { all: list.length, running: 0, waiting: 0, thinking: 0, idle: 0 }
    for (const v of list) {
      for (const k of Object.keys(bucket) as Array<keyof typeof bucket>) {
        if (bucket[k](v.state)) c[k]++
      }
    }
    return c
  }, [list])

  const visible = useMemo<SessionView[]>(() => {
    if (filter === 'all') return list
    return list.filter((v) => bucket[filter](v.state))
  }, [list, filter])

  return (
    <div className="agents-root">
      <header className="agents-nav">
        <div className="brand">
          <span className={`mark ${connected ? 'live' : 'dead'}`} aria-hidden />
          <span className="name">Houston</span>
          <span className="slash">/</span>
          <span className="tag">Mission Control</span>
        </div>
        <div className="telemetry">
          <Metric k="Running" v={counts.running} tone="run" />
          <Metric k="Awaiting" v={counts.waiting} tone="wait" />
          <Metric k="Idle" v={counts.idle} tone="idle" />
        </div>
      </header>

      <nav className="agents-filters" aria-label="filter">
        <Tab label="All"        count={counts.all}      active={filter === 'all'}      onClick={() => setFilter('all')} />
        <Tab label="Running"    count={counts.running}  active={filter === 'running'}  onClick={() => setFilter('running')} />
        <Tab label="Needs You"  count={counts.waiting}  active={filter === 'waiting'}  urgent onClick={() => setFilter('waiting')} />
        <Tab label="Thinking"   count={counts.thinking} active={filter === 'thinking'} onClick={() => setFilter('thinking')} />
        <Tab label="Idle"       count={counts.idle}     active={filter === 'idle'}     onClick={() => setFilter('idle')} />
      </nav>

      {visible.length === 0 && (
        <div className="agents-empty">
          {filter === 'all'
            ? connected
              ? 'No active Claude sessions. Run houston doctor to check hooks.'
              : 'Connecting to agent stream…'
            : `No sessions matching ${filter}.`}
        </div>
      )}

      <main className="agents-grid">
        {visible.map((v) => (
          <AgentCard key={v.session_id} view={v} />
        ))}
      </main>
    </div>
  )
}

function Metric({ k, v, tone }: { k: string; v: number; tone: string }) {
  return (
    <div className="metric">
      <span className="k">{k}</span>
      <span className={`v ${tone}`}>{v}</span>
    </div>
  )
}

function Tab({
  label, count, active, urgent, onClick,
}: {
  label: string
  count: number
  active: boolean
  urgent?: boolean
  onClick: () => void
}) {
  return (
    <button
      className={`tab ${active ? 'active' : ''} ${urgent && count > 0 ? 'urgent' : ''}`}
      onClick={onClick}
      type="button"
    >
      {label} <span className="count">{count}</span>
    </button>
  )
}
