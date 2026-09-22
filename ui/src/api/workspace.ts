// Mirror of server/workspace.go's Workspace contract. A discriminated union
// on `agent` so the type system enforces what the Go server guarantees: an
// agent pane always carries a run_id.
interface WorkspacePaneBase {
  id: string
  index: number
  active: boolean
  command: string
}

export type WorkspacePane =
  | (WorkspacePaneBase & {
      agent: true
      run_id: string
      agent_type?: string
      state?: string
      updated_at?: number
      detail?: string
      stale?: boolean
    })
  | (WorkspacePaneBase & { agent: false; run_id?: undefined })

export interface WorkspaceWindow {
  index: number
  name: string
  active: boolean
  session: string
  branch?: string
  task?: string
  crew_codename?: string
  issue_id?: string
  pr_number?: string
  pr_state?: string
  pr_check_state?: string
  panes: WorkspacePane[]
}

export interface WorkspaceProject {
  name: string
  window_count: number
  main_checkout?: WorkspaceWindow[]
  worktrees?: WorkspaceWindow[]
  other?: WorkspaceWindow[]
}

export interface Workspace {
  host: string
  projects: WorkspaceProject[]
}

export async function fetchWorkspace(): Promise<Workspace> {
  const res = await fetch('/api/workspace')
  if (!res.ok) {
    throw new Error(`fetchWorkspace: ${res.status} ${await res.text()}`)
  }
  return (await res.json()) as Workspace
}
