import type { DispatchOptions, DispatchRepo } from '../api/dispatch'
import { NEW_CREW } from '../api/dispatch'
import type { Run } from '../api/runs'
import { agoLabel } from './format'

export interface DispatchPrefs {
  mode?: 'worker' | 'dispatcher'
  repo?: string
  engine?: string
  model?: string
  tier?: string
  effort?: string
}

const PREFS_KEY = 'houston-dispatch-prefs'

function prefsKey(host?: string): string {
  return !host || host === 'local' ? PREFS_KEY : `${PREFS_KEY}@${host}`
}

export function loadPrefs(host?: string): DispatchPrefs {
  try {
    const saved = localStorage.getItem(prefsKey(host))
    if (!saved) return {}
    const parsed: unknown = JSON.parse(saved)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return parsed as DispatchPrefs
  } catch {
    return {}
  }
}

export function savePrefs(p: DispatchPrefs, host?: string): void {
  try {
    localStorage.setItem(prefsKey(host), JSON.stringify(p))
  } catch {
    // ignore
  }
}

/** Engines the worker form offers: those with models, narrowed to what this
 *  host can dispatch unless that lookup failed. */
export function workerEngines(o: DispatchOptions): string[] {
  const withModels = o.engine_order.filter((e) => (o.engines[e] ?? []).length > 0)
  if (o.dispatcher_engines_error) return withModels
  return withModels.filter((e) => (o.dispatcher_engines ?? []).includes(e))
}

/** The default model for an engine+tier, falling back to the engine's first
 *  model when the tier map's pick isn't (or is no longer) one of them. */
export function defaultModel(o: DispatchOptions, engine: string, tier: string): string {
  const preferred = o.tier_models[engine]?.[tier]
  if (preferred && (o.engines[engine] ?? []).includes(preferred)) return preferred
  return o.engines[engine]?.[0] ?? ''
}

export function defaultCrew(repo?: DispatchRepo): string {
  return repo?.crews[0] ?? NEW_CREW
}

interface ResolvedInitial {
  repo: string
  crew: string
  engine: string
  tier: string
  model: string
  effort: string
  linkRepoUnknown: boolean
}

/** Merges a `#/dispatch?repo=&crew=` link, remembered device prefs, and
 *  hard defaults into the form's initial state. */
export function resolveInitial(
  o: DispatchOptions,
  prefs: DispatchPrefs,
  link: { repo?: string; crew?: string } | null,
): ResolvedInitial {
  const repoKnown = !!link?.repo && o.repos.some((r) => r.path === link.repo)
  const linkedCrew = link?.crew && link.crew !== NEW_CREW && !link.repo ? link.crew : undefined
  const crewRepo = linkedCrew
    ? (o.repos.find((r) => r.home?.includes(linkedCrew)) ?? o.repos.find((r) => r.crews.includes(linkedCrew)))
    : undefined
  const repo =
    link?.repo && repoKnown
      ? link.repo
      : crewRepo
        ? crewRepo.path
        : prefs.repo && o.repos.some((r) => r.path === prefs.repo)
          ? prefs.repo
          : (o.repos[0]?.path ?? '')

  const offered = workerEngines(o)
  const engine =
    prefs.engine && offered.includes(prefs.engine)
      ? prefs.engine
      : offered.includes('claude')
        ? 'claude'
        : (offered[0] ?? '')

  const tier =
    prefs.tier && o.tiers.includes(prefs.tier)
      ? prefs.tier
      : o.tiers.includes('standard')
        ? 'standard'
        : (o.tiers[0] ?? '')

  const effort =
    prefs.effort && o.efforts.includes(prefs.effort)
      ? prefs.effort
      : o.efforts.includes('medium')
        ? 'medium'
        : (o.efforts[0] ?? '')

  const model = prefs.model && (o.engines[engine] ?? []).includes(prefs.model) ? prefs.model : defaultModel(o, engine, tier)

  const chosenRepo = o.repos.find((r) => r.path === repo)
  // A crew from a link whose repo we don't know belongs to that repo, not ours.
  const linkCrew = link?.repo && !repoKnown ? undefined : link?.crew
  const crew =
    linkCrew !== undefined && (linkCrew === NEW_CREW || (chosenRepo?.crews.includes(linkCrew) ?? false))
      ? linkCrew
      : defaultCrew(chosenRepo)

  return { repo, crew, engine, tier, model, effort, linkRepoUnknown: !!link?.repo && !repoKnown }
}

/** `<id> · <age> ago`, or bare `id` if it doesn't start with a unix timestamp. */
export function crewAgeLabel(id: string, now: number): string {
  const m = id.match(/^(\d+)-/)
  if (!m) return id
  return `${id} · ${agoLabel(Number(m[1]), now)} ago`
}

/** Matches server/dispatch.go's dispatchIssueRe — a GitHub issue number or a
 *  Linear id. Empty is valid (the field is optional). */
const ISSUE_RE = /^([0-9]{1,9}|[A-Z]{2,10}-[0-9]{1,9})$/

export function isValidIssue(issue: string): boolean {
  return issue.trim() === '' || ISSUE_RE.test(issue.trim())
}

/** Same 40%-of-viewport cap as the terminal composer. */
export function taskMaxHeight(vh: number): number {
  return Math.max(120, Math.round(vh * 0.4))
}

/** The newest non-removed run on the dispatched host for a just-dispatched
 *  branch (`host` a peer name; local otherwise, so peer runs are skipped),
 *  optionally narrowed to a crew — an empty/undefined run.crew.name is a
 *  wildcard (tmux-only runs belong to no crew group and still match). */
export function findDispatchedRun(runs: Run[], branch: string, crew?: string, host?: string): Run | undefined {
  const peer = host && host !== 'local' ? host : undefined
  let best: Run | undefined
  for (const r of runs) {
    if (r.removed) continue
    if (peer ? r.host !== peer : r.host) continue
    if (r.branch !== branch) continue
    if (crew !== undefined && r.crew?.name && r.crew.name !== crew) continue
    const t = r.since ?? r.updated_at
    if (!best || t > (best.since ?? best.updated_at)) best = r
  }
  return best
}
