import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fetchDispatchOptions } from '../api/dispatch'
import { CrewGroupHeader } from './CrewGroupHeader'
import { resetCrewReposCache } from './useCrewRepos'
import type { FleetEntry } from './fleetEntries'

type CrewEntry = Extract<FleetEntry, { kind: 'crew' }>

vi.mock('../api/dispatch', () => ({ fetchDispatchOptions: vi.fn() }))
const fetchOptions = vi.mocked(fetchDispatchOptions)

const counts = { working: 0, needsYou: 0, stuck: 0, done: 0, idle: 0, ended: 0 }
const entry = (project?: string): CrewEntry => ({
  kind: 'crew',
  key: 'crew:local:1-1',
  crew: '1-1',
  host: 'local',
  project,
  shown: [],
  members: [],
  needsYou: 0,
  counts,
})

describe('CrewGroupHeader', () => {
  beforeEach(() => {
    resetCrewReposCache()
    fetchOptions.mockRejectedValue(new Error('offline'))
  })
  afterEach(cleanup)

  it('shows the crew project, which is the home repo for a mixed crew', () => {
    render(<CrewGroupHeader entry={entry('dispatcher')} />)
    expect(screen.getByText('dispatcher')).toBeTruthy()
  })

  it('falls back to unknown repo with no project', () => {
    render(<CrewGroupHeader entry={entry()} />)
    expect(screen.getByText('unknown repo')).toBeTruthy()
  })

  it('links Dispatch here to the repo the project name resolves to', async () => {
    fetchOptions.mockResolvedValue({ repos: [{ name: 'dispatcher', path: '/r/dispatcher' }] } as never)
    render(<CrewGroupHeader entry={entry('dispatcher')} />)
    await waitFor(() => expect(screen.getByText('Dispatch here')).toBeTruthy())
  })

  it('links Dispatch here to the peer host for a crew on a peer, without a local options fetch', async () => {
    fetchOptions.mockResolvedValue({ repos: [{ name: 'dispatcher', path: '/r/dispatcher' }] } as never)
    render(<CrewGroupHeader entry={{ ...entry('dispatcher'), host: 'b' }} />)
    expect(screen.getByText('Dispatch here').getAttribute('href')).toBe('#/dispatch?host=b&crew=1-1')
    await Promise.resolve()
    expect(fetchOptions).not.toHaveBeenCalled()
  })
})
