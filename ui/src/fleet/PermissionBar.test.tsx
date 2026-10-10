import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { PermissionBar } from './PermissionBar'
import type { Run } from '../api/runs'
import type { Prompt } from '../api/answer'

const MOVED = 'The session moved on — refresh'

function run(state: Run['state']): Run {
  return {
    id: 'r1', agent: 'claude', state,
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true, chat: true },
    updated_at: 0,
  } as Run
}

const prompt = (frame: string): Prompt => ({ question: `Run ${frame}?`, choices: ['Yes', 'No'], detail: '', frame })

const json = (body: unknown) => ({ status: 200, ok: true, json: async () => body, text: async () => JSON.stringify(body) }) as Response
const notFound = () => ({ status: 404, ok: false, text: async () => '' }) as Response

interface Env {
  /** Called for each /prompt request, in start order. */
  onPrompt: (n: number) => Promise<Response>
  answer: () => Promise<Response>
  promptStarts: () => number
}

function install(env: Pick<Env, 'onPrompt' | 'answer'>): Env {
  let starts = 0
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    if (url.includes('/prompt')) return env.onPrompt(starts++)
    if (url.includes('/answer')) return env.answer()
    throw new Error(`unexpected fetch: ${url}`)
  }))
  return { ...env, promptStarts: () => starts }
}

const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('PermissionBar', () => {
  it('drops the moved notice when the bar deactivates, so a later prompt starts clean', async () => {
    let current = prompt('p1')
    install({
      onPrompt: async () => json(current),
      answer: async () => ({ status: 409, ok: false, text: async () => 'prompt changed' }) as Response,
    })
    const onPrompt = vi.fn()
    const view = render(<PermissionBar run={run('blocked')} onPrompt={onPrompt} />)
    await tick(0)
    fireEvent.click(screen.getByRole('button', { name: '1. Yes' }))
    await tick(0)
    expect(screen.getByText(MOVED)).toBeTruthy()

    view.rerender(<PermissionBar run={run('running')} onPrompt={onPrompt} />)
    current = prompt('p2')
    view.rerender(<PermissionBar run={run('blocked')} onPrompt={onPrompt} />)
    await tick(0)
    expect(screen.getByText('Run p2?')).toBeTruthy()
    expect(screen.queryByText(MOVED)).toBeNull()
  })

  it('does not count a poll that started before the answer toward re-showing the frame', async () => {
    const hung: Array<(r: Response) => void> = []
    install({
      // Poll 0 is the mount poll; poll 1 (the 2 s tick) hangs across the tap.
      onPrompt: (n) => (n === 1 ? new Promise<Response>((resolve) => hung.push(resolve)) : Promise.resolve(json(prompt('p1')))),
      answer: async () => ({ status: 204, ok: true, text: async () => '' }) as Response,
    })
    render(<PermissionBar run={run('blocked')} onPrompt={() => {}} />)
    await tick(0)
    await tick(2000)
    expect(hung).toHaveLength(1)

    fireEvent.click(screen.getByRole('button', { name: '1. Yes' }))
    await tick(0)
    expect(screen.queryByRole('group', { name: 'Permission prompt' })).toBeNull()

    // The in-flight poll lands after the 204 with the answered frame.
    await act(async () => { hung[0](json(prompt('p1'))) })
    // Polls started after the answer: two keep it hidden, the third re-shows it.
    await tick(2000)
    await tick(2000)
    expect(screen.queryByRole('group', { name: 'Permission prompt' })).toBeNull()
    await tick(2000)
    expect(screen.getByRole('group', { name: 'Permission prompt' })).toBeTruthy()
  })

  it('hides an answered frame once a poll shows no dialog', async () => {
    let gone = false
    install({
      onPrompt: async () => (gone ? notFound() : json(prompt('p1'))),
      answer: async () => ({ status: 204, ok: true, text: async () => '' }) as Response,
    })
    render(<PermissionBar run={run('blocked')} onPrompt={() => {}} />)
    await tick(0)
    fireEvent.click(screen.getByRole('button', { name: '1. Yes' }))
    await tick(0)
    gone = true
    await tick(2000)
    gone = false
    await tick(2000)
    expect(screen.getByRole('group', { name: 'Permission prompt' })).toBeTruthy()
  })
})
