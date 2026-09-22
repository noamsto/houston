// Run state arrives joined onto the tree, so this view needs no runs prop.
import { useWorkspace } from './useWorkspace'
import { useNow } from './useNow'
import { agoLabel } from './format'
import { isFresh, needsYou } from './staleness'
import type { WorkspacePane, WorkspaceProject, WorkspaceWindow } from '../api/workspace'
import './fleet.css'

interface WorkspaceViewProps {
  onOpen?: (runId: string) => void
}

type AgentPane = Extract<WorkspacePane, { agent: true }>

const freshness = (p: AgentPane) => ({ state: p.state ?? '', updated_at: p.updated_at ?? 0 })

function windows(project: WorkspaceProject): WorkspaceWindow[] {
  return [...(project.main_checkout ?? []), ...(project.worktrees ?? []), ...(project.other ?? [])]
}

function agentPanes(project: WorkspaceProject): AgentPane[] {
  return windows(project).flatMap((w) => w.panes.filter((p): p is AgentPane => p.agent))
}

/** "3 panes · fish, bash" — the panes with no run, collapsed to one quiet line. */
function plainSummary(panes: WorkspacePane[]): string {
  const cmds = [...new Set(panes.map((p) => p.command))]
  return `${panes.length} ${panes.length === 1 ? 'pane' : 'panes'} · ${cmds.join(', ')}`
}

function PaneRow({ pane, now, onOpen }: { pane: AgentPane; now: number; onOpen?: (runId: string) => void }) {
  const attention = needsYou(freshness(pane), now)
  const aged = !isFresh(freshness(pane), now)
  const state = pane.state ?? ''

  return (
    <button
      type="button"
      className={`ws-pane agent${attention ? ' attention' : ''}`}
      onClick={() => onOpen?.(pane.run_id)}
    >
      <span className="run-dot" style={{ background: `var(--state-${state}, var(--text-faint))` }} />
      <span className="ws-pane-main">
        <span className="ws-pane-head">
          <span className="ws-pane-agent">{pane.agent_type || pane.command}</span>
          {state && <span className="ws-pane-state">{state}</span>}
          {pane.stale && <span className="run-chip stale">stale</span>}
          {pane.updated_at ? (
            <span className={`run-age${aged ? ' stale' : ''}`}>{agoLabel(pane.updated_at, now)}</span>
          ) : null}
        </span>
        {pane.detail && pane.detail !== state && <span className="ws-pane-detail">{pane.detail}</span>}
      </span>
    </button>
  )
}

function WindowBlock({ win, now, onOpen }: { win: WorkspaceWindow; now: number; onOpen?: (runId: string) => void }) {
  const agents = win.panes.filter((p): p is AgentPane => p.agent)
  const plain = win.panes.filter((p) => !p.agent)

  return (
    <div>
      <div className="ws-window">
        <span className="ws-window-title">{win.branch || win.name}</span>
        <span className="ws-window-session">{win.session}</span>
        {win.crew_codename && <span className="run-chip codename">{win.crew_codename}</span>}
        {win.issue_id && <span className="run-chip issue">{win.issue_id}</span>}
        {win.pr_number && (
          <span className={`run-chip pr${win.pr_check_state === 'failure' ? ' failing' : ''}`}>
            #{win.pr_number}{win.pr_state === 'merged' || win.pr_state === 'closed' ? ` ${win.pr_state}` : ''}
          </span>
        )}
      </div>
      {agents.map((pane) => (
        <PaneRow key={pane.id} pane={pane} now={now} onOpen={onOpen} />
      ))}
      {plain.length > 0 && <div className="ws-plain">{plainSummary(plain)}</div>}
    </div>
  )
}

function Bucket({ label, wins, now, onOpen }: { label: string; wins?: WorkspaceWindow[]; now: number; onOpen?: (runId: string) => void }) {
  if (!wins || wins.length === 0) return null
  return (
    <div>
      <div className="ws-bucket">{label}</div>
      {wins.map((w) => (
        <WindowBlock key={`${w.session}:${w.index}`} win={w} now={now} onOpen={onOpen} />
      ))}
    </div>
  )
}

export function WorkspaceView({ onOpen }: WorkspaceViewProps) {
  const { workspace, error, loading, refreshing, refresh } = useWorkspace()
  const now = useNow()

  return (
    <div className="workspace mocha">
      <header className="fleet-nav">
        <h1>Workspace</h1>
      </header>

      {loading && <div className="fleet-empty">Loading workspace…</div>}

      {!loading && error && !workspace && (
        <div className="fleet-empty">
          <div>{error}</div>
          <button type="button" className="ws-retry" onClick={refresh} disabled={refreshing}>Retry</button>
        </div>
      )}

      {error && workspace && (
        <div className="ws-banner" role="alert">
          <span>Showing last known data — {error}</span>
          <button type="button" className="ws-retry" onClick={refresh} disabled={refreshing}>Retry</button>
        </div>
      )}

      {!loading && workspace && workspace.projects.length === 0 && (
        <div className="fleet-empty">No tmux sessions found.</div>
      )}

      {workspace?.projects.map((project) => {
        const agents = agentPanes(project)
        const waiting = agents.filter((p) => needsYou(freshness(p), now)).length
        return (
          <section key={project.name}>
            <div className="fleet-group">
              <span>{project.name === '' ? 'Other sessions' : project.name}</span>
              <span className="ws-session-meta">
                {waiting > 0 && <span className="ws-need">{waiting} need you</span>}
                <span>{agents.length} {agents.length === 1 ? 'agent' : 'agents'}</span>
              </span>
            </div>
            <Bucket label="Main checkout" wins={project.main_checkout} now={now} onOpen={onOpen} />
            <Bucket label={`Worktrees (${project.worktrees?.length ?? 0})`} wins={project.worktrees} now={now} onOpen={onOpen} />
            <Bucket label="Other" wins={project.other} now={now} onOpen={onOpen} />
          </section>
        )
      })}
    </div>
  )
}
