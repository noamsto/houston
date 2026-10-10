import { useEffect, useState } from 'react'
import { fetchDispatchOptions } from '../api/dispatch'

export interface CrewRepo {
  name: string
  path: string
}

export interface CrewRepos {
  byCrew: Map<string, CrewRepo>
  byName: Map<string, CrewRepo | null>
}

const EMPTY: CrewRepos = { byCrew: new Map(), byName: new Map() }

let cached: Promise<CrewRepos> | null = null

// One fetch per page load: a crew created later shows no repo until reload.
// A failure is tolerated silently and resolves to empty maps.
function loadCrewRepos(): Promise<CrewRepos> {
  cached ??= fetchDispatchOptions()
    .then((opts) => {
      const byCrew = new Map<string, CrewRepo>()
      const byName = new Map<string, CrewRepo | null>()
      for (const r of opts.repos) {
        for (const crew of r.home ?? []) byCrew.set(crew, { name: r.name, path: r.path })
        // Two repos sharing a name are ambiguous: better no link than the wrong one.
        byName.set(r.name, byName.has(r.name) ? null : { name: r.name, path: r.path })
      }
      return { byCrew, byName }
    })
    .catch(() => EMPTY)
  return cached
}

export function resetCrewReposCache(): void {
  cached = null
}

export function useCrewRepos(enabled: boolean): CrewRepos {
  const [repos, setRepos] = useState<CrewRepos>(EMPTY)

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    void loadCrewRepos().then((r) => {
      if (!cancelled) setRepos(r)
    })
    return () => {
      cancelled = true
    }
  }, [enabled])

  return repos
}

export function resolveCrewRepo(crew: string, project: string | undefined, repos: CrewRepos): CrewRepo | undefined {
  return repos.byCrew.get(crew) ?? (project ? repos.byName.get(project) ?? undefined : undefined)
}
