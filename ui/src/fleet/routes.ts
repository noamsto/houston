import { useCallback, useEffect, useState } from 'react'

export type DetailTab = 'activity' | 'terminal'

export interface DetailRoute {
  id: string
  tab: DetailTab
}

export function parseDetailRoute(hash: string): DetailRoute | null {
  const m = hash.match(/^#\/fleet\/([^/]+)(?:\/([^/]*))?$/)
  if (!m) return null
  return { id: m[1], tab: m[2] === 'terminal' ? 'terminal' : 'activity' }
}

export function runHash(id: string, tab: DetailTab = 'activity'): string {
  return `#/fleet/${id}/${tab}`
}

export function useDetailRoute(): DetailRoute | null {
  const [detail, setDetail] = useState<DetailRoute | null>(() => parseDetailRoute(window.location.hash))
  useEffect(() => {
    const onHash = () => setDetail(parseDetailRoute(window.location.hash))
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])
  return detail
}

interface DispatchLink {
  repo?: string
  crew?: string
}

export interface DispatchRoute extends DispatchLink {
  seq: number
}

export function parseDispatchRoute(hash: string): DispatchLink | null {
  const m = hash.match(/^#\/dispatch(?:\?(.*))?$/)
  if (!m) return null
  const params = new URLSearchParams(m[1] ?? '')
  return { repo: params.get('repo') || undefined, crew: params.get('crew') || undefined }
}

export function dispatchHash(params: DispatchLink): string {
  const search = new URLSearchParams()
  if (params.repo) search.set('repo', params.repo)
  if (params.crew) search.set('crew', params.crew)
  const qs = search.toString()
  return qs ? `#/dispatch?${qs}` : '#/dispatch'
}

/** `seq` is a per-hashchange token, not a route field: it increments on
 *  every hashchange (even a link followed twice, producing the same
 *  repo/crew) so a consumer effect keyed on it re-applies rather than
 *  bailing out on an unchanged value. */
export function useDispatchRoute(): DispatchRoute | null {
  const [route, setRoute] = useState<DispatchRoute | null>(() => {
    const link = parseDispatchRoute(window.location.hash)
    return link && { ...link, seq: 0 }
  })
  useEffect(() => {
    let seq = 0
    const onHash = () => {
      seq += 1
      const link = parseDispatchRoute(window.location.hash)
      setRoute(link && { ...link, seq })
    }
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])
  return route
}

export type ShellTab = 'fleet' | 'crews' | 'workspace' | 'dispatch'

export const TAB_LABEL: Record<ShellTab, string> = {
  fleet: 'Fleet',
  crews: 'Crews',
  workspace: 'Workspace',
  dispatch: 'Dispatch',
}

export function parseTabRoute(hash: string): ShellTab | null {
  if (hash === '' || hash === '#' || hash === '#/' || hash === '#/fleet') return 'fleet'
  if (hash === '#/crews') return 'crews'
  if (hash === '#/workspace') return 'workspace'
  if (parseDispatchRoute(hash)) return 'dispatch'
  return null
}

export function tabHash(tab: ShellTab): string {
  return `#/${tab}`
}

/** A detail or other non-tab hash leaves the tab as it was, so the tab under
 *  a run-detail overlay is the one Back returns to. `closeDetail` is for the
 *  mobile overlay, which a tab tap must dismiss; the desktop detail is a
 *  persistent pane, so with a run selected a tab switch is state-only. */
export function useShellTab(): [ShellTab, (tab: ShellTab, closeDetail?: boolean) => void] {
  const [tab, setTab] = useState<ShellTab>(() => parseTabRoute(window.location.hash) ?? 'fleet')
  useEffect(() => {
    const onHash = () => {
      const next = parseTabRoute(window.location.hash)
      if (next) setTab(next)
    }
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])
  const goTab = useCallback((next: ShellTab, closeDetail = false) => {
    setTab(next)
    if (closeDetail || parseDetailRoute(window.location.hash) === null) {
      window.location.hash = tabHash(next)
    }
  }, [])
  return [tab, goTab]
}
