// Mirror of server/workspace.go's Workspace contract. A discriminated union
// on `agent` so the type system enforces what the Go server guarantees: an
// agent pane always carries a run_id.
export type WorkspacePane =
  | { id: string; index: number; active: boolean; command: string; agent: true; run_id: string }
  | { id: string; index: number; active: boolean; command: string; agent: false; run_id?: undefined }

export interface WorkspaceWindow {
  index: number
  name: string
  active: boolean
  branch?: string
  task?: string
  crew_codename?: string
  panes: WorkspacePane[]
}

export interface WorkspaceSession {
  name: string
  window_count: number
  main_checkout?: WorkspaceWindow[]
  worktrees?: WorkspaceWindow[]
  other?: WorkspaceWindow[]
}

export interface Workspace {
  host: string
  sessions: WorkspaceSession[]
}

export async function fetchWorkspace(): Promise<Workspace> {
  const res = await fetch('/api/workspace')
  if (!res.ok) {
    throw new Error(`fetchWorkspace: ${res.status} ${await res.text()}`)
  }
  return (await res.json()) as Workspace
}
