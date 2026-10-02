// Addresses a terminal by run id (server resolves the live pane) or, for the
// classic views, by a raw pane target string. Every terminal consumer
// (socket path, composer sends) goes through this module so `/api/pane/*`
// stays confined to the classic address kind — see fleet/noPaneRoutes.test.ts.
export type TerminalAddress =
  | { kind: 'run'; id: string }
  | { kind: 'pane'; target: string; paneId?: string; server?: string }

// Private-use close code: the pane's tmux server changed underneath the
// socket. usePaneSocket must not auto-retry on this code.
export const WS_CLOSE_SERVER_CHANGED = 4409

type PaneAddress = Extract<TerminalAddress, { kind: 'pane' }>

const paneQuery = (a: PaneAddress) =>
  `?pane_id=${encodeURIComponent(a.paneId ?? '')}&server=${encodeURIComponent(a.server ?? '')}`

export function terminalKey(a: TerminalAddress): string {
  return a.kind === 'run' ? `run:${a.id}` : `pane:${a.target}:${a.paneId ?? ''}:${a.server ?? ''}`
}

export function terminalSocketPath(a: TerminalAddress): string {
  return a.kind === 'run' ? `/api/runs/${encodeURIComponent(a.id)}/terminal` : `/api/pane/${a.target}/ws${paneQuery(a)}`
}

// Resolves to a short reason on failure, null on success.
async function request(url: string, init: RequestInit): Promise<string | null> {
  try {
    const res = await fetch(url, { method: 'POST', signal: AbortSignal.timeout(15000), ...init })
    if (res.ok) return null
    return res.status === 401 ? 'session expired — reload' : `HTTP ${res.status}`
  } catch (e) {
    return e instanceof DOMException && e.name === 'TimeoutError' ? 'timed out' : 'offline'
  }
}

const runInput = (id: string, body: unknown) =>
  request(`/api/runs/${encodeURIComponent(id)}/input`, {
    body: JSON.stringify(body),
    headers: { 'Content-Type': 'application/json' },
  })

const paneForm = (a: PaneAddress, params: Record<string, string>) =>
  request(`/api/pane/${a.target}/send${paneQuery(a)}`, {
    body: new URLSearchParams(params),
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
  })

const paneJSON = (a: PaneAddress, body: unknown) =>
  request(`/api/pane/${a.target}/send-with-images${paneQuery(a)}`, {
    body: JSON.stringify(body),
    headers: { 'Content-Type': 'application/json' },
  })

export interface TerminalImage {
  name: string
  type: string
  data: string
}

export function sendText(a: TerminalAddress, text: string): Promise<string | null> {
  return a.kind === 'run' ? runInput(a.id, { type: 'text', text }) : paneForm(a, { input: text })
}

export function sendKey(a: TerminalAddress, key: string): Promise<string | null> {
  return a.kind === 'run'
    ? runInput(a.id, { type: 'key', key })
    : paneForm(a, { input: key, special: 'true' })
}

export function sendImage(a: TerminalAddress, text: string, image: TerminalImage): Promise<string | null> {
  return a.kind === 'run'
    ? runInput(a.id, { type: 'image', text, images: [image] })
    : paneJSON(a, { text, images: [image] })
}
