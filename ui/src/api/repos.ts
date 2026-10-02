export interface RepoEntry {
  path: string
  name: string
  valid: boolean
}

export interface RepoCandidate {
  path: string
  name: string
  registered: boolean
}

async function getJSON<T>(name: string, url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) {
    throw new Error(`${name}: ${res.status} ${await res.text()}`)
  }
  return (await res.json()) as T
}

export function fetchRepos(): Promise<{ roots: string[]; repos: RepoEntry[] }> {
  return getJSON('fetchRepos', '/api/repos')
}

export function fetchRepoCandidates(
  q: string,
): Promise<{ roots: string[]; candidates: RepoCandidate[]; truncated: boolean }> {
  return getJSON('fetchRepoCandidates', '/api/repos/candidates?q=' + encodeURIComponent(q))
}

async function errorText(res: Response): Promise<string> {
  const text = await res.text()
  try {
    const body = JSON.parse(text) as { error?: string }
    if (body.error) return body.error
  } catch {
    // not JSON — fall through to the raw text
  }
  return text.trim() || `HTTP ${res.status}`
}

async function send(method: 'POST' | 'DELETE', path: string): Promise<Response | null> {
  try {
    return await fetch('/api/repos', {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path }),
    })
  } catch {
    return null
  }
}

export async function addRepo(path: string): Promise<{ ok: true; path: string } | { ok: false; error: string }> {
  const res = await send('POST', path)
  if (!res) return { ok: false, error: 'network error' }
  if (!res.ok) return { ok: false, error: await errorText(res) }
  try {
    const body = JSON.parse(await res.text()) as { path: string }
    return { ok: true, path: body.path }
  } catch {
    return { ok: false, error: 'malformed response' }
  }
}

export async function removeRepo(path: string): Promise<{ ok: true } | { ok: false; error: string }> {
  const res = await send('DELETE', path)
  if (!res) return { ok: false, error: 'network error' }
  if (!res.ok) return { ok: false, error: await errorText(res) }
  return { ok: true }
}
