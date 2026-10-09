export type Mode = 'dispatcher' | 'tmux'

export async function fetchMode(): Promise<Mode> {
  const res = await fetch('/api/mode')
  if (!res.ok) throw new Error(`mode: ${res.status}`)
  const body = (await res.json()) as { mode?: unknown }
  if (body.mode !== 'dispatcher' && body.mode !== 'tmux') throw new Error('mode: unknown value')
  return body.mode
}
