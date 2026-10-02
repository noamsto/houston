import { useEffect, useState } from 'react'
import {
  addRepo,
  fetchRepoCandidates,
  fetchRepos,
  removeRepo,
  type RepoCandidate,
  type RepoEntry,
} from '../api/repos'

const SEARCH_DEBOUNCE_MS = 200

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

export function RepoPicker({ onChanged, onClose }: { onChanged: (added?: string) => void; onClose: () => void }) {
  const [repos, setRepos] = useState<RepoEntry[]>([])
  const [roots, setRoots] = useState<string[]>([])
  const [query, setQuery] = useState('')
  const [candidates, setCandidates] = useState<RepoCandidate[]>([])
  const [truncated, setTruncated] = useState(false)
  const [listError, setListError] = useState<string | null>(null)
  const [searchError, setSearchError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [version, setVersion] = useState(0)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    fetchRepos()
      .then((r) => {
        if (cancelled) return
        setRepos(r.repos)
        setRoots(r.roots)
        setListError(null)
      })
      .catch((e: unknown) => {
        if (!cancelled) setListError(messageOf(e))
      })
    return () => { cancelled = true }
  }, [version])

  useEffect(() => {
    let cancelled = false
    const timer = setTimeout(
      () => {
        fetchRepoCandidates(query)
          .then((r) => {
            if (cancelled) return
            setCandidates(r.candidates)
            setTruncated(r.truncated)
            setSearchError(null)
          })
          .catch((e: unknown) => {
            if (!cancelled) setSearchError(messageOf(e))
          })
      },
      query === '' ? 0 : SEARCH_DEBOUNCE_MS,
    )
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [query, version])

  async function change(action: () => Promise<{ ok: true; path?: string } | { ok: false; error: string }>): Promise<void> {
    setBusy(true)
    setActionError(null)
    const res = await action()
    setBusy(false)
    if (!res.ok) {
      setActionError(res.error)
      return
    }
    setVersion((v) => v + 1)
    onChanged(res.path)
  }

  const error = actionError ?? listError ?? searchError

  return (
    <section className="repo-picker" aria-label="Manage repos">
      <div className="repo-picker-head">
        <h2>Manage repos</h2>
        <button type="button" className="repo-picker-btn" onClick={onClose}>Close</button>
      </div>

      {error && <p className="dispatch-hint dispatch-error" role="alert">{error}</p>}

      <ul className="repo-picker-list">
        {repos.map((r) => (
          <li key={r.path} className="repo-picker-row">
            <div className="repo-picker-name">
              <strong>{r.name}</strong>
              {!r.valid && <span className="repo-picker-missing">missing</span>}
              <span className="dispatch-hint">{r.path}</span>
            </div>
            <button
              type="button"
              className="repo-picker-btn"
              disabled={busy}
              aria-label={`Remove ${r.name}`}
              onClick={() => { void change(() => removeRepo(r.path)) }}
            >
              Remove
            </button>
          </li>
        ))}
      </ul>

      <div className="dispatch-field">
        <label htmlFor="repo-picker-search">Search repos</label>
        <input
          id="repo-picker-search"
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {roots.length > 0 && <span className="dispatch-hint">Searching under {roots.join(', ')}</span>}
      </div>

      <ul className="repo-picker-list">
        {candidates.map((c) => (
          <li key={c.path} className="repo-picker-row">
            <div className="repo-picker-name">
              <strong>{c.name}</strong>
              <span className="dispatch-hint">{c.path}</span>
            </div>
            <button
              type="button"
              className="repo-picker-btn"
              disabled={busy || c.registered}
              aria-label={`Add ${c.name}`}
              onClick={() => { void change(() => addRepo(c.path)) }}
            >
              Add
            </button>
          </li>
        ))}
      </ul>
      {truncated && <p className="dispatch-hint">Showing the first results — type to narrow the search.</p>}
    </section>
  )
}
