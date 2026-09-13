import { useEffect, useState } from 'react'

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
