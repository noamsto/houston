// Correlation already happened server-side (C2/C3) — the tree the API
// returns already carries run ids on the panes that have them, so unlike
// FleetView/CrewsView this view never consumes useRuns(); see
// docs/superpowers/plans/2026-09-10-workspace-tab.md, C5.
import { useWorkspace } from './useWorkspace'
import type { WorkspaceWindow } from '../api/workspace'
import './fleet.css'

interface WorkspaceViewProps {
  onOpen?: (runId: string) => void
}

function renderWindows(windows: WorkspaceWindow[], onOpen?: (runId: string) => void) {
  return windows.map((win) => (
    <div key={win.index}>
      <div className="ws-window">
        <span>{win.index}: {win.name}</span>
        {win.branch && <span className="run-chip">{win.branch}</span>}
        {win.crew_codename && <span className="run-chip codename">{win.crew_codename}</span>}
      </div>
      {win.panes.map((pane) =>
        pane.agent ? (
          <button
            key={pane.id}
            type="button"
            className="ws-pane agent"
            onClick={() => onOpen?.(pane.run_id)}
          >
            {pane.active && <span className="ws-pane-dot" />}
            <span>{pane.command}</span>
          </button>
        ) : (
          <div key={pane.id} className="ws-pane">
            {pane.active && <span className="ws-pane-dot" />}
            <span>{pane.command}</span>
          </div>
        ),
      )}
    </div>
  ))
}

function renderBucket(label: string, windows: WorkspaceWindow[] | undefined, onOpen?: (runId: string) => void) {
  if (!windows || windows.length === 0) return null
  return (
    <div key={label}>
      <div className="ws-bucket">{label}</div>
      {renderWindows(windows, onOpen)}
    </div>
  )
}

export function WorkspaceView({ onOpen }: WorkspaceViewProps) {
  const { workspace, error, loading } = useWorkspace()

  return (
    <div className="workspace mocha">
      <header className="fleet-nav">
        <h1>Workspace</h1>
      </header>

      {loading && <div className="fleet-empty">Loading workspace…</div>}

      {!loading && error && !workspace && (
        <div className="fleet-empty">{error}</div>
      )}

      {!loading && workspace && workspace.sessions.length === 0 && (
        <div className="fleet-empty">No tmux sessions found.</div>
      )}

      {workspace?.sessions.map((session) => (
        <section key={session.name}>
          <div className="fleet-group">
            <span>{session.name}</span>
            <span>{session.window_count}</span>
          </div>
          {renderBucket('Main checkout', session.main_checkout, onOpen)}
          {renderBucket(`Worktrees (${session.worktrees?.length ?? 0})`, session.worktrees, onOpen)}
          {renderBucket('Other', session.other, onOpen)}
        </section>
      ))}
    </div>
  )
}
