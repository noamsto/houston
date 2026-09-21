import { describe, expect, it } from 'vitest'

// The legacy /api/pane/* routes stay confined to the classic views (the pane
// branch of api/terminal.ts). Scans non-test sources only, so the literal in
// this file and in other tests' assertions doesn't trip it.
const fleetSources = import.meta.glob('./**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<
  string,
  string
>

describe('fleet/ never builds a /api/pane route', () => {
  for (const [path, source] of Object.entries(fleetSources)) {
    if (path.endsWith('.test.ts') || path.endsWith('.test.tsx')) continue

    it(`${path} has no /api/pane or paneWsTarget reference`, () => {
      expect(source).not.toContain('/api/pane')
      expect(source).not.toContain('paneWsTarget')
    })
  }
})

describe('MobileInputBar has no /api/pane literal', () => {
  const modules = import.meta.glob('../components/MobileInputBar.tsx', {
    query: '?raw',
    import: 'default',
    eager: true,
  }) as Record<string, string>

  it('the legacy URL lives only in api/terminal.ts', () => {
    const [source] = Object.values(modules)
    expect(source).not.toContain('/api/pane')
  })
})
