// Mirror of server/dispatch.go's dispatchOptions/dispatchRequest contract.
export interface DispatchRepo {
  path: string
  name: string
  crews: string[] // newest first, never empty-vs-missing ambiguity: always an array
  home?: string[] // subset of crews whose dispatcher pane lives in this repo — see server/dispatch.go's dispatchHomeCrews
  registered?: boolean
}

export interface DispatchOptions {
  repos: DispatchRepo[]
  tiers: string[]
  efforts: string[]
  plans: string[]
  engines: Record<string, string[]>
  engine_order: string[] // display order; engines is a map and has none of its own
  tier_models: Record<string, Record<string, string>> // default model per engine+tier
  repo_roots?: string[]
  dispatcher_engines?: string[]
  dispatcher_engines_error?: string
}

export const NEW_CREW = 'new'

export interface DispatchRequest {
  repo: string
  title: string
  spec?: string
  tier: string
  engine: string
  model: string
  effort: string
  plan?: string // server defaults to "required" when omitted; the form never sets it
  crew: string
  issue?: string
}

// Mirror of the POST /api/dispatch response contract. `failed` covers every
// non-200 status: `error` is dispatch's own stderr on 422, or
// houston's own reason otherwise, always shown verbatim.
export type DispatchOutcome =
  | { kind: 'started'; workerId: string; branch: string; issueUrl?: string; crew?: string }
  | { kind: 'failed'; status: number; error: string; output?: string; workerId?: string; crew?: string }

export async function fetchDispatchOptions(): Promise<DispatchOptions> {
  const res = await fetch('/api/dispatch/options')
  if (!res.ok) {
    throw new Error(`fetchDispatchOptions: ${res.status} ${await res.text()}`)
  }
  return (await res.json()) as DispatchOptions
}

interface DispatchSuccessBody {
  worker_id: string
  branch: string
  issue_url?: string
  output: string
  crew?: string
}

interface DispatchErrorBody {
  error: string
  output?: string
  worker_id?: string
  crew?: string
}

export async function submitDispatch(req: DispatchRequest): Promise<DispatchOutcome> {
  let res: Response
  try {
    res = await fetch('/api/dispatch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    })
  } catch {
    return { kind: 'failed', status: 0, error: 'network error' }
  }
  const text = await res.text()
  if (res.ok) {
    try {
      const body = JSON.parse(text) as DispatchSuccessBody
      return { kind: 'started', workerId: body.worker_id, branch: body.branch, issueUrl: body.issue_url, crew: body.crew }
    } catch {
      return { kind: 'failed', status: res.status, error: 'malformed response: ' + text.trim() }
    }
  }
  try {
    const body = JSON.parse(text) as DispatchErrorBody
    return { kind: 'failed', status: res.status, error: body.error, output: body.output, workerId: body.worker_id, crew: body.crew }
  } catch {
    return { kind: 'failed', status: res.status, error: text.trim() }
  }
}

export interface DispatcherRequest {
  repo: string
  tasks: string[]
  engine: string
  model?: string
  effort?: string
}

export type DispatcherOutcome =
  | { kind: 'started'; crew: string; session: string; window: string; pane: string; runId: string }
  | {
      kind: 'failed'
      status: number
      error: string
      output?: string
      crew?: string
      session?: string
      window?: string
      pane?: string
    }

interface DispatcherSuccessBody {
  crew: string
  session: string
  window: string
  pane: string
  run_id: string
}

interface DispatcherErrorBody extends DispatchErrorBody {
  session?: string
  window?: string
  pane?: string
}

export async function submitDispatcher(req: DispatcherRequest): Promise<DispatcherOutcome> {
  let res: Response
  try {
    res = await fetch('/api/dispatch/dispatcher', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    })
  } catch {
    return { kind: 'failed', status: 0, error: 'network error' }
  }
  const text = await res.text()
  if (res.ok) {
    try {
      const body = JSON.parse(text) as DispatcherSuccessBody
      return {
        kind: 'started',
        crew: body.crew,
        session: body.session,
        window: body.window,
        pane: body.pane,
        runId: body.run_id,
      }
    } catch {
      return { kind: 'failed', status: res.status, error: 'malformed response: ' + text.trim() }
    }
  }
  try {
    const body = JSON.parse(text) as DispatcherErrorBody
    return {
      kind: 'failed',
      status: res.status,
      error: body.error,
      output: body.output,
      crew: body.crew,
      session: body.session,
      window: body.window,
      pane: body.pane,
    }
  } catch {
    return { kind: 'failed', status: res.status, error: text.trim() }
  }
}
