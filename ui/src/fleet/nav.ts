import { useEffect, useState } from 'react'
import type { MouseEvent } from 'react'
import type { Run } from '../api/runs'
import { nameLabel } from './format'
import { navigateHash as go, parseDetailRoute, parseTabRoute, runHash, TAB_LABEL, tabHash, type DetailTab, type ShellTab } from './routes'

/**
 * houston's own navigation stack, kept in `history.state` so houston's Back,
 * the edge swipe and the browser's native back all pop the same entry.
 *
 * `depth` counts the run entries houston pushed above the first entry it did
 * not (a shell tab, or a deep-linked run), `from` is the hash it was opened
 * from, and `root` the shell-tab hash at the bottom of the stack, or null when
 * the stack starts on a deep-linked run.
 */
export interface NavEntry {
  depth: number
  from: string
  root: string | null
}

export function navEntry(): NavEntry | null {
  const entry = (window.history.state as { houston?: NavEntry } | null)?.houston
  return entry && typeof entry.depth === 'number' ? entry : null
}

export function openRun(id: string, tab?: DetailTab): void {
  const from = window.location.hash
  if (parseDetailRoute(from)?.id === id) {
    go('replaceState', window.history.state, runHash(id, tab))
    return
  }
  const current = navEntry()
  const root = current ? current.root : parseDetailRoute(from) ? null : tabHash(parseTabRoute(from) ?? 'fleet')
  go('pushState', { houston: { depth: (current?.depth ?? 0) + 1, from, root } }, runHash(id, tab))
}

/** A lateral move between one run's tabs: it replaces, so it adds no entry. */
export function switchRunTab(id: string, tab: DetailTab): void {
  go('replaceState', window.history.state, runHash(id, tab))
}

/** Desktop list/Workspace pick: the detail is a persistent pane, so choosing a
 *  sibling run while one is shown replaces instead of pushing. */
export function selectRun(id: string): void {
  if (!parseDetailRoute(window.location.hash)) {
    openRun(id)
    return
  }
  go('replaceState', window.history.state, runHash(id))
}

// history.back()/go() land asynchronously and navEntry() keeps the old depth
// until then, so a second pop in that window would overshoot.
let popping = false
window.addEventListener('popstate', () => { popping = false })

/** A real pop when houston pushed the entry; otherwise (deep link, reload with
 *  nothing of houston's underneath) replace to the parent, never push. */
export function back(rootHash: string): void {
  if (popping) return
  const current = navEntry()
  if (current && current.depth >= 1) {
    popping = true
    window.history.back()
    return
  }
  go('replaceState', null, current?.from ?? rootHash)
}

export function popToRoot(rootHash: string): void {
  if (popping) return
  const current = navEntry()
  if (current && current.depth >= 1 && current.root !== null) {
    popping = true
    window.history.go(-current.depth)
    return
  }
  go('replaceState', null, current?.root ?? rootHash)
}

export function useNavEntry(): NavEntry | null {
  const [entry, setEntry] = useState(navEntry)
  useEffect(() => {
    const onChange = () => setEntry(navEntry())
    window.addEventListener('hashchange', onChange)
    window.addEventListener('popstate', onChange)
    return () => {
      window.removeEventListener('hashchange', onChange)
      window.removeEventListener('popstate', onChange)
    }
  }, [])
  return entry
}

/** An in-app link's click handler that keeps modified clicks (new tab) native. */
export function onAppLink(navigate: () => void) {
  return (e: MouseEvent<HTMLAnchorElement>) => {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    navigate()
  }
}

/** Where `back()` lands, named for a Back button: the parent run's codename
 *  or branch, else the shell tab. */
export function backLabel(entry: NavEntry | null, runs: Run[], tab: ShellTab): string {
  const parent = entry && entry.depth >= 1 ? entry.from : null
  const parentRun = parent ? parseDetailRoute(parent) : null
  if (parentRun) {
    const run = runs.find((r) => r.id === parentRun.id)
    return run ? run.crew?.codename || run.branch || nameLabel(run) : 'Back'
  }
  return TAB_LABEL[(parent && parseTabRoute(parent)) || tab]
}

export function rootLabel(entry: NavEntry | null, tab: ShellTab): string {
  return TAB_LABEL[(entry?.root && parseTabRoute(entry.root)) || tab]
}
