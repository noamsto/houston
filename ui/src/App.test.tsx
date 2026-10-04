import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import App from './App'

vi.mock('./fleet/Shell', () => ({ Shell: () => 'fleet-shell' }))

afterEach(() => {
  cleanup()
  window.location.hash = ''
})

describe('App', () => {
  it.each(['', '#/agents', '#/panes', '#/fleet', '#/nonsense'])(
    'renders the fleet shell for hash %s',
    (hash) => {
      window.location.hash = hash
      render(<App />)
      expect(screen.getByText('fleet-shell')).toBeTruthy()
    },
  )
})
