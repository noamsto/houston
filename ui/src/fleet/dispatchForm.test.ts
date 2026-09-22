import { afterEach, describe, expect, it } from 'vitest'
import type { DispatchOptions } from '../api/dispatch'
import type { Run } from '../api/runs'
import {
  crewAgeLabel,
  defaultCrew,
  defaultModel,
  findDispatchedRun,
  isValidIssue,
  loadPrefs,
  resolveInitial,
  savePrefs,
  taskMaxHeight,
  type DispatchPrefs,
} from './dispatchForm'

const PREFS_KEY = 'houston-dispatch-prefs'

const options: DispatchOptions = {
  repos: [
    { path: '/repo/a', name: 'repo-a', crews: ['200-1', '100-2'] },
    { path: '/repo/b', name: 'repo-b', crews: [] },
  ],
  tiers: ['trivial', 'standard', 'deep'],
  efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
  plans: ['required', 'provided'],
  engines: {
    claude: ['opus', 'sonnet', 'haiku'],
    codex: ['gpt-5.6-sol', 'gpt-5.6-terra'],
  },
  engine_order: ['claude', 'codex'],
  tier_models: {
    claude: { trivial: 'haiku', standard: 'sonnet', deep: 'opus' },
    codex: { trivial: 'gpt-5.6-luna', standard: 'gpt-5.6-terra', deep: 'gpt-5.6-sol' },
  },
}

afterEach(() => {
  localStorage.clear()
})

describe('loadPrefs / savePrefs', () => {
  it('returns {} when nothing is persisted', () => {
    expect(loadPrefs()).toEqual({})
  })

  it('round-trips a saved value', () => {
    const prefs: DispatchPrefs = { repo: '/repo/a', engine: 'claude', model: 'sonnet', tier: 'standard', effort: 'high' }
    savePrefs(prefs)
    expect(loadPrefs()).toEqual(prefs)
  })

  it('ignores garbage JSON', () => {
    localStorage.setItem(PREFS_KEY, 'not json')
    expect(loadPrefs()).toEqual({})
  })

  it('ignores a non-object JSON value', () => {
    localStorage.setItem(PREFS_KEY, '"a string"')
    expect(loadPrefs()).toEqual({})
    localStorage.setItem(PREFS_KEY, '[1,2,3]')
    expect(loadPrefs()).toEqual({})
  })
})

describe('defaultModel', () => {
  it('picks the tier map model when it is one of the engine models', () => {
    expect(defaultModel(options, 'claude', 'standard')).toBe('sonnet')
    expect(defaultModel(options, 'claude', 'deep')).toBe('opus')
  })

  it('falls back to the engine first model when the tier pick is not offered', () => {
    // codex/trivial maps to gpt-5.6-luna, which isn't in this fixture's engine list.
    expect(defaultModel(options, 'codex', 'trivial')).toBe('gpt-5.6-sol')
  })
})

describe('defaultCrew', () => {
  it('picks the newest (first) crew', () => {
    expect(defaultCrew(options.repos[0])).toBe('200-1')
  })

  it('is "new" for a crewless repo or an undefined repo', () => {
    expect(defaultCrew(options.repos[1])).toBe('new')
    expect(defaultCrew(undefined)).toBe('new')
  })
})

describe('resolveInitial', () => {
  it('defaults: first repo, claude/standard/sonnet/medium, newest crew, no link', () => {
    const r = resolveInitial(options, {}, null)
    expect(r).toEqual({
      repo: '/repo/a',
      crew: '200-1',
      engine: 'claude',
      tier: 'standard',
      model: 'sonnet',
      effort: 'medium',
      linkRepoUnknown: false,
    })
  })

  it('prefers remembered prefs over defaults when still valid', () => {
    const prefs: DispatchPrefs = { repo: '/repo/b', engine: 'codex', model: 'gpt-5.6-sol', tier: 'deep', effort: 'high' }
    const r = resolveInitial(options, prefs, null)
    expect(r.repo).toBe('/repo/b')
    expect(r.engine).toBe('codex')
    expect(r.model).toBe('gpt-5.6-sol')
    expect(r.tier).toBe('deep')
    expect(r.effort).toBe('high')
  })

  it('ignores invalid remembered prefs and falls back to defaults', () => {
    const prefs: DispatchPrefs = {
      repo: '/repo/gone',
      engine: 'bogus-engine',
      model: 'bogus-model',
      tier: 'bogus-tier',
      effort: 'bogus-effort',
    }
    const r = resolveInitial(options, prefs, null)
    expect(r).toEqual({
      repo: '/repo/a',
      crew: '200-1',
      engine: 'claude',
      tier: 'standard',
      model: 'sonnet',
      effort: 'medium',
      linkRepoUnknown: false,
    })
  })

  it('case (a): link.repo known — applies link.crew when it is one of that repo crews', () => {
    const r = resolveInitial(options, {}, { repo: '/repo/a', crew: '100-2' })
    expect(r.repo).toBe('/repo/a')
    expect(r.crew).toBe('100-2')
    expect(r.linkRepoUnknown).toBe(false)
  })

  it('case (a): link.repo known — falls back to defaultCrew when link.crew is not one of that repo crews', () => {
    const r = resolveInitial(options, {}, { repo: '/repo/a', crew: 'no-such-crew' })
    expect(r.repo).toBe('/repo/a')
    expect(r.crew).toBe('200-1')
  })

  it('case (a): link.crew "new" is always valid', () => {
    const r = resolveInitial(options, {}, { repo: '/repo/a', crew: 'new' })
    expect(r.crew).toBe('new')
  })

  it('case (b): no link.repo — applies link.crew against the chosen (pref/default) repo when valid', () => {
    const prefs: DispatchPrefs = { repo: '/repo/a' }
    const r = resolveInitial(options, prefs, { crew: '100-2' })
    expect(r.repo).toBe('/repo/a')
    expect(r.crew).toBe('100-2')
  })

  it('case (b): no link.repo — falls back to defaultCrew when link.crew is invalid for the chosen repo', () => {
    const r = resolveInitial(options, {}, { crew: 'no-such-crew' })
    expect(r.repo).toBe('/repo/a')
    expect(r.crew).toBe('200-1')
  })

  it('case (c): link.repo unknown — ignores link.crew, uses defaultCrew(chosen repo), and flags linkRepoUnknown', () => {
    const r = resolveInitial(options, {}, { repo: '/repo/unknown', crew: '200-1' })
    expect(r.repo).toBe('/repo/a')
    expect(r.crew).toBe('200-1') // coincidentally equal to defaultCrew(/repo/a), not because the link matched
    expect(r.linkRepoUnknown).toBe(true)
  })

  it('case (c): link.repo unknown with a crewless default repo still gets "new", not the link crew', () => {
    const prefs: DispatchPrefs = { repo: '/repo/b' }
    const r = resolveInitial(options, prefs, { repo: '/repo/unknown', crew: '200-1' })
    expect(r.repo).toBe('/repo/b')
    expect(r.crew).toBe('new')
    expect(r.linkRepoUnknown).toBe(true)
  })
})

describe('crewAgeLabel', () => {
  it('formats id + age', () => {
    const nowSec = 1_700_000_030
    expect(crewAgeLabel('1700000000-4242', nowSec * 1000)).toBe('1700000000-4242 · 30s ago')
  })

  it('returns the bare id when it does not start with a unix prefix', () => {
    expect(crewAgeLabel('not-a-timestamp', Date.now())).toBe('not-a-timestamp')
  })
})

describe('taskMaxHeight', () => {
  it('floors at 120', () => {
    expect(taskMaxHeight(100)).toBe(120)
    expect(taskMaxHeight(0)).toBe(120)
  })

  it('is 40% of the viewport once that exceeds the floor', () => {
    expect(taskMaxHeight(500)).toBe(200)
    expect(taskMaxHeight(1000)).toBe(400)
  })
})

describe('findDispatchedRun', () => {
  function run(p: Partial<Run>): Run {
    return {
      id: 'r1',
      agent: 'claude',
      state: 'idle',
      activity: {},
      tokens: { input: 0, output: 0 },
      caps: { terminal: false, reply: false, kill: false },
      updated_at: 1000,
      ...p,
    } as Run
  }

  it('matches by branch alone when crew is undefined', () => {
    const r = run({ id: 'r1', branch: 'feat/x' })
    expect(findDispatchedRun([r], 'feat/x')).toBe(r)
  })

  it('excludes non-matching branches', () => {
    const r = run({ id: 'r1', branch: 'feat/other' })
    expect(findDispatchedRun([r], 'feat/x')).toBeUndefined()
  })

  it('excludes removed runs', () => {
    const r = run({ id: 'r1', branch: 'feat/x', removed: true })
    expect(findDispatchedRun([r], 'feat/x')).toBeUndefined()
  })

  it('narrows by crew.name when both are known and mismatched', () => {
    const r = run({ id: 'r1', branch: 'feat/x', crew: { name: 'other-crew' } })
    expect(findDispatchedRun([r], 'feat/x', 'my-crew')).toBeUndefined()
  })

  it('treats an empty/undefined run.crew.name as a wildcard', () => {
    const noCrew = run({ id: 'r1', branch: 'feat/x' })
    const emptyCrew = run({ id: 'r2', branch: 'feat/x', crew: { name: '' } })
    expect(findDispatchedRun([noCrew], 'feat/x', 'my-crew')).toBe(noCrew)
    expect(findDispatchedRun([emptyCrew], 'feat/x', 'my-crew')).toBe(emptyCrew)
  })

  it('picks the newest by since (falling back to updated_at)', () => {
    const older = run({ id: 'older', branch: 'feat/x', since: 100, updated_at: 500 })
    const newer = run({ id: 'newer', branch: 'feat/x', since: 200, updated_at: 300 })
    expect(findDispatchedRun([older, newer], 'feat/x')).toBe(newer)
  })
})

describe('isValidIssue', () => {
  it('accepts empty', () => {
    expect(isValidIssue('')).toBe(true)
    expect(isValidIssue('   ')).toBe(true)
  })

  it('accepts a GitHub issue number', () => {
    expect(isValidIssue('123')).toBe(true)
  })

  it('accepts a Linear id', () => {
    expect(isValidIssue('ENG-123')).toBe(true)
  })

  it('rejects a bogus id', () => {
    expect(isValidIssue('not-an-issue')).toBe(false)
  })

  it('rejects a leading #', () => {
    expect(isValidIssue('#123')).toBe(false)
  })

  it('trims surrounding whitespace before matching', () => {
    expect(isValidIssue('  123  ')).toBe(true)
  })
})
