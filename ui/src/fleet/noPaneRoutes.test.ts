import { describe, expect, it } from 'vitest'

// #59 moved fleet/ to run-addressed terminal I/O; the legacy /api/pane/*
// routes must stay confined to the classic views (api/terminal.ts's pane
// branch, SplitContainer). This is a static guard, not a behavioral test —
// scans non-test sources only, so it can't see itself or other test files.
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
