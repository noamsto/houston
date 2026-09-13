import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import App from './App'
import { viewForHash } from './view'

vi.mock('./fleet/Shell', () => ({ Shell: () => 'fleet-shell' }))
vi.mock('./components/agents/AgentsView', () => ({ AgentsView: () => 'agents-view' }))

afterEach(() => {
  cleanup()
  window.location.hash = ''
})

describe('viewForHash', () => {
  it.each([
    ['', 'fleet'],
    ['#', 'fleet'],
    ['#/', 'fleet'],
    ['#/fleet', 'fleet'],
    ['#/fleet/pane-1/terminal', 'fleet'],
    ['#/nonsense', 'fleet'],
    ['#/agents', 'agents'],
    ['#/panes', 'panes'],
  ] as const)('%s -> %s', (hash, expected) => {
    expect(viewForHash(hash)).toBe(expected)
  })
})

describe('App', () => {
  it('renders the fleet shell for an empty hash', () => {
    window.location.hash = ''
    render(<App />)
    expect(screen.getByText('fleet-shell')).toBeTruthy()
  })

  it('renders the agents view for #/agents', () => {
    window.location.hash = '#/agents'
    render(<App />)
    expect(screen.getByText('agents-view')).toBeTruthy()
  })
})
