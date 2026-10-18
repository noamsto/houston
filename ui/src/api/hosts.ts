export interface HostHealth {
  name: string
  local?: boolean
  health: 'connecting' | 'connected' | 'unreachable'
  error?: string
  last_seen?: number
}

// The local houston is addressed by its plain route; a peer by the
// host-addressed forward (server/peer_forward.go).
export function hostAPI(host: string | undefined, path: string): string {
  if (!host || host === 'local') return `/api/${path}`
  return `/api/hosts/${encodeURIComponent(host)}/${path}`
}

export async function fetchHosts(): Promise<HostHealth[]> {
  const res = await fetch('/api/hosts')
  if (!res.ok) {
    throw new Error(`fetchHosts: ${res.status} ${await res.text()}`)
  }
  const body = (await res.json()) as { hosts: HostHealth[] }
  return body.hosts
}
