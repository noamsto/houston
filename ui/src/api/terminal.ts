// Addresses a terminal by run id; the server resolves the live pane. Every
// terminal consumer (socket path, composer sends) goes through this module.
export type TerminalAddress = { kind: 'run'; id: string }

// Private-use close code: the pane's tmux server changed underneath the
// socket. usePaneSocket must not auto-retry on this code.
export const WS_CLOSE_SERVER_CHANGED = 4409

export function terminalKey(a: TerminalAddress): string {
  return `run:${a.id}`
}

export function terminalSocketPath(a: TerminalAddress): string {
  return `/api/runs/${encodeURIComponent(a.id)}/terminal`
}

// A text or image body can already be in the pane when the client timer fires,
// because the server may have typed it and then failed to answer in time.
const longSendTimeout = 'timed out — the text may have been partly sent; check the agent before resending'

// Resolves to a short reason on failure, null on success.
async function request(url: string, init: RequestInit, onTimeout = 'timed out'): Promise<string | null> {
  try {
    const res = await fetch(url, { method: 'POST', signal: AbortSignal.timeout(15000), ...init })
    if (res.ok) return null
    if (res.status === 401) return 'session expired — reload'
    if (res.status === 502) {
      try {
        const body: unknown = await res.json()
        if (body !== null && typeof body === 'object' && 'partial' in body && body.partial === true) {
          return 'may have been partly sent — check the agent before resending'
        }
      } catch {
        // A missing or unreadable body is still a plain 502.
      }
    }
    return `HTTP ${res.status}`
  } catch (e) {
    return e instanceof DOMException && e.name === 'TimeoutError' ? onTimeout : 'offline'
  }
}

const runInput = (id: string, body: unknown, onTimeout?: string) =>
  request(
    `/api/runs/${encodeURIComponent(id)}/input`,
    {
      body: JSON.stringify(body),
      headers: { 'Content-Type': 'application/json' },
    },
    onTimeout,
  )

export interface TerminalImage {
  name: string
  type: string
  data: string
}

export function sendText(a: TerminalAddress, text: string): Promise<string | null> {
  return runInput(a.id, { type: 'text', text }, longSendTimeout)
}

export function sendKey(a: TerminalAddress, key: string): Promise<string | null> {
  return runInput(a.id, { type: 'key', key })
}

export function sendImage(a: TerminalAddress, text: string, image: TerminalImage): Promise<string | null> {
  return runInput(a.id, { type: 'image', text, images: [image] }, longSendTimeout)
}
