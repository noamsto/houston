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
 *  sibling run while one is shown replaces instead of pushing; selecting the run
 *  its `from` names (the on-screen parent) collapses the stack with `back()`. */
export function selectRun(id: string): void {
  if (!parseDetailRoute(window.location.hash)) {
    openRun(id)
    return
  }
  const current = navEntry()
  if (current && parseDetailRoute(current.from)?.id === id) {
    back(current.root ?? current.from)
    return
  }
  go('replaceState', window.history.state, runHash(id))
}

// history.back()/go() land asynchronously and navEntry() keeps the old depth
// until then, so a second pop in that window would overshoot. `popstate` is the
// usual clear, but a cancelled traversal or a pop past a pruned history start
// never yields one, so `pageshow`/`hashchange` and a fallback timer clear it too.
const POP_FALLBACK_MS = 1000
let popping = false
let popTimer: ReturnType<typeof setTimeout> | null = null

/** Drops the in-flight pop guard and its fallback timer. The test history helper
 *  calls it on uninstall so one test's pending pop cannot suppress the next. */
export function clearPopGuard(): void {
  popping = false
  if (popTimer !== null) {
    clearTimeout(popTimer)
    popTimer = null
  }
}

function raisePopGuard(): void {
  popping = true
  if (popTimer !== null) clearTimeout(popTimer)
  popTimer = setTimeout(clearPopGuard, POP_FALLBACK_MS)
}

window.addEventListener('popstate', clearPopGuard)
window.addEventListener('pageshow', clearPopGuard)
window.addEventListener('hashchange', clearPopGuard)

/** A real pop when houston pushed the entry; otherwise (deep link, reload with
 *  nothing of houston's underneath) replace to the parent, never push. */
export function back(rootHash: string): void {
  if (popping) return
  const current = navEntry()
  if (current && current.depth >= 1) {
    raisePopGuard()
    window.history.back()
    return
  }
  go('replaceState', null, current?.from ?? rootHash)
}

export function popToRoot(rootHash: string): void {
  if (popping) return
  const current = navEntry()
  if (current && current.depth >= 1 && current.root !== null) {
    raisePopGuard()
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
