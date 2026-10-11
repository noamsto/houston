import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { CrewGroupHeader } from './CrewGroupHeader'
import type { FleetEntry } from './fleetEntries'

type CrewEntry = Extract<FleetEntry, { kind: 'crew' }>

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
  it('shows the crew project, which is the home repo for a mixed crew', () => {
    render(<CrewGroupHeader entry={entry('dispatcher')} />)
    expect(screen.getByText('dispatcher')).toBeTruthy()
  })

  it('falls back to unknown repo with no project', () => {
    render(<CrewGroupHeader entry={entry()} />)
    expect(screen.getByText('unknown repo')).toBeTruthy()
  })
})
