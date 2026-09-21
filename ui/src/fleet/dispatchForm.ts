// Pure helpers backing the Dispatch tab. Kept free of React so the
// precedence rules (resolveInitial) and the arithmetic (taskMaxHeight,
// crewAgeLabel) can be unit-tested directly.
import type { DispatchOptions, DispatchRepo } from '../api/dispatch'
import { NEW_CREW } from '../api/dispatch'
import type { Run } from '../api/runs'
import { agoLabel } from './format'

export interface DispatchPrefs {
  repo?: string
  engine?: string
  model?: string
  tier?: string
  effort?: string
}

const PREFS_KEY = 'houston-dispatch-prefs'

export function loadPrefs(): DispatchPrefs {
  try {
    const saved = localStorage.getItem(PREFS_KEY)
    if (!saved) return {}
    const parsed: unknown = JSON.parse(saved)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return parsed as DispatchPrefs
  } catch {
    return {}
  }
}

export function savePrefs(p: DispatchPrefs): void {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(p))
  } catch {
    // ignore
  }
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
 *  hard defaults into the form's initial state (spec R3/R4/R5). */
export function resolveInitial(
  o: DispatchOptions,
  prefs: DispatchPrefs,
  link: { repo?: string; crew?: string } | null,
): ResolvedInitial {
  const repoKnown = !!link?.repo && o.repos.some((r) => r.path === link.repo)
  const repo =
    link?.repo && repoKnown
      ? link.repo
      : prefs.repo && o.repos.some((r) => r.path === prefs.repo)
        ? prefs.repo
        : (o.repos[0]?.path ?? '')

  const engine =
    prefs.engine && o.engine_order.includes(prefs.engine)
      ? prefs.engine
      : o.engine_order.includes('claude')
        ? 'claude'
        : (o.engine_order[0] ?? '')

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
  // Three cases (plan-critic, binding): (a) link.repo known — apply
  // link.crew when valid against that repo; (b) no link.repo — apply
  // link.crew when valid against the chosen (pref/default) repo; (c)
  // link.repo unknown — ignore link.crew entirely.
  const linkCrewValid =
    link?.crew !== undefined && (link.crew === NEW_CREW || (chosenRepo?.crews.includes(link.crew) ?? false))
  const crew = link?.repo && !repoKnown ? defaultCrew(chosenRepo) : linkCrewValid ? link.crew! : defaultCrew(chosenRepo)

  return { repo, crew, engine, tier, model, effort, linkRepoUnknown: !!link?.repo && !repoKnown }
}

/** `<id> · <age> ago`, or bare `id` if the id doesn't start with a unix
 *  timestamp (crew ids are always `<unix>-<pid>`, but don't crash otherwise). */
export function crewAgeLabel(id: string, now: number): string {
  const m = id.match(/^(\d+)-/)
  if (!m) return id
  const unixSeconds = Number(m[1])
  if (!Number.isFinite(unixSeconds)) return id
  return `${id} · ${agoLabel(unixSeconds, now)} ago`
}

/** The same 40%-of-viewport rule #64 uses for the composer, duplicated as a
 *  pure helper here since composerMaxHeight.ts lives only on that unmerged
 *  branch. */
export function taskMaxHeight(vh: number): number {
  return Math.max(120, Math.round(vh * 0.4))
}

/** The newest non-removed run for a just-dispatched branch, optionally
 *  narrowed to a crew — an empty/undefined run.crew.name is a wildcard
 *  (tmux-only runs belong to no crew group and still match). */
export function findDispatchedRun(runs: Run[], branch: string, crew?: string): Run | undefined {
  let best: Run | undefined
  for (const r of runs) {
    if (r.removed) continue
    if (r.branch !== branch) continue
    if (crew !== undefined && r.crew?.name && r.crew.name !== crew) continue
    const t = r.since ?? r.updated_at
    if (!best || t > (best.since ?? best.updated_at)) best = r
  }
  return best
}
